// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/siderolabs/omni/client/pkg/imagefactory"
	"github.com/siderolabs/omni/client/pkg/infra/provision"
	"github.com/ulikunitz/xz"
	xoaclient "github.com/vatesfr/xenorchestra-go-sdk/client"
	"go.uber.org/zap"

	"github.com/kreove/omni-infra-provider-xoa/internal/pkg/provider/data"
	"github.com/kreove/omni-infra-provider-xoa/internal/pkg/provider/resources"
)

const (
	imageCachePrefix = "omni-talos-"
	managedTag       = "omni-managed"

	// bootFirmware is fixed to UEFI. Talos images for XCP-ng are built to boot
	// via UEFI (Sidero's Xen Orchestra guide uses a "Generic Linux UEFI"
	// template), and a BIOS VM cannot boot them at all. This is not exposed as
	// a Machine Class option because BIOS is never a working choice here; use
	// a manual template_id if you need different firmware.
	bootFirmware = "uefi"

	// baseSeedTemplateName is the built-in, diskless XO template used as the
	// starting point when building a new golden Talos template. XO ships this
	// template on every pool.
	baseSeedTemplateName = "Other install media"

	// talosPlatform is the Talos platform this provider provisions. NoCloud is
	// what makes the guest read its machine config from the config drive the
	// provider attaches.
	talosPlatform = "nocloud"

	// talosDiskFormat is the disk image the factory publishes for XCP-ng use.
	// It arrives xz-compressed and is decompressed before upload, because XO
	// imports a raw VDI.
	talosDiskFormat = "raw.xz"

	imageBuildTimeout = 45 * time.Minute
)

// imageBuild tracks the state of an in-progress or completed golden-template
// build for a single deterministic cache name. Builds run in a detached
// goroutine so that ensureTalosImage can return quickly and let the
// provisioning framework retry via provision.NewRetryInterval instead of
// blocking a single step call for the lifetime of a multi-gigabyte
// download+upload.
type imageBuild struct {
	mu         sync.Mutex
	done       bool
	templateID string
	err        error
}

// imageSource is everything a detached golden-template build needs.
//
// It is copied out of the provision context on purpose. The context belongs to
// the step that created it, and the build outlives that step.
type imageSource struct {
	url          string
	headers      http.Header
	schematicID  string
	talosVersion string
}

// ensureTalosImage resolves a manually supplied XO template or builds and
// reuses a cached "golden" Talos template imported from Image Factory. The
// returned boolean is false while a build is still in progress.
func (p *Provisioner) ensureTalosImage(
	ctx context.Context,
	logger *zap.Logger,
	pctx provision.Context[*resources.Machine],
	providerData data.Data,
) (string, bool, error) {
	if providerData.TemplateID != "" {
		templates, err := p.client.GetTemplate(xoaclient.Template{Id: providerData.TemplateID})
		if err != nil {
			return "", false, fmt.Errorf("failed to resolve XO template %q: %w", providerData.TemplateID, err)
		}

		if len(templates) != 1 {
			return "", false, fmt.Errorf("XO template %q did not resolve to exactly one template", providerData.TemplateID)
		}

		return templates[0].Id, true, nil
	}

	// Omni owns the schematic upload and knows how its image factory spells a
	// medium, so the provider asks for one by description rather than building
	// a factory URL itself. This also keeps working against a factory that
	// authenticates downloads, which a hand-built URL would not.
	media, err := pctx.EnsureInstallationMedia(
		ctx,
		logger,
		mediaSpecFor(providerData),
		// Keep the serial console for hosts that provide one, but make tty0
		// the last console= so it owns /dev/console. XCP-ng HVM guests do
		// not get a serial port unless one is configured, and the VergeOS
		// provider this was ported from enabled one explicitly. Without
		// tty0 every message after early boot -- including kernel panics --
		// is written to a device that does not exist, leaving the XO
		// console blank and the failure invisible.
		provision.WithExtraKernelArgs("console=ttyS0,38400n8", "console=tty0"),
		provision.WithoutConnectionParams(),
	)
	if err != nil {
		return "", false, fmt.Errorf("failed to resolve Talos installation media: %w", err)
	}

	pctx.State.TypedSpec().Value.Schematic = media.SchematicID
	pctx.State.TypedSpec().Value.TalosVersion = pctx.GetTalosVersion()

	source := imageSource{
		url:          media.URL,
		headers:      media.Headers,
		schematicID:  media.SchematicID,
		talosVersion: pctx.GetTalosVersion(),
	}

	return p.ensureGoldenTemplate(ctx, logger, providerData, source, cacheNameFor(media))
}

