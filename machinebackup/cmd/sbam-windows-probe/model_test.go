package main

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestClassifyMountPath(t *testing.T) {
	tests := []struct {
		path string
		want mountKind
	}{
		{"", mountNoMount},
		{`C:\`, mountDriveRoot},
		{`C:\Mount\Data\`, mountDirectory},
		{`\\server\share\`, mountOther},
	}
	for _, tc := range tests {
		if got := classifyMountPath(tc.path); got != tc.want {
			t.Errorf("classifyMountPath(%q)=%s want=%s", tc.path, got, tc.want)
		}
	}
}

func TestParseMultiSZ(t *testing.T) {
	data := append(utf16.Encode([]rune(`C:\`)), 0)
	data = append(data, utf16.Encode([]rune(`C:\Mount\Data\`))...)
	data = append(data, 0, 0)
	paths, err := parseMultiSZ(data, uint32(len(data)))
	if err != nil || strings.Join(paths, "|") != `C:\|C:\Mount\Data\` {
		t.Fatalf("paths=%q error=%v", paths, err)
	}
	if _, err := parseMultiSZ(data[:len(data)-1], uint32(len(data)-1)); err == nil {
		t.Fatal("truncated MULTI_SZ accepted")
	}
}

func TestParseExtents(t *testing.T) {
	data := make([]byte, extentHeaderSize+extentSize)
	binary.LittleEndian.PutUint32(data[:4], 1)
	binary.LittleEndian.PutUint32(data[extentHeaderSize:], 3)
	binary.LittleEndian.PutUint64(data[extentHeaderSize+8:], 1048576)
	binary.LittleEndian.PutUint64(data[extentHeaderSize+16:], 575668224)
	extents, err := parseExtents(data, uint32(len(data)))
	if err != nil || len(extents) != 1 || extents[0].DiskNumber != 3 || extents[0].StartingOffset != 1048576 || extents[0].Length != 575668224 {
		t.Fatalf("extents=%+v error=%v", extents, err)
	}
	binary.LittleEndian.PutUint32(data[:4], 2)
	if _, err := parseExtents(data, uint32(len(data))); err == nil {
		t.Fatal("inconsistent extent count accepted")
	}
}

func TestEnsureTrailingBackslash(t *testing.T) {
	const guid = `\\?\Volume{12345678-1234-1234-1234-123456789abc}`
	if got := ensureTrailingBackslash(guid); got != guid+`\` {
		t.Fatalf("GUID=%q", got)
	}
	if got := ensureTrailingBackslash(guid + `\`); got != guid+`\` {
		t.Fatalf("already terminated GUID=%q", got)
	}
}

func TestWriteVolumeReportPreservesGUIDAndReportsNoMount(t *testing.T) {
	volume := probeVolume{
		GUID:        `\\?\Volume{12345678-1234-1234-1234-123456789abc}\`,
		Extents:     []probeExtent{{DiskNumber: 2, StartingOffset: 1048576, Length: 575668224}},
		GUIDSupport: supportResult{Supported: true},
	}
	var output bytes.Buffer
	writeVolumeReport(&output, volume)
	text := output.String()
	for _, expected := range []string{
		`GUID=\\?\Volume{12345678-1234-1234-1234-123456789abc}\`,
		"MOUNT_COUNT=0",
		"MOUNT_KIND=NO_MOUNT",
		"EXTENT_COUNT=1",
		"EXTENT_0_DISK=2",
		"VSS_VOLUME_GUID_SUPPORTED=true",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("report missing %q:\n%s", expected, text)
		}
	}
}

func TestWriteVolumeReportSeparatesGUIDAndDriveRootResults(t *testing.T) {
	volume := probeVolume{
		GUID:        `\\?\Volume{12345678-1234-1234-1234-123456789abc}\`,
		MountPaths:  []string{`C:\`, `C:\Mount\Data\`},
		GUIDSupport: supportResult{Supported: true},
		DriveRootSupport: map[string]supportResult{
			`C:\`: {Supported: false, Err: "test HRESULT"},
		},
	}
	var output bytes.Buffer
	writeVolumeReport(&output, volume)
	text := output.String()
	for _, expected := range []string{
		"MOUNT_0_KIND=DRIVE_ROOT",
		"MOUNT_1_KIND=DIRECTORY_MOUNT",
		`VSS_DRIVE_ROOT=C:\`,
		"VSS_DRIVE_ROOT_SUPPORTED=false",
		"VSS_DRIVE_ROOT_ERROR=test HRESULT",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("report missing %q:\n%s", expected, text)
		}
	}
}
