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
	cidataMediaDescriptor   = 0xF8
)

// cidataGeometry holds the derived FAT layout.
//
// The FAT has to be large enough to hold an entry for every cluster, but the
// cluster count itself depends on how many sectors the FAT consumes. An
// earlier version hardcoded sectors-per-FAT to a value copied from a drive
// with different dimensions, which produced a filesystem with 6125 clusters
// and room for only 5118 entries -- something the code's own round-trip test
// happily accepted, while fsck.fat and mtools both refused to mount it.
// Solving the dependency here keeps the geometry correct if the image size or
// cluster size is ever changed.
type cidataGeometry struct {
	sectorsPerFAT int
	fatOffset     int
	rootOffset    int
	dataOffset    int
	clusterSize   int
	clusters      int
	fatEntries    int
}

func computeCidataGeometry() cidataGeometry {
	rootDirSectors := cidataRootEntries * 32 / cidataBytesPerSector

	spf := 1
	for {
		dataSectors := cidataTotalSectors - cidataReservedSectors - cidataNumFATs*spf - rootDirSectors
		clusters := dataSectors / cidataSectorsPerCluster

		// Clusters are numbered from 2, so the FAT needs clusters+2 entries.
		neededSectors := ((clusters+2)*2 + cidataBytesPerSector - 1) / cidataBytesPerSector
		if neededSectors <= spf {
			break
		}

		spf = neededSectors
	}

	dataSectors := cidataTotalSectors - cidataReservedSectors - cidataNumFATs*spf - rootDirSectors
	rootOffset := (cidataReservedSectors + cidataNumFATs*spf) * cidataBytesPerSector

	return cidataGeometry{
		sectorsPerFAT: spf,
		fatOffset:     cidataReservedSectors * cidataBytesPerSector,
		rootOffset:    rootOffset,
		dataOffset:    rootOffset + cidataRootEntries*32,
		clusterSize:   cidataSectorsPerCluster * cidataBytesPerSector,
		clusters:      dataSectors / cidataSectorsPerCluster,
		fatEntries:    spf * cidataBytesPerSector / 2,
	}
}

// cidataFile is one file written into the config drive's root directory.
//
// NoCloud file names cannot be expressed as 8.3 names: "user-data" is nine
// characters. A FAT directory entry only holds 8+3, so writing the short name
// alone produces a file genuinely called USER-DAT, and Talos -- which opens
// "user-data" -- gets ENOENT from a filesystem it mounted successfully. Each
// file therefore needs VFAT long-file-name entries carrying the real name,
// which is what mkfs.fat/mcopy and Xen Orchestra both emit.
type cidataFile struct {
	longName  string // the name Talos opens, e.g. "user-data"
	shortName string // 8.3 alias, e.g. "USER-D~1"
	content   []byte
}

