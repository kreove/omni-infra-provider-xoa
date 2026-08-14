// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

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

	imageURL, cacheName, err := buildTalosImageReference(
		p.imageFactoryBaseURL,
		pctx.State.TypedSpec().Value.Schematic,
		pctx.GetTalosVersion(),
		providerData.Architecture,
	)
	if err != nil {
		return "", false, err
	}

	return p.ensureGoldenTemplate(ctx, logger, providerData, imageURL, cacheName)
}

func (p *Provisioner) ensureGoldenTemplate(
	ctx context.Context,
	logger *zap.Logger,
	providerData data.Data,
	imageURL, cacheName string,
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
			logger.Info(
				"starting Talos image import",
				zap.String("name", cacheName),
				zap.String("url", imageURL),
			)

			go p.buildGoldenTemplate(providerData, imageURL, cacheName, build)
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
func (p *Provisioner) buildGoldenTemplate(providerData data.Data, imageURL, cacheName string, build *imageBuild) {
	ctx, cancel := context.WithTimeout(context.Background(), imageBuildTimeout)
	defer cancel()

	templateID, err := p.importGoldenTemplate(ctx, providerData, imageURL, cacheName)

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
	imageURL, cacheName string,
) (string, error) {
	rawPath, err := downloadAndDecompress(ctx, imageURL)
	if err != nil {
		return "", fmt.Errorf("failed to download Talos image from %q: %w", imageURL, err)
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
		"name_label":       cacheName,
		// The name is a hash of the source URL, so it identifies the image
		// uniquely but tells an operator nothing. Record the URL it was built
		// from: it names the Talos version and schematic, which is what
		// someone deciding whether a cached template is still needed actually
		// has to know.
		"name_description": "Talos golden image managed by Sidero Omni. Built from " + imageURL,
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

// downloadAndDecompress streams imageURL (a .raw.xz Talos NoCloud image) to a
// scratch file, decompressing as it goes, and returns the scratch file path.
func downloadAndDecompress(ctx context.Context, imageURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
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

func buildTalosImageReference(
	baseURL,
	schematic,
	talosVersion,
	architecture string,
) (imageURL, cacheName string, err error) {
	if strings.TrimSpace(schematic) == "" {
		return "", "", fmt.Errorf("cannot build Talos image URL without a schematic ID")
	}

	if strings.TrimSpace(talosVersion) == "" {
		return "", "", fmt.Errorf("cannot build Talos image URL without a Talos version")
	}

	if strings.TrimSpace(architecture) == "" {
		return "", "", fmt.Errorf("cannot build Talos image URL without an architecture")
	}

	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", "", fmt.Errorf("invalid Image Factory URL %q: %w", baseURL, err)
	}

	if base.Scheme != "https" && base.Scheme != "http" {
		return "", "", fmt.Errorf("Image Factory URL must use HTTP or HTTPS")
	}

	if base.Host == "" {
		return "", "", fmt.Errorf("Image Factory URL %q has no host", baseURL)
	}

	base.Path = path.Join(
		base.Path,
		"image",
		schematic,
		talosVersion,
		fmt.Sprintf("nocloud-%s.raw.xz", architecture),
	)
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""

	imageURL = base.String()
	hash := sha256.Sum256([]byte(imageURL))
	cacheName = imageCachePrefix + hex.EncodeToString(hash[:12])

	return imageURL, cacheName, nil
}

func isNotFoundErr(err error) bool {
	_, ok := err.(xoaclient.NotFound)

	return ok
}
