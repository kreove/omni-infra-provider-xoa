// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"strings"
	"testing"

	"github.com/siderolabs/omni/client/pkg/imagefactory"
	xoaclient "github.com/vatesfr/xenorchestra-go-sdk/client"

	"github.com/kreove/omni-infra-provider-xoa/internal/pkg/provider/data"
)

func TestMediaSpecFor(t *testing.T) {
	t.Parallel()

	spec := mediaSpecFor(data.Data{Architecture: "amd64"})

	if spec.Kind != imagefactory.InstallationMediaKindDisk {
		t.Errorf("Kind = %q, want a disk image", spec.Kind)
	}

	// NoCloud is what makes Talos read its config from the drive this provider
	// attaches; any other platform would ignore it.
	if spec.Platform != talosPlatform {
		t.Errorf("Platform = %q, want %q", spec.Platform, talosPlatform)
	}

	// XO imports a raw VDI, so the compressed raw image is what gets fetched
	// and decompressed. qcow2 would not be importable here.
	if spec.Format != talosDiskFormat {
		t.Errorf("Format = %q, want %q", spec.Format, talosDiskFormat)
	}

	if spec.Architecture != "amd64" {
		t.Errorf("Architecture = %q, want amd64", spec.Architecture)
	}
}

// The medium is fetched by a goroutine that outlives the step which requested
// the URL, so the default token lifetime -- which assumes an immediate fetch --
// is not enough.
func TestMediaSpecForRequestsALongDownloadToken(t *testing.T) {
	t.Parallel()

	spec := mediaSpecFor(data.Data{Architecture: "amd64"})

	if spec.DownloadTokenTTL < imageBuildTimeout {
		t.Errorf("DownloadTokenTTL = %s, want at least the build timeout %s", spec.DownloadTokenTTL, imageBuildTimeout)
	}
}

// The spec must satisfy the image factory's own rules, or the medium is
// rejected at resolve time rather than here.
func TestMediaSpecForIsValid(t *testing.T) {
	t.Parallel()

	if err := mediaSpecFor(data.Data{Architecture: "amd64"}).Validate(); err != nil {
		t.Errorf("media spec is invalid: %v", err)
	}

	if err := mediaSpecFor(data.Data{}).Validate(); err == nil {
		t.Error("expected an empty architecture to fail media spec validation")
	}
}

// The template name must come from StorageKey, never the URL: the URL can carry
// credentials or a download token, so a name derived from it would change when
// those rotate and orphan the template already built under the old name.
func TestCacheNameForUsesStorageKey(t *testing.T) {
	t.Parallel()

	got := cacheNameFor(imagefactory.InstallationMedia{
		StorageKey:  "abc123",
		URL:         "https://factory.talos.dev/image/x/y/z?token=secret",
		SchematicID: "schematic123",
	})

	if got != imageCachePrefix+"abc123" {
		t.Fatalf("cacheNameFor() = %q, want %q", got, imageCachePrefix+"abc123")
	}

	if strings.Contains(got, "secret") || strings.Contains(got, "token") {
		t.Errorf("cache name %q leaks the download URL", got)
	}
}

func TestCacheNameForIsStableAndDistinct(t *testing.T) {
	t.Parallel()

	first := cacheNameFor(imagefactory.InstallationMedia{StorageKey: "aaa"})

	if first != cacheNameFor(imagefactory.InstallationMedia{StorageKey: "aaa"}) {
		t.Error("cache name is not stable across calls")
	}

	if first == cacheNameFor(imagefactory.InstallationMedia{StorageKey: "bbb"}) {
		t.Error("distinct media must not share a cache name")
	}
}

// The template name is a digest, so the description is the only thing telling
// an operator which Talos build a cached template holds.
func TestDescribeTemplate(t *testing.T) {
	t.Parallel()

	got := describeTemplate(imageSource{
		schematicID:  "schematic123",
		talosVersion: "v1.12.4",
		url:          "https://factory.talos.dev/image?token=secret",
	}, data.Data{Architecture: "amd64"})

	for _, want := range []string{"v1.12.4", "amd64", "schematic123"} {
		if !strings.Contains(got, want) {
			t.Errorf("description %q is missing %q", got, want)
		}
	}

	// The URL can carry credentials and must not be persisted into XO.
	if strings.Contains(got, "secret") || strings.Contains(got, "http") {
		t.Errorf("description %q leaks the download URL", got)
	}
}

func TestIsNotFoundErr(t *testing.T) {
	t.Parallel()

	if !isNotFoundErr(xoaclient.NotFound{}) {
		t.Fatal("expected xoaclient.NotFound to be recognized as not-found")
	}

	if isNotFoundErr(nil) {
		t.Fatal("nil error must not be treated as not-found")
	}
}