// mediaSpecFor describes the installation medium a Machine Class asks for.
func mediaSpecFor(providerData data.Data) provision.MediaSpec {
	return provision.MediaSpec{
		MediaSpec: imagefactory.MediaSpec{
			Kind:         imagefactory.InstallationMediaKindDisk,
			Platform:     talosPlatform,
			Architecture: providerData.Architecture,
			Format:       talosDiskFormat,
		},
		// The URL has to outlast the download, which runs detached from the
		// step that requested it. Omni's default assumes the fetch happens
		// immediately, which is not true here.
		DownloadTokenTTL: imageBuildTimeout,
	}
}

// cacheNameFor derives the XO template name for a medium.
//
// StorageKey exists for exactly this: it identifies the medium and changes only
// when the medium does. The URL must not be used instead -- it can carry
// credentials or a download token, so a name derived from it would change
// whenever those rotate and orphan the template already built under the old
// name.
func cacheNameFor(media imagefactory.InstallationMedia) string {
	return imageCachePrefix + media.StorageKey
}

func (p *Provisioner) ensureGoldenTemplate(
	ctx context.Context,
	logger *zap.Logger,
	providerData data.Data,
	source imageSource,
	cacheName string,
) (string, bool, error) {
	buildAny, loaded := p.imageBuilds.LoadOrStore(cacheName, &imageBuild{})

	build, ok := buildAny.(*imageBuild)
	if !ok {
		return "", false, fmt.Errorf("invalid internal image build state for %q", cacheName)
	}

	if !loaded {
		templates, err := p.client.GetTemplate(xoaclient.Template{NameLabel: cacheName, PoolId: providerData.PoolID})

		switch {
		case err == nil && len(templates) == 1:
			build.mu.Lock()
			build.done = true
			build.templateID = templates[0].Id
			build.mu.Unlock()
		case err == nil && len(templates) > 1:
			p.imageBuilds.Delete(cacheName)

			return "", false, fmt.Errorf("multiple XO templates named %q in pool %q", cacheName, providerData.PoolID)
		case !isNotFoundErr(err):
			p.imageBuilds.Delete(cacheName)

			return "", false, fmt.Errorf("failed to inspect XO template cache: %w", err)
		default:
			// The media URL is deliberately absent from this line: it can
			// carry credentials or a download token.
			logger.Info(
				"starting Talos image import",
				zap.String("name", cacheName),
				zap.String("schematic", source.schematicID),
				zap.String("talos_version", source.talosVersion),
			)

			go p.buildGoldenTemplate(providerData, source, cacheName, build)
		}
	}

	build.mu.Lock()
	defer build.mu.Unlock()

	if build.err != nil {
		err := build.err
		// Allow a later Machine Request to retry the build from scratch
		// instead of being stuck behind a permanently failed attempt.
		p.imageBuilds.Delete(cacheName)

		return "", false, err
	}

	if !build.done {
		return "", false, nil
	}

	return build.templateID, true, nil
}

// buildGoldenTemplate downloads and decompresses the Talos NoCloud raw image,
// uploads it into XO as a VDI, attaches it to a fresh diskless VM, and
// converts that VM into a clonable template. It runs in its own goroutine
// and reports the outcome through build.
//
// The VDI-attach and convert-to-template calls are not wrapped by the XO Go
// SDK, so they go through the client's raw Call escape hatch (vm.attachDisk,
// vm.convertToTemplate). Both were confirmed against a live XO instance.
func (p *Provisioner) buildGoldenTemplate(providerData data.Data, source imageSource, cacheName string, build *imageBuild) {
	ctx, cancel := context.WithTimeout(context.Background(), imageBuildTimeout)
	defer cancel()

	templateID, err := p.importGoldenTemplate(ctx, providerData, source, cacheName)

	build.mu.Lock()
	defer build.mu.Unlock()

	if err != nil {
		build.err = err

		return
	}

	build.templateID = templateID
	build.done = true
}

