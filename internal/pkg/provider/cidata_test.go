// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"unicode/utf16"
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
// label.
func TestCidataImageIsProbeableAtOffsetZero(t *testing.T) {
	t.Parallel()

	img, err := buildCidataImage(sampleJoinConfig, "instance-id: test\n", "version: 1\n")
	if err != nil {
		t.Fatalf("buildCidataImage failed: %v", err)
	}

	if got := string(img[3:11]); got != "mkfs.fat" {
		t.Errorf("OEM name = %q, want %q", got, "mkfs.fat")
	}

	if label := strings.TrimSpace(string(img[43:54])); label != cidataLabel {
		t.Errorf("volume label = %q, want %q -- Talos looks for exactly this", label, cidataLabel)
	}

	if got := strings.TrimSpace(string(img[54:62])); got != "FAT16" {
		t.Errorf("fs type = %q, want FAT16", got)
	}

	if img[510] != 0x55 || img[511] != 0xAA {
		t.Errorf("boot signature = %02x%02x, want 55aa", img[510], img[511])
	}

	for _, b := range img[0x1BE : 0x1BE+16] {
		if b != 0 {
			t.Errorf("found a partition table entry at 0x1BE; the filesystem must live at offset 0")

			break
		}
	}
}

// The FAT must be able to address every cluster in the data area. A previous
// version hardcoded sectors-per-FAT and produced 6125 clusters with room for
// 5118 entries; the round-trip test below passed anyway, while fsck.fat and
// mtools both refused to mount the result.
func TestCidataGeometryFATCoversAllClusters(t *testing.T) {
	t.Parallel()

	geom := computeCidataGeometry()

	if geom.fatEntries < geom.clusters+2 {
		t.Fatalf(
			"FAT holds %d entries but the filesystem has %d clusters (needs %d)",
			geom.fatEntries, geom.clusters, geom.clusters+2,
		)
	}

	// FAT16 is selected by cluster count, not by the "FAT16" string in the
	// boot sector. Straying outside this range would silently make the image
	// FAT12 or FAT32 while still claiming FAT16.
	if geom.clusters < 4085 || geom.clusters > 65524 {
		t.Errorf("cluster count %d is outside the FAT16 range (4085..65524)", geom.clusters)
	}
}

// Talos opens "user-data", which cannot be stored as an 8.3 name. Reading the
// directory the way a driver does -- preferring long-file-name entries -- is
// the only assertion that catches a drive whose files are really called
// USER-DAT. An earlier test read back the short names it had itself invented
// and so passed against an image Talos could not use.
func TestCidataFilesAreReadableByTheirLongNames(t *testing.T) {
	t.Parallel()

	meta := "instance-id: 12345678-1234-1234-1234-123456789abc\n"
	network := "version: 1\n"

	img, err := buildCidataImage(sampleJoinConfig, meta, network)
	if err != nil {
		t.Fatalf("buildCidataImage failed: %v", err)
	}

	files := readCidataRoot(t, img)

	for name, want := range map[string]string{
		"user-data":      sampleJoinConfig,
		"meta-data":      meta,
		"network-config": network,
	} {
		got, ok := files[name]
		if !ok {
			t.Errorf("%q not present; directory contains %v", name, keysOf(files))

			continue
		}

		if got != want {
			t.Errorf("%s content mismatch:\n got: %q\nwant: %q", name, got, want)
		}
	}
}

// "network-config" is 14 characters and needs two long-name entries, and a
// large join config spans several clusters; both are exercised here.
func TestCidataHandlesMultiEntryNamesAndMultiClusterFiles(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("machine-config-line\n", 2000) // ~40 KiB

	img, err := buildCidataImage(big, "instance-id: x\n", "version: 1\n")
	if err != nil {
		t.Fatalf("buildCidataImage failed: %v", err)
	}

	files := readCidataRoot(t, img)

	if files["user-data"] != big {
		t.Errorf("multi-cluster user-data did not round-trip (got %d bytes, want %d)",
			len(files["user-data"]), len(big))
	}

	if files["network-config"] != "version: 1\n" {
		t.Errorf("two-entry long name did not round-trip: got %q", files["network-config"])
	}
}

// Writes an image to the path in CIDATA_DUMP so it can be checked with real
// FAT tooling, which is how both bugs in this file were originally found:
//
//	CIDATA_DUMP=/tmp/cidata.img go test ./internal/pkg/provider -run TestCidataDump
//	fsck.fat -n /tmp/cidata.img
//	mdir -i /tmp/cidata.img ::
//	mtype -i /tmp/cidata.img ::user-data
func TestCidataDump(t *testing.T) {
	path := os.Getenv("CIDATA_DUMP")
	if path == "" {
		t.Skip("set CIDATA_DUMP=<path> to write an image for external validation")
	}

	img, err := buildCidataImage(sampleJoinConfig, "instance-id: abc-123\n", "version: 1\n")
	if err != nil {
		t.Fatalf("buildCidataImage failed: %v", err)
	}

	if err := os.WriteFile(path, img, 0o600); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}

	t.Logf("wrote %s (%d bytes)", path, len(img))
}

// readCidataRoot parses the boot sector, root directory and FAT chains,
// resolving long-file-name entries the way a filesystem driver does.
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

	out := map[string]string{}
	var pending []uint16 // long-name characters accumulated so far

	for i := range rootEnt {
		e := img[rootOff+i*32 : rootOff+(i+1)*32]
		if e[0] == 0x00 {
			break
		}

		if e[0] == 0xE5 {
			pending = nil

			continue
		}

		if e[11] == 0x0F {
			// Long-name entries appear in reverse order, so prepend.
			var chunk []uint16
			for _, at := range []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30} {
				c := binary.LittleEndian.Uint16(e[at:])
				if c == 0x0000 || c == 0xFFFF {
					break
				}
				chunk = append(chunk, c)
			}
			pending = append(chunk, pending...)

			continue
		}

		if e[11]&0x08 != 0 || e[11]&0x10 != 0 { // volume label or directory
			pending = nil

			continue
		}

		name := strings.TrimSpace(string(e[0:8]))
		if len(pending) > 0 {
			name = string(utf16.Decode(pending))
		}
		pending = nil

		start := binary.LittleEndian.Uint16(e[26:])
		size := int(binary.LittleEndian.Uint32(e[28:]))

		var body strings.Builder
		for clus, read := start, 0; read < size; {
			if clus < 2 || int(clus)*2 >= spf*bps {
				t.Fatalf("%s: invalid cluster %d in chain", name, clus)
			}

			off := dataOff + (int(clus)-2)*clusterSize
			n := min(clusterSize, size-read)
			body.Write(img[off : off+n])
			read += n

			next := binary.LittleEndian.Uint16(img[fatOff+int(clus)*2:])
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

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}
