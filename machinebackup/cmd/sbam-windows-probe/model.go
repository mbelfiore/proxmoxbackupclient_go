package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf16"
)

const (
	extentHeaderSize = 8
	extentSize       = 24
)

type mountKind string

const (
	mountNoMount   mountKind = "NO_MOUNT"
	mountDriveRoot mountKind = "DRIVE_ROOT"
	mountDirectory mountKind = "DIRECTORY_MOUNT"
	mountOther     mountKind = "OTHER"
)

type probeExtent struct {
	DiskNumber     uint32
	StartingOffset uint64
	Length         uint64
}

type supportResult struct {
	Supported bool
	Err       string
}

type probeVolume struct {
	GUID             string
	MountPaths       []string
	Extents          []probeExtent
	GUIDSupport      supportResult
	DriveRootSupport map[string]supportResult
}

func classifyMountPath(path string) mountKind {
	if path == "" {
		return mountNoMount
	}
	if len(path) == 3 && isASCIILetter(path[0]) && path[1] == ':' && (path[2] == '\\' || path[2] == '/') {
		return mountDriveRoot
	}
	if len(path) > 3 && isASCIILetter(path[0]) && path[1] == ':' && (path[2] == '\\' || path[2] == '/') {
		return mountDirectory
	}
	return mountOther
}

func isASCIILetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func ensureTrailingBackslash(path string) string {
	if strings.HasSuffix(path, `\`) {
		return path
	}
	return path + `\`
}

func parseMultiSZ(buffer []uint16, used uint32) ([]string, error) {
	if used == 0 || uint64(used) > uint64(len(buffer)) {
		return nil, fmt.Errorf("invalid MULTI_SZ length %d for buffer %d", used, len(buffer))
	}
	data := buffer[:used]
	if len(data) == 1 && data[0] == 0 || len(data) == 2 && data[0] == 0 && data[1] == 0 {
		return []string{}, nil
	}
	if len(data) < 2 || data[len(data)-1] != 0 || data[len(data)-2] != 0 {
		return nil, fmt.Errorf("MULTI_SZ is not double-NUL terminated")
	}
	paths := make([]string, 0)
	start := 0
	for index, value := range data {
		if value != 0 {
			continue
		}
		if index == start {
			if index != len(data)-1 {
				return nil, fmt.Errorf("MULTI_SZ contains data after terminator")
			}
			return paths, nil
		}
		paths = append(paths, string(utf16.Decode(data[start:index])))
		start = index + 1
	}
	return nil, fmt.Errorf("MULTI_SZ terminator missing")
}

func parseExtents(buffer []byte, used uint32) ([]probeExtent, error) {
	if uint64(used) > uint64(len(buffer)) || used < extentHeaderSize {
		return nil, fmt.Errorf("invalid extent response size %d for buffer %d", used, len(buffer))
	}
	data := buffer[:used]
	count := uint64(binary.LittleEndian.Uint32(data[:4]))
	if count > uint64((math.MaxInt-extentHeaderSize)/extentSize) {
		return nil, fmt.Errorf("extent count %d overflows", count)
	}
	required := extentHeaderSize + int(count)*extentSize
	if required > len(data) {
		return nil, fmt.Errorf("extent count %d requires %d bytes, got %d", count, required, len(data))
	}
	extents := make([]probeExtent, 0, count)
	for index := 0; index < int(count); index++ {
		offset := extentHeaderSize + index*extentSize
		startingOffset := int64(binary.LittleEndian.Uint64(data[offset+8 : offset+16]))
		length := int64(binary.LittleEndian.Uint64(data[offset+16 : offset+24]))
		if startingOffset < 0 || length <= 0 {
			return nil, fmt.Errorf("extent %d has invalid offset/length %d/%d", index, startingOffset, length)
		}
		extents = append(extents, probeExtent{
			DiskNumber:     binary.LittleEndian.Uint32(data[offset : offset+4]),
			StartingOffset: uint64(startingOffset),
			Length:         uint64(length),
		})
	}
	return extents, nil
}

func writeVolumeReport(output io.Writer, volume probeVolume) {
	fmt.Fprintln(output, "VOLUME")
	fmt.Fprintf(output, "GUID=%s\n", volume.GUID)
	fmt.Fprintf(output, "MOUNT_COUNT=%d\n", len(volume.MountPaths))
	if len(volume.MountPaths) == 0 {
		fmt.Fprintf(output, "MOUNT_KIND=%s\n", mountNoMount)
	}
	for i, path := range volume.MountPaths {
		fmt.Fprintf(output, "MOUNT_%d=%s\n", i, path)
		fmt.Fprintf(output, "MOUNT_%d_KIND=%s\n", i, classifyMountPath(path))
	}
	fmt.Fprintf(output, "EXTENT_COUNT=%d\n", len(volume.Extents))
	for i, extent := range volume.Extents {
		fmt.Fprintf(output, "EXTENT_%d_DISK=%d\n", i, extent.DiskNumber)
		fmt.Fprintf(output, "EXTENT_%d_OFFSET=%d\n", i, extent.StartingOffset)
		fmt.Fprintf(output, "EXTENT_%d_LENGTH=%d\n", i, extent.Length)
	}
	writeSupportResult(output, "VSS_VOLUME_GUID", volume.GUIDSupport)
	driveRootIndex := 0
	for _, path := range volume.MountPaths {
		if classifyMountPath(path) != mountDriveRoot {
			continue
		}
		prefix := "VSS_DRIVE_ROOT"
		if driveRootIndex > 0 {
			prefix = fmt.Sprintf("VSS_DRIVE_ROOT_%d", driveRootIndex)
		}
		fmt.Fprintf(output, "%s=%s\n", prefix, path)
		writeSupportResult(output, prefix, volume.DriveRootSupport[path])
		driveRootIndex++
	}
	fmt.Fprintln(output)
}

func writeSupportResult(output io.Writer, prefix string, result supportResult) {
	fmt.Fprintf(output, "%s_SUPPORTED=%t\n", prefix, result.Supported)
	fmt.Fprintf(output, "%s_ERROR=%s\n", prefix, result.Err)
}