// buildCidataImage renders a NoCloud config drive containing the Omni join
// config. The returned bytes are a complete filesystem image, ready to be
// uploaded as a VDI and attached to the VM.
func buildCidataImage(userData, metaData, networkConfig string) ([]byte, error) {
	files := []cidataFile{
		{longName: "user-data", shortName: "USER-D~1", content: []byte(userData)},
		{longName: "meta-data", shortName: "META-D~1", content: []byte(metaData)},
		{longName: "network-config", shortName: "NETWOR~1", content: []byte(networkConfig)},
	}

	geom := computeCidataGeometry()

	img := make([]byte, cidataTotalSectors*cidataBytesPerSector)

	writeCidataBootSector(img, geom)

	fatOffset := geom.fatOffset
	rootOffset := geom.rootOffset
	dataOffset := geom.dataOffset
	clusterSize := geom.clusterSize

	// Cluster 0 and 1 are reserved by the FAT spec; user data starts at 2.
	fat := make([]uint16, geom.fatEntries)
	fat[0] = 0xFF00 | uint16(cidataMediaDescriptor)
	fat[1] = 0xFFFF

	nextCluster := uint16(2)
	dirIndex := 0

	// The volume label lives in the root directory as well as the boot sector.
	// Probers read the boot sector, but fsck.fat complains if the two disagree,
	// so write the same lowercase form in both.
	writeCidataDirEntry(img[rootOffset:], cidataLabel, "", 0x08, 0, 0)
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
				return nil, fmt.Errorf("cidata image too small for %q", f.longName)
			}

			offset := dataOffset + (int(cur)-2)*clusterSize
			if offset+clusterSize > len(img) {
				return nil, fmt.Errorf("cidata image too small for %q", f.longName)
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

		// Long-file-name entries precede the short entry and are stored in
		// reverse order, highest sequence number first.
		lfnEntries := (len([]rune(f.longName)) + lfnCharsPerEntry - 1) / lfnCharsPerEntry
		if dirIndex+lfnEntries >= cidataRootEntries {
			return nil, fmt.Errorf("too many files for the cidata root directory")
		}

		checksum := lfnChecksum(f.shortName)

		for seq := lfnEntries; seq >= 1; seq-- {
			last := seq == lfnEntries
			writeCidataLFNEntry(img[rootOffset+dirIndex*32:], f.longName, seq, last, checksum)
			dirIndex++
		}

		writeCidataDirEntry(
			img[rootOffset+dirIndex*32:],
			f.shortName, "", 0x20, start, uint32(len(f.content)),
		)
		dirIndex++
	}

	// Both FAT copies must match; fsck and some probers compare them.
	fatBytes := new(bytes.Buffer)
	if err := binary.Write(fatBytes, binary.LittleEndian, fat); err != nil {
		return nil, fmt.Errorf("failed to serialize FAT: %w", err)
	}

	for i := range cidataNumFATs {
		copy(img[fatOffset+i*geom.sectorsPerFAT*cidataBytesPerSector:], fatBytes.Bytes())
	}

	return img, nil
}

func writeCidataBootSector(img []byte, geom cidataGeometry) {
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
	binary.LittleEndian.PutUint16(img[22:], uint16(geom.sectorsPerFAT))
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

// lfnCharsPerEntry is fixed by the VFAT layout: 5 characters at offset 1, 6 at
// offset 14 and 2 at offset 28, all UTF-16LE.
const lfnCharsPerEntry = 13

// lfnChecksum derives the checksum that ties long-file-name entries to their
// 8.3 entry. A driver that computes a different value discards the long name
// and falls back to the short one, which is the failure this whole mechanism
// exists to avoid. The rotate-and-add is specified by VFAT; the byte
// arithmetic is expected to wrap.
func lfnChecksum(shortName string) byte {
	padded := shortName + strings.Repeat(" ", 11-len(shortName))

	var sum byte
	for i := range 11 {
		sum = byte((sum&1)<<7) + (sum >> 1) + padded[i]
	}

	return sum
}

func writeCidataLFNEntry(dst []byte, longName string, seq int, last bool, checksum byte) {
	order := byte(seq)
	if last {
		order |= 0x40
	}

	dst[0] = order
	dst[11] = 0x0F // attribute marking this as a long-name entry
	dst[12] = 0
	dst[13] = checksum
	dst[26] = 0 // first cluster is always zero in a long-name entry
	dst[27] = 0

	runes := []rune(longName)
	// Character slots, in the order VFAT stores them within the entry.
	slots := []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}

	for i, at := range slots {
		idx := (seq-1)*lfnCharsPerEntry + i

		var val uint16
		switch {
		case idx < len(runes):
			val = uint16(runes[idx])
		case idx == len(runes):
			val = 0x0000 // NUL terminator
		default:
			val = 0xFFFF // unused slots are padded with 0xFFFF
		}

		binary.LittleEndian.PutUint16(dst[at:], val)
	}
}

func writeCidataDirEntry(dst []byte, name, ext string, attr byte, cluster uint16, size uint32) {
	copy(dst[0:8], []byte(name+strings.Repeat(" ", 8-len(name))))
	copy(dst[8:11], []byte(ext+strings.Repeat(" ", 3-len(ext))))
	dst[11] = attr

	binary.LittleEndian.PutUint16(dst[26:], cluster)
	binary.LittleEndian.PutUint32(dst[28:], size)
}
