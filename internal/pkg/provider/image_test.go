// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"strings"
	"testing"

	xoaclient "github.com/vatesfr/xenorchestra-go-sdk/client"
)

func TestBuildTalosImageReference(t *testing.T) {
	t.Parallel()

	imageURL, cacheName, err := buildTalosImageReference(
		"https://factory.talos.dev/",
		"schematic123",
		"v1.12.4",
		"amd64",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantURL := "https://factory.talos.dev/image/schematic123/v1.12.4/nocloud-amd64.raw.xz"
	if imageURL != wantURL {
		t.Fatalf("got URL %q, want %q", imageURL, wantURL)
	}

	if !strings.HasPrefix(cacheName, imageCachePrefix) {
		t.Fatalf("unexpected cache name %q", cacheName)
	}

	secondURL, secondName, err := buildTalosImageReference(
		"https://factory.talos.dev",
		"schematic123",
		"v1.12.4",
		"amd64",
	)
	if err != nil {
		t.Fatalf("unexpected second error: %v", err)
	}

	if imageURL != secondURL || cacheName != secondName {
		t.Fatal("image reference must be deterministic")
	}
}

func TestBuildTalosImageReferenceChangesWithInputs(t *testing.T) {
	t.Parallel()

	base, baseName, err := buildTalosImageReference("https://factory.talos.dev", "schematic123", "v1.12.4", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	otherSchematic, otherSchematicName, err := buildTalosImageReference("https://factory.talos.dev", "schematic456", "v1.12.4", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if base == otherSchematic || baseName == otherSchematicName {
		t.Fatal("changing the schematic must change the image reference")
	}

	otherVersion, otherVersionName, err := buildTalosImageReference("https://factory.talos.dev", "schematic123", "v1.13.0", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if base == otherVersion || baseName == otherVersionName {
		t.Fatal("changing the Talos version must change the image reference")
	}
}

func TestBuildTalosImageReferenceRejectsMissingScheme(t *testing.T) {
	t.Parallel()

	_, _, err := buildTalosImageReference(
		"factory.talos.dev",
		"schematic123",
		"v1.12.4",
		"amd64",
	)
	if err == nil {
		t.Fatal("expected invalid base URL error")
	}
}

func TestBuildTalosImageReferenceRejectsMissingSchematic(t *testing.T) {
	t.Parallel()

	_, _, err := buildTalosImageReference("https://factory.talos.dev", "", "v1.12.4", "amd64")
	if err == nil {
		t.Fatal("expected missing schematic error")
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
