package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"unicode/utf16"
)

const (
	volumeDiskExtentsHeaderSize = 8 // DWORD count plus native x64 alignment.
	volumeDiskExtentSize        = 24
)

// partitionInformationEX mirrors the Windows amd64 PARTITION_INFORMATION_EX
// layout. PartitionInfo is the 112-byte MBR/GPT union at offset 32.
type partitionInformationEX struct {
	PartitionStyle     uint32
	Reserved           [4]byte
	StartingOffset     int64
	PartitionLength    int64
	PartitionNumber    uint32
	RewritePartition   byte
	IsServicePartition byte
	Reserved2          [2]byte
	PartitionInfo      [112]byte
}

type WindowsPartitionIdentity struct {
	Style   DiskLayoutStyle
	MBRType byte
	GPTType [16]byte
}

const partitionLDMTypeMBR byte = 0x42

var (
	// GUID fields are stored in native Windows memory order: the first three
	// fields are little-endian, followed by the final eight bytes verbatim.
	partitionLDMDataGUID     = [16]byte{0xa0, 0x60, 0x9b, 0xaf, 0x31, 0x14, 0x62, 0x4f, 0xbc, 0x68, 0x33, 0x11, 0x71, 0x4a, 0x69, 0xad}
	partitionLDMMetadataGUID = [16]byte{0xaa, 0xc8, 0x08, 0x58, 0x8f, 0x7e, 0xe0, 0x42, 0x85, 0xd2, 0xe1, 0xe9, 0x04, 0x34, 0xcf, 0xb3}
)

func partitionIdentity(entry partitionInformationEX) (WindowsPartitionIdentity, error) {
	switch entry.PartitionStyle {
	case 0:
		return WindowsPartitionIdentity{Style: DiskLayoutMBR, MBRType: entry.PartitionInfo[0]}, nil
	case 1:
		identity := WindowsPartitionIdentity{Style: DiskLayoutGPT}
		copy(identity.GPTType[:], entry.PartitionInfo[:16])
		return identity, nil
	default:
		return WindowsPartitionIdentity{}, fmt.Errorf("unsupported partition style %d", entry.PartitionStyle)
	}
}

func validateWindowsPartitionIdentities(identities []WindowsPartitionIdentity) error {
	for i, identity := range identities {
		switch identity.Style {
		case DiskLayoutMBR:
			if identity.MBRType == partitionLDMTypeMBR {
				return fmt.Errorf("partition %d uses unsupported MBR LDM type 0x42", i)
			}
		case DiskLayoutGPT:
			if identity.GPTType == partitionLDMDataGUID || identity.GPTType == partitionLDMMetadataGUID {
				return fmt.Errorf("partition %d uses an unsupported GPT LDM partition type", i)
			}
		default:
			return fmt.Errorf("partition %d has unsupported style %q", i, identity.Style)
		}
	}
	return nil
}

type WindowsDiskExtent struct {
	DiskNumber     uint32
	StartingOffset uint64
	Length         uint64
}

type WindowsVolume struct {
	VolumeGUID string
	MountPaths []string
	Extents    []WindowsDiskExtent
}

type WindowsMountKind uint8

const (
	WindowsNoMount WindowsMountKind = iota
	WindowsDriveRootMount
	WindowsDirectoryMount
	WindowsOtherMount
)

type WindowsDiskPlanSegment struct {
	Start     uint64
	End       uint64
	Raw       bool
	Volume    *WindowsVolume
	VSSSource string
}

func parseUTF16MultiSZ(buffer []uint16, used uint32) ([]string, error) {
	if used == 0 || uint64(used) > uint64(len(buffer)) {
		return nil, fmt.Errorf("invalid MULTI_SZ length %d for buffer %d", used, len(buffer))
	}
	data := buffer[:used]
	if (len(data) == 1 && data[0] == 0) || (len(data) == 2 && data[0] == 0 && data[1] == 0) {
		return []string{}, nil
	}
	if len(data) < 2 || data[len(data)-1] != 0 || data[len(data)-2] != 0 {
		return nil, fmt.Errorf("MULTI_SZ is not double-NUL terminated")
	}
	paths := make([]string, 0)
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] != 0 {
			continue
		}
		if i == start {
			if i != len(data)-1 {
				return nil, fmt.Errorf("MULTI_SZ contains data after terminator")
			}
			return paths, nil
		}
		paths = append(paths, string(utf16.Decode(data[start:i])))
		start = i + 1
	}
	return nil, fmt.Errorf("MULTI_SZ terminator missing")
}

func parseVolumeDiskExtents(buffer []byte, bytesReturned uint32) ([]WindowsDiskExtent, error) {
	if uint64(bytesReturned) > uint64(len(buffer)) {
		return nil, fmt.Errorf("extent bytes returned %d exceed buffer %d", bytesReturned, len(buffer))
	}
	data := buffer[:bytesReturned]
	if len(data) < volumeDiskExtentsHeaderSize {
		return nil, fmt.Errorf("extent buffer too short: %d", len(data))
	}
	count := uint64(binary.LittleEndian.Uint32(data[:4]))
	if count > uint64((math.MaxInt-volumeDiskExtentsHeaderSize)/volumeDiskExtentSize) {
		return nil, fmt.Errorf("extent count %d overflows buffer calculation", count)
	}
	required := volumeDiskExtentsHeaderSize + int(count)*volumeDiskExtentSize
	if required > len(data) {
		return nil, fmt.Errorf("extent count %d requires %d bytes, got %d", count, required, len(data))
	}
	extents := make([]WindowsDiskExtent, 0, count)
	for i := 0; i < int(count); i++ {
		offset := volumeDiskExtentsHeaderSize + i*volumeDiskExtentSize
		startSigned := int64(binary.LittleEndian.Uint64(data[offset+8 : offset+16]))
		lengthSigned := int64(binary.LittleEndian.Uint64(data[offset+16 : offset+24]))
		if startSigned < 0 || lengthSigned <= 0 {
			return nil, fmt.Errorf("extent %d has invalid offset/length %d/%d", i, startSigned, lengthSigned)
		}
		start, length := uint64(startSigned), uint64(lengthSigned)
		if start > math.MaxUint64-length {
			return nil, fmt.Errorf("extent %d overflows", i)
		}
		extents = append(extents, WindowsDiskExtent{
			DiskNumber:     binary.LittleEndian.Uint32(data[offset : offset+4]),
			StartingOffset: start,
			Length:         length,
		})
	}
	return extents, nil
}

