// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import "testing"

// The failure this guards against: a machine provisioned by an older build has
// a config drive named exactly "cidata". If isConfigDrive stopped recognizing
// that, ensureConfigDrive would decide the machine has no config drive and
// build, upload and hot-attach a second one -- on every existing machine, on
// the first reconcile after upgrading the provider.
func TestIsConfigDriveRecognizesLegacyName(t *testing.T) {
	t.Parallel()

	if !isConfigDrive("cidata") {
		t.Error("the pre-rename config drive name must still be recognized; " +
			"otherwise every existing machine gets a second config drive attached")
	}
}

func TestIsConfigDrive(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bool{
		"cidata":                          true, // legacy, pre-rename
		"cidata-talos-worker-01":          true, // per-machine
		"cidata-":                         true, // degenerate, still ours
		"cidata-9dcd1a3f-machine-request": true,
		"disk0":                           false, // the boot disk
		"":                                false,
		"cidatabase":                      false, // prefix-alike, not ours
		"my-cidata":                       false, // suffix-alike, not ours
		"CIDATA":                          false, // XO name labels are case-sensitive
	} {
		if got := isConfigDrive(name); got != want {
			t.Errorf("isConfigDrive(%q) = %v, want %v", name, got, want)
		}
	}
}

// Whatever configDriveNameFor produces must be recognized on the next
// reconcile. If these two ever disagree the provider attaches a fresh config
// drive to the same machine every time it reconciles.
func TestConfigDriveNamingRoundTrips(t *testing.T) {
	t.Parallel()

	for _, vmName := range []string{
		"talos-worker-01",
		"9dcd1a3f-0f4e-4c8a-9c39-2b6d1e5a7c04",
		"cluster-01-test-control-plane-1",
		"",
	} {
		name := configDriveNameFor(vmName)
		if !isConfigDrive(name) {
			t.Errorf("configDriveNameFor(%q) = %q, which isConfigDrive does not recognize", vmName, name)
		}
	}
}

// The drive must be identifiable in the Xen Orchestra disk list, which is the
// entire point of the rename.
func TestConfigDriveNameIdentifiesTheMachine(t *testing.T) {
	t.Parallel()

	got := configDriveNameFor("cluster-01-test-worker-2")
	want := "cidata-cluster-01-test-worker-2"

	if got != want {
		t.Errorf("configDriveNameFor = %q, want %q", got, want)
	}
}

// Talos finds the drive by the FAT volume label inside the image, never by the
// XO name_label. The rename is safe precisely because these are independent,
// so the filesystem label must stay "cidata" regardless of the VDI name.
func TestFATVolumeLabelIsUnaffectedByVDIName(t *testing.T) {
	t.Parallel()

	if cidataLabel != "cidata" {
		t.Fatalf("FAT volume label is %q; Talos NoCloud only probes for \"cidata\"", cidataLabel)
	}

	img, err := buildCidataImage("#!talos\n", noCloudMetaData("talos-worker-01"), "version: 1\n")
	if err != nil {
		t.Fatalf("buildCidataImage failed: %v", err)
	}

	// The label lives in the boot sector at offset 43, padded to 11 bytes.
	if got := string(img[43:54]); got != "cidata     " {
		t.Errorf("boot sector volume label = %q, want %q", got, "cidata     ")
	}
}
