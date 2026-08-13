// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
)

// Xen Orchestra can build a cloud-init config drive itself, from the
// cloudConfig/networkConfig parameters of vm.create, and the files it writes
// are correct. Its layout is not: it wraps the FAT filesystem in an MBR whose
// only partition starts at LBA 1. A partition table like that is ambiguous --
// a prober cannot distinguish it from a whole-disk ("superfloppy") filesystem
// -- so Talos's block discovery rejects the table, then finds no filesystem at
// offset 0 either, because offset 0 holds the MBR rather than a FAT boot
// sector. The drive shows up in Talos as a bare disk with no type and no
// label, the cidata volume is reported missing, and the machine sits in
// maintenance mode forever holding a perfectly good join config it cannot see.
//
// This builds the same thing without the MBR: a plain FAT16 filesystem,
// labelled cidata, starting at offset 0, which is exactly the layout
// `mkfs.fat -F 16 -n cidata` produces on a raw file and what cloud-init's
// NoCloud datasource expects.
const (
	cidataLabel             = "cidata"
	cidataBytesPerSector    = 512
	cidataSectorsPerCluster = 4
	cidataReservedSectors   = 1
	cidataNumFATs           = 2
	cidataRootEntries       = 512
	cidataTotalSectors      = 24576 // 12 MiB, matching what XO allocates
	cidataSectorsPerFAT     = 20
	cidataMediaDescriptor   = 0xF8
)

// cidataFile is one file written into the config drive's root directory.
type cidataFile struct {
	name    string // 8.3 short name, e.g. "USER-DAT"
	ext     string // 8.3 extension, blank for these
	content []byte
}

// buildCidataImage renders a NoCloud config drive containing the Omni join
// config. The returned bytes are a complete filesystem image, ready to be
// uploaded as a VDI and attached to the VM.
func buildCidataImage(userData, metaData, networkConfig string) ([]byte, error) {
	files := []cidataFile{
		{name: "USER-DAT", content: []byte(userData)},
		{name: "META-DAT", content: []byte(metaData)},
		{name: "NETWORK-", content: []byte(networkConfig)},
	}

	img := make([]byte, cidataTotalSectors*cidataBytesPerSector)

	writeCidataBootSector(img)

	fatOffset := cidataReservedSectors * cidataBytesPerSector
	rootOffset := (cidataReservedSectors + cidataNumFATs*cidataSectorsPerFAT) * cidataBytesPerSector
	dataOffset := rootOffset + cidataRootEntries*32
	clusterSize := cidataSectorsPerCluster * cidataBytesPerSector

	// Cluster 0 and 1 are reserved by the FAT spec; user data starts at 2.
	fat := make([]uint16, cidataSectorsPerFAT*cidataBytesPerSector/2)
	fat[0] = 0xFF00 | uint16(cidataMediaDescriptor)
	fat[1] = 0xFFFF

	nextCluster := uint16(2)
	dirIndex := 0

	// The volume label lives in the root directory as well as the boot sector;
	// blkid reads the boot sector, but keeping both consistent avoids tools
	// disagreeing about the label.
	writeCidataDirEntry(img[rootOffset:], strings.ToUpper(cidataLabel), "", 0x08, 0, 0)
	dirIndex++

	for _, f := range files {
		if len(f.content) == 0 {
			continue
		}

		clusters := (len(f.content) + clusterSize - 1) / clusterSize
		start := nextCluster

		for i := range clusters {
			cur := start + uint16(i)
			if int(cur) >= len(fat) {
				return nil, fmt.Errorf("cidata image too small for %q", f.name)
			}

			offset := dataOffset + (int(cur)-2)*clusterSize
			if offset+clusterSize > len(img) {
				return nil, fmt.Errorf("cidata image too small for %q", f.name)
			}

			chunk := f.content[i*clusterSize:]
			if len(chunk) > clusterSize {
				chunk = chunk[:clusterSize]
			}
			copy(img[offset:], chunk)

			if i == clusters-1 {
				fat[cur] = 0xFFFF // end of chain
			} else {
				fat[cur] = cur + 1
			}
		}

		nextCluster += uint16(clusters)

		if dirIndex >= cidataRootEntries {
			return nil, fmt.Errorf("too many files for the cidata root directory")
		}

		writeCidataDirEntry(
			img[rootOffset+dirIndex*32:],
			f.name, f.ext, 0x20, start, uint32(len(f.content)),
		)
		dirIndex++
	}

	// Both FAT copies must match; fsck and some probers compare them.
	fatBytes := new(bytes.Buffer)
	if err := binary.Write(fatBytes, binary.LittleEndian, fat); err != nil {
		return nil, fmt.Errorf("failed to serialize FAT: %w", err)
	}

	for i := range cidataNumFATs {
		copy(img[fatOffset+i*cidataSectorsPerFAT*cidataBytesPerSector:], fatBytes.Bytes())
	}

	return img, nil
}

func writeCidataBootSector(img []byte) {
	// x86 jump instruction; not executed, but probers expect a plausible one.
	copy(img[0:], []byte{0xEB, 0x3C, 0x90})
	copy(img[3:], []byte("mkfs.fat"))

	binary.LittleEndian.PutUint16(img[11:], cidataBytesPerSector)
	img[13] = cidataSectorsPerCluster
	binary.LittleEndian.PutUint16(img[14:], cidataReservedSectors)
	img[16] = cidataNumFATs
	binary.LittleEndian.PutUint16(img[17:], cidataRootEntries)
	binary.LittleEndian.PutUint16(img[19:], cidataTotalSectors)
	img[21] = cidataMediaDescriptor
	binary.LittleEndian.PutUint16(img[22:], cidataSectorsPerFAT)
	binary.LittleEndian.PutUint16(img[24:], 32) // sectors per track
	binary.LittleEndian.PutUint16(img[26:], 64) // heads

	img[38] = 0x29 // extended boot signature: serial, label and type follow
	binary.LittleEndian.PutUint32(img[39:], 0x11223344)

	// Label is padded to 11 bytes. Talos matches it case-insensitively, and
	// mkfs.fat writes it lowercase here, so this mirrors that exactly.
	label := cidataLabel + strings.Repeat(" ", 11-len(cidataLabel))
	copy(img[43:], label)
	copy(img[54:], "FAT16   ")

	img[510] = 0x55
	img[511] = 0xAA
}

func writeCidataDirEntry(dst []byte, name, ext string, attr byte, cluster uint16, size uint32) {
	copy(dst[0:8], []byte(name+strings.Repeat(" ", 8-len(name))))
	copy(dst[8:11], []byte(ext+strings.Repeat(" ", 3-len(ext))))
	dst[11] = attr

	binary.LittleEndian.PutUint16(dst[26:], cluster)
	binary.LittleEndian.PutUint32(dst[28:], size)
}