func (p *Provisioner) importGoldenTemplate(
	ctx context.Context,
	providerData data.Data,
	source imageSource,
	cacheName string,
) (string, error) {
	rawPath, err := downloadAndDecompress(ctx, source)
	if err != nil {
		return "", fmt.Errorf("failed to download Talos image: %w", err)
	}
	defer os.Remove(rawPath)

	vdi, err := p.client.CreateVDI(xoaclient.CreateVDIReq{
		SRId:      providerData.SRID,
		Filepath:  rawPath,
		NameLabel: cacheName,
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload Talos image into XO SR %q: %w", providerData.SRID, err)
	}

	baseTemplates, err := p.client.GetTemplate(xoaclient.Template{
		NameLabel: baseSeedTemplateName,
		PoolId:    providerData.PoolID,
	})
	if err != nil || len(baseTemplates) != 1 {
		return "", fmt.Errorf(
			"failed to resolve base seed template %q in pool %q: %w",
			baseSeedTemplateName, providerData.PoolID, err,
		)
	}

	var vmID string
	seedParams := map[string]interface{}{
		"name_label": cacheName,
		// The name is a digest, so it identifies the image uniquely but tells
		// an operator nothing. Record what the image actually is: the Talos
		// version and schematic are what someone deciding whether a cached
		// template is still needed has to know. The source URL is deliberately
		// not recorded -- it can carry credentials.
		"name_description": describeTemplate(source, providerData),
		"template":         baseTemplates[0].Id,
		"CPUs":             1,
		"bootAfterCreate":  false,
		"tags":             []string{managedTag},
		// Talos on XCP-ng requires UEFI; Sidero's Xen Orchestra guide builds
		// its template on "Generic Linux UEFI" for exactly this reason. The
		// base seed template used here defaults to BIOS, and a BIOS VM cannot
		// boot the Talos nocloud image -- it powers on, fails, and halts.
		"hvmBootFirmware": bootFirmware,
	}
	if err = p.client.Call("vm.create", seedParams, &vmID); err != nil {
		return "", fmt.Errorf("failed to create seed VM for %q: %w", cacheName, err)
	}

	attachParams := map[string]interface{}{
		"vm":  vmID,
		"vdi": vdi.VDIId,
	}

	var attached bool
	if err = p.client.Call("vm.attachDisk", attachParams, &attached); err != nil {
		return "", fmt.Errorf("failed to attach imported disk to seed VM %q: %w", vmID, err)
	}

	var convertResult interface{}
	if err = p.client.Call("vm.convertToTemplate", map[string]interface{}{"id": vmID}, &convertResult); err != nil {
		return "", fmt.Errorf("failed to convert seed VM %q into a template: %w", vmID, err)
	}

	return vmID, nil
}

// describeTemplate renders the description stored on a cached golden template.
func describeTemplate(source imageSource, providerData data.Data) string {
	return fmt.Sprintf(
		"Talos %s %s, schematic %s, golden image managed by Sidero Omni",
		source.talosVersion,
		providerData.Architecture,
		source.schematicID,
	)
}

// downloadAndDecompress streams the installation medium (a .raw.xz Talos
// NoCloud image) to a scratch file, decompressing as it goes, and returns the
// scratch file path.
func downloadAndDecompress(ctx context.Context, source imageSource) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.url, nil)
	if err != nil {
		return "", err
	}

	// A factory that authenticates downloads returns them here. They are sent
	// whenever present rather than decided from configuration, because the
	// same factory may authenticate by header or inside the URL depending on
	// how Omni is set up.
	for key, values := range source.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The URL is not included: it can carry a download token.
		return "", fmt.Errorf("unexpected HTTP status %d from the image factory", resp.StatusCode)
	}

	xzReader, err := xz.NewReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to initialize xz decompression: %w", err)
	}

	out, err := os.CreateTemp("", "omni-talos-*.raw")
	if err != nil {
		return "", err
	}
	defer out.Close()

	if _, err = io.Copy(out, xzReader); err != nil {
		os.Remove(out.Name())

		return "", fmt.Errorf("failed to decompress image: %w", err)
	}

	return out.Name(), nil
}

func isNotFoundErr(err error) bool {
	_, ok := err.(xoaclient.NotFound)

	return ok
}