func classifyWindowsMountPath(path string) WindowsMountKind {
	if path == "" {
		return WindowsNoMount
	}
	if len(path) == 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '\\' || path[2] == '/') {
		return WindowsDriveRootMount
	}
	if len(path) > 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '\\' || path[2] == '/') {
		return WindowsDirectoryMount
	}
	return WindowsOtherMount
}

func supportedVSSSource(volume WindowsVolume) (string, error) {
	driveRoots := make([]string, 0)
	for _, path := range volume.MountPaths {
		if classifyWindowsMountPath(path) == WindowsDriveRootMount {
			driveRoots = append(driveRoots, path)
		}
	}
	if len(driveRoots) == 0 {
		if len(volume.MountPaths) == 0 {
			return "", fmt.Errorf("volume %s has no mount path and no verified VSS source", volume.VolumeGUID)
		}
		return "", fmt.Errorf("volume %s has no supported drive-root VSS source", volume.VolumeGUID)
	}
	sort.Strings(driveRoots)
	return driveRoots[0], nil
}

func buildValidatedWindowsDiskPlan(diskSize uint64, style DiskLayoutStyle, targetDisk uint32, partitions []DiskExtent, identities []WindowsPartitionIdentity, volumes []WindowsVolume) ([]WindowsDiskPlanSegment, error) {
	if len(partitions) != len(identities) {
		return nil, fmt.Errorf("partition geometry/identity count mismatch: %d/%d", len(partitions), len(identities))
	}
	for i, identity := range identities {
		if identity.Style != style {
			return nil, fmt.Errorf("partition %d style %q differs from disk style %q", i, identity.Style, style)
		}
	}
	if err := validateWindowsPartitionIdentities(identities); err != nil {
		return nil, err
	}
	return buildWindowsDiskPlan(diskSize, style, targetDisk, partitions, volumes)
}

func buildWindowsDiskPlan(diskSize uint64, style DiskLayoutStyle, targetDisk uint32, partitions []DiskExtent, volumes []WindowsVolume) ([]WindowsDiskPlanSegment, error) {
	layout := DiskLayout{Size: diskSize, Style: style, Partitions: partitions}
	segments, err := layout.Segments()
	if err != nil {
		return nil, err
	}

	volumeByStart := make(map[uint64]*WindowsVolume)
	for i := range volumes {
		volume := &volumes[i]
		targetExtents := make([]WindowsDiskExtent, 0, 1)
		for _, extent := range volume.Extents {
			if extent.DiskNumber == targetDisk {
				targetExtents = append(targetExtents, extent)
			}
		}
		if len(targetExtents) == 0 {
			continue
		}
		if len(volume.Extents) != 1 || len(targetExtents) != 1 {
			return nil, fmt.Errorf("volume %s spans multiple extents and touches PhysicalDrive%d", volume.VolumeGUID, targetDisk)
		}
		extent := targetExtents[0]
		var partition *DiskExtent
		for j := range partitions {
			if partitions[j].Start == extent.StartingOffset {
				if partition != nil {
					return nil, fmt.Errorf("ambiguous partition mapping at offset %d", extent.StartingOffset)
				}
				partition = &partitions[j]
			}
		}
		if partition == nil {
			return nil, fmt.Errorf("volume %s extent at %d has no partition", volume.VolumeGUID, extent.StartingOffset)
		}
		if extent.Length > partition.End-partition.Start {
			return nil, fmt.Errorf("volume %s extent length %d exceeds partition length %d", volume.VolumeGUID, extent.Length, partition.End-partition.Start)
		}
		if existing := volumeByStart[extent.StartingOffset]; existing != nil {
			return nil, fmt.Errorf("volumes %s and %s map to the same partition", existing.VolumeGUID, volume.VolumeGUID)
		}
		volumeByStart[extent.StartingOffset] = volume
	}

	plan := make([]WindowsDiskPlanSegment, 0, len(segments))
	for _, segment := range segments {
		planned := WindowsDiskPlanSegment{Start: segment.Start, End: segment.End, Raw: true}
		if segment.Data {
			if volume := volumeByStart[segment.Start]; volume != nil {
				source, err := supportedVSSSource(*volume)
				if err != nil {
					return nil, err
				}
				planned.Raw = false
				planned.Volume = volume
				planned.VSSSource = source
			}
		}
		plan = append(plan, planned)
	}
	return plan, nil
}

func vssSourcesForPlan(plan []WindowsDiskPlanSegment) ([]string, error) {
	sources := make([]string, 0, 1)
	seen := make(map[string]bool)
	for _, segment := range plan {
		if segment.Raw || seen[segment.VSSSource] {
			continue
		}
		seen[segment.VSSSource] = true
		sources = append(sources, segment.VSSSource)
	}
	if len(sources) > 1 {
		return nil, fmt.Errorf("multiple VSS source volumes are unsupported by the current snapshot wrapper")
	}
	return sources, nil
}
