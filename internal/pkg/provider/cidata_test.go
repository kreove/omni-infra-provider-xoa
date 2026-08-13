// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"encoding/binary"
	"strings"
	"testing"
)

const sampleJoinConfig = `apiVersion: v1alpha1
kind: SideroLinkConfig
apiUrl: https://omni.example.com:8090/?jointoken=v2%3Aabcdef
---
apiVersion: v1alpha1
kind: EventSinkConfig
endpoint: '[fdae:41e4:649b:9303::1]:8091'
`

// The whole point of this image is that a prober finds a filesystem at offset
// 0. Xen Orchestra's own config drive fails precisely here: it puts an MBR
// there instead, and Talos then reports the disk as having no type and no
// label. Assert the bytes a prober actually looks at.
func TestCidataImageIsProbeableAtOffsetZero(t *testing.T) {
	t.Parallel()

	img, err := buildCidataImage(sampleJoinConfig, "instance-id: test\n", "version: 1\n")
	if err != nil {
		t.Fatalf("buildCidataImage failed: %v", err)
	}

	if got := string(img[3:11]); got != "mkfs.fat" {
		t.Errorf("OEM name = %q, want %q", got, "mkfs.fat")
	}

	label := strings.TrimSpace(string(img[43:54]))
	if label != cidataLabel {
		t.Errorf("volume label = %q, want %q -- Talos looks for exactly this", label, cidataLabel)
	}

	if got := strings.TrimSpace(string(img[54:62])); got != "FAT16" {
		t.Errorf("fs type = %q, want FAT16", got)
	}

	if img[510] != 0x55 || img[511] != 0xAA {
		t.Errorf("boot signature = %02x%02x, want 55aa", img[510], img[511])
	}

	// An MBR partition entry at 0x1BE is what makes XO's drive undetectable.
	// There must not be one here.
	mbr := img[0x1BE : 0x1BE+16]
	allZero := true
	for _, b := range mbr {
		if b != 0 {
			allZero = false
		}
	}
	if !allZero {
		t.Errorf("found a partition table entry at 0x1BE (%x); the filesystem must live at offset 0", mbr)
	}
}

// Read the image back the way a filesystem driver would, so a corrupt
// directory or FAT chain fails here rather than on a booting machine.
func TestCidataImageFilesAreReadable(t *testing.T) {
	t.Parallel()

	meta := "instance-id: 12345678-1234-1234-1234-123456789abc\n"
	network := "version: 1\n"

	img, err := buildCidataImage(sampleJoinConfig, meta, network)
	if err != nil {
		t.Fatalf("buildCidataImage failed: %v", err)
	}

	files := readCidataRoot(t, img)

	for name, want := range map[string]string{
		"USER-DAT": sampleJoinConfig,
		"META-DAT": meta,
		"NETWORK-": network,
	} {
		got, ok := files[name]
		if !ok {
			t.Errorf("%s missing from the root directory", name)

			continue
		}

		if got != want {
			t.Errorf("%s content mismatch:\n got: %q\nwant: %q", name, got, want)
		}
	}
}

// A join config larger than one cluster must still round-trip, which exercises
// the FAT chain rather than a single-cluster shortcut.
func TestCidataImageHandlesMultiClusterFiles(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("machine-config-line\n", 2000) // ~40 KiB, many clusters

	img, err := buildCidataImage(big, "instance-id: x\n", "version: 1\n")
	if err != nil {
		t.Fatalf("buildCidataImage failed: %v", err)
	}

	files := readCidataRoot(t, img)
	if files["USER-DAT"] != big {
		t.Errorf("multi-cluster user-data did not round-trip (got %d bytes, want %d)",
			len(files["USER-DAT"]), len(big))
	}
}

// readCidataRoot parses the image's boot sector, root directory and FAT chains,
// mirroring what a filesystem driver does.
func readCidataRoot(t *testing.T, img []byte) map[string]string {
	t.Helper()

	bps := int(binary.LittleEndian.Uint16(img[11:]))
	spc := int(img[13])
	resv := int(binary.LittleEndian.Uint16(img[14:]))
	nfat := int(img[16])
	rootEnt := int(binary.LittleEndian.Uint16(img[17:]))
	spf := int(binary.LittleEndian.Uint16(img[22:]))

	fatOff := resv * bps
	rootOff := (resv + nfat*spf) * bps
	dataOff := rootOff + rootEnt*32
	clusterSize := spc * bps

	fatEntry := func(n uint16) uint16 {
		return binary.LittleEndian.Uint16(img[fatOff+int(n)*2:])
	}

	out := map[string]string{}

	for i := range rootEnt {
		e := img[rootOff+i*32 : rootOff+(i+1)*32]
		if e[0] == 0x00 {
			break
		}
		if e[0] == 0xE5 || e[11]&0x08 != 0 || e[11] == 0x0F {
			continue // deleted, volume label, or long-file-name entry
		}

		name := strings.TrimSpace(string(e[0:8]))
		start := binary.LittleEndian.Uint16(e[26:])
		size := int(binary.LittleEndian.Uint32(e[28:]))

		var body strings.Builder
		for clus, read := start, 0; read < size; {
			if clus < 2 || int(clus) >= spf*bps/2 {
				t.Fatalf("%s: invalid cluster %d in chain", name, clus)
			}

			off := dataOff + (int(clus)-2)*clusterSize
			n := min(clusterSize, size-read)
			body.Write(img[off : off+n])
			read += n

			next := fatEntry(clus)
			if next >= 0xFFF8 {
				if read < size {
					t.Fatalf("%s: chain ended after %d of %d bytes", name, read, size)
				}

				break
			}
			clus = next
		}

		out[name] = body.String()
	}

	return out
}
