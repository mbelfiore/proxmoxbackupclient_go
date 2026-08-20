package main

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
	"unsafe"
)

func multiSZ(values ...string) []uint16 {
	out := make([]uint16, 0)
	for _, value := range values {
		out = append(out, utf16.Encode([]rune(value))...)
		out = append(out, 0)
	}
	return append(out, 0)
}

func TestPartitionInformationEXLayoutAMD64(t *testing.T) {
	var entry partitionInformationEX
	if got := unsafe.Sizeof(entry); got != 144 {
		t.Fatalf("PARTITION_INFORMATION_EX size=%d want=144", got)
	}
	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"style", unsafe.Offsetof(entry.PartitionStyle), 0},
		{"starting offset", unsafe.Offsetof(entry.StartingOffset), 8},
		{"partition length", unsafe.Offsetof(entry.PartitionLength), 16},
		{"partition number", unsafe.Offsetof(entry.PartitionNumber), 24},
		{"rewrite", unsafe.Offsetof(entry.RewritePartition), 28},
		{"service", unsafe.Offsetof(entry.IsServicePartition), 29},
		{"union", unsafe.Offsetof(entry.PartitionInfo), 32},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s offset=%d want=%d", check.name, check.got, check.want)
		}
	}
}

func TestDynamicPartitionClassification(t *testing.T) {
	basicGPT := [16]byte{0xa2, 0xa0, 0xd0, 0xeb, 0xe5, 0xb9, 0x33, 0x44, 0x87, 0xc0, 0x68, 0xb6, 0xb7, 0x26, 0x99, 0xc7}
	tests := []struct {
		name     string
		identity WindowsPartitionIdentity
		wantErr  bool
	}{
		{"basic MBR", WindowsPartitionIdentity{Style: DiskLayoutMBR, MBRType: 0x07}, false},
		{"basic GPT", WindowsPartitionIdentity{Style: DiskLayoutGPT, GPTType: basicGPT}, false},
		{"MBR LDM", WindowsPartitionIdentity{Style: DiskLayoutMBR, MBRType: 0x42}, true},
		{"MBR Storage Spaces data", WindowsPartitionIdentity{Style: DiskLayoutMBR, MBRType: 0xd7}, true},
		{"MBR Storage Spaces", WindowsPartitionIdentity{Style: DiskLayoutMBR, MBRType: 0xe7}, true},
		{"GPT LDM data", WindowsPartitionIdentity{Style: DiskLayoutGPT, GPTType: partitionLDMDataGUID}, true},
		{"GPT LDM metadata", WindowsPartitionIdentity{Style: DiskLayoutGPT, GPTType: partitionLDMMetadataGUID}, true},
		{"GPT Storage Spaces", WindowsPartitionIdentity{Style: DiskLayoutGPT, GPTType: partitionSpacesGUID}, true},
		{"GPT Storage Spaces data", WindowsPartitionIdentity{Style: DiskLayoutGPT, GPTType: partitionSpacesDataGUID}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWindowsPartitionIdentities([]WindowsPartitionIdentity{tc.identity})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantError=%v", err, tc.wantErr)
			}
		})
	}
}

func TestPartitionIdentityReadsNativeUnion(t *testing.T) {
	mbr := partitionInformationEX{PartitionStyle: 0}
	mbr.PartitionInfo[0] = partitionLDMTypeMBR
	identity, err := partitionIdentity(mbr)
	if err != nil || identity.Style != DiskLayoutMBR || identity.MBRType != partitionLDMTypeMBR {
		t.Fatalf("MBR identity=%+v error=%v", identity, err)
	}
	gpt := partitionInformationEX{PartitionStyle: 1}
	copy(gpt.PartitionInfo[:16], partitionLDMDataGUID[:])
	identity, err = partitionIdentity(gpt)
	if err != nil || identity.Style != DiskLayoutGPT || identity.GPTType != partitionLDMDataGUID {
		t.Fatalf("GPT identity=%+v error=%v", identity, err)
	}
}

func TestSingleExtentLDMFailsBeforeDiskPlan(t *testing.T) {
	partitions := []DiskExtent{{Start: 100, End: 200}}
	volume := WindowsVolume{VolumeGUID: "ldm", MountPaths: []string{`C:\`}, Extents: []WindowsDiskExtent{{DiskNumber: 0, StartingOffset: 100, Length: 100}}}
	if _, err := buildValidatedWindowsDiskPlan(300, DiskLayoutMBR, 0, partitions, []WindowsPartitionIdentity{{Style: DiskLayoutMBR, MBRType: 0x42}}, []WindowsVolume{volume}); err == nil {
		t.Fatal("single-extent LDM partition must fail before plan construction")
	}
	if _, err := buildValidatedWindowsDiskPlan(300, DiskLayoutMBR, 0, partitions, []WindowsPartitionIdentity{{Style: DiskLayoutMBR, MBRType: 0x07}}, []WindowsVolume{volume}); err != nil {
		t.Fatalf("ordinary single-extent basic volume rejected: %v", err)
	}
}

func TestParseUTF16MultiSZ(t *testing.T) {
	tests := []struct {
		name string
		data []uint16
		used uint32
		want []string
		err  bool
	}{
		{"zero paths", []uint16{0}, 1, []string{}, false},
		{"zero paths double NUL", []uint16{0, 0}, 2, []string{}, false},
		{"drive root", multiSZ(`C:\`), uint32(len(multiSZ(`C:\`))), []string{`C:\`}, false},
		{"two paths", multiSZ(`C:\`, `C:\Mount\Data\`), uint32(len(multiSZ(`C:\`, `C:\Mount\Data\`))), []string{`C:\`, `C:\Mount\Data\`}, false},
		{"directory mount", multiSZ(`C:\Mount\Data\`), uint32(len(multiSZ(`C:\Mount\Data\`))), []string{`C:\Mount\Data\`}, false},
		{"used beyond buffer", []uint16{0}, 2, nil, true},
		{"missing terminator", utf16.Encode([]rune(`C:\`)), 3, nil, true},
		{"truncated double terminator", append(utf16.Encode([]rune(`C:\`)), 0), 4, nil, true},
		{"data after terminator", []uint16{0, 'X'}, 2, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseUTF16MultiSZ(tc.data, tc.used)
			if (err != nil) != tc.err {
				t.Fatalf("error=%v wantError=%v", err, tc.err)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("paths=%q want=%q", got, tc.want)
			}
		})
	}
}

func encodeExtents(extents ...WindowsDiskExtent) []byte {
	data := make([]byte, volumeDiskExtentsHeaderSize+len(extents)*volumeDiskExtentSize)
	binary.LittleEndian.PutUint32(data, uint32(len(extents)))
	for i, extent := range extents {
		offset := volumeDiskExtentsHeaderSize + i*volumeDiskExtentSize
		binary.LittleEndian.PutUint32(data[offset:], extent.DiskNumber)
		binary.LittleEndian.PutUint64(data[offset+8:], extent.StartingOffset)
		binary.LittleEndian.PutUint64(data[offset+16:], extent.Length)
	}
	return data
}

func TestParseVolumeDiskExtents(t *testing.T) {
	single := encodeExtents(WindowsDiskExtent{DiskNumber: 2, StartingOffset: 4096, Length: 8192})
	multi := encodeExtents(WindowsDiskExtent{0, 1, 2}, WindowsDiskExtent{1, 3, 4})
	hugeCount := make([]byte, volumeDiskExtentsHeaderSize)
	binary.LittleEndian.PutUint32(hugeCount, ^uint32(0))
	tests := []struct {
		name string
		data []byte
		used uint32
		want int
		err  bool
	}{
		{"single", single, uint32(len(single)), 1, false},
		{"multi", multi, uint32(len(multi)), 2, false},
		{"short header", []byte{1, 0, 0}, 3, 0, true},
		{"count exceeds returned bytes", single, uint32(len(single) - 1), 0, true},
		{"huge incoherent count", hugeCount, uint32(len(hugeCount)), 0, true},
		{"returned exceeds buffer", single, uint32(len(single) + 1), 0, true},
		{"negative encoded offset", encodeExtents(WindowsDiskExtent{0, ^uint64(0), 1}), uint32(len(single)), 0, true},
		{"zero length", encodeExtents(WindowsDiskExtent{0, 1, 0}), uint32(len(single)), 0, true},
		{"extent overflow", encodeExtents(WindowsDiskExtent{0, ^uint64(0) - 1, 4}), uint32(len(single)), 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVolumeDiskExtents(tc.data, tc.used)
			if (err != nil) != tc.err || len(got) != tc.want {
				t.Fatalf("extents=%v error=%v", got, err)
			}
		})
	}
}

func TestBuildWindowsDiskPlan(t *testing.T) {
	partitions := []DiskExtent{{Start: 100, End: 200}, {Start: 300, End: 500}}
	volume := WindowsVolume{VolumeGUID: `\\?\Volume{one}\`, MountPaths: []string{`C:\Mount\Data\`, `C:\`}, Extents: []WindowsDiskExtent{{DiskNumber: 0, StartingOffset: 100, Length: 100}}}
	t.Run("single volume and raw partition", func(t *testing.T) {
		plan, err := buildWindowsDiskPlan(600, DiskLayoutGPT, 0, []DiskExtent{{Start: 300, End: 500}, {Start: 100, End: 200}}, []WindowsVolume{volume})
		if err != nil {
			t.Fatal(err)
		}
		if len(plan) != 5 || plan[1].VSSSource != `C:\` || plan[1].Raw || !plan[3].Raw {
			t.Fatalf("unexpected plan: %+v", plan)
		}
		if len(plan[1].Volume.MountPaths) != 2 {
			t.Fatal("all mount paths were not preserved")
		}
	})
	tests := []struct {
		name    string
		parts   []DiskExtent
		volumes []WindowsVolume
		err     bool
	}{
		{"volume other disk ignored", partitions, []WindowsVolume{{VolumeGUID: "other", Extents: []WindowsDiskExtent{{1, 100, 100}}}}, false},
		{"duplicate mapping", partitions, []WindowsVolume{volume, {VolumeGUID: "two", MountPaths: []string{`D:\`}, Extents: []WindowsDiskExtent{{0, 100, 100}}}}, true},
		{"multi extent target", partitions, []WindowsVolume{{VolumeGUID: "multi", MountPaths: []string{`C:\`}, Extents: []WindowsDiskExtent{{0, 100, 50}, {1, 0, 50}}}}, true},
		{"unknown target extent", partitions, []WindowsVolume{{VolumeGUID: "unknown", MountPaths: []string{`C:\`}, Extents: []WindowsDiskExtent{{0, 250, 10}}}}, true},
		{"extent beyond partition", partitions, []WindowsVolume{{VolumeGUID: "large", MountPaths: []string{`C:\`}, Extents: []WindowsDiskExtent{{0, 100, 101}}}}, true},
		{"directory only fails closed", partitions, []WindowsVolume{{VolumeGUID: "dir", MountPaths: []string{`C:\Mount\Data\`}, Extents: []WindowsDiskExtent{{0, 100, 100}}}}, true},
		{"no mount path fails closed", partitions, []WindowsVolume{{VolumeGUID: "none", Extents: []WindowsDiskExtent{{0, 100, 100}}}}, true},
		{"overlap", []DiskExtent{{100, 250, false}, {200, 300, false}}, nil, true},
		{"past disk", []DiskExtent{{500, 601, false}}, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildWindowsDiskPlan(600, DiskLayoutGPT, 0, tc.parts, tc.volumes)
			if (err != nil) != tc.err {
				t.Fatalf("error=%v wantError=%v", err, tc.err)
			}
		})
	}
}

func TestClassifyWindowsMountPath(t *testing.T) {
	if classifyWindowsMountPath(`C:\`) != WindowsDriveRootMount {
		t.Fatal("drive root not recognized")
	}
	if classifyWindowsMountPath(`C:\Mount\Data\`) != WindowsDirectoryMount {
		t.Fatal("directory mount not recognized")
	}
	if classifyWindowsMountPath("") != WindowsNoMount {
		t.Fatal("empty path not recognized")
	}
}

func TestSupportedVSSSourceVolumeGUIDFallback(t *testing.T) {
	guid := `\\?\Volume{3a886445-0000-0000-0000-100000000000}\`
	tests := []struct {
		name   string
		volume WindowsVolume
		want   string
		err    bool
	}{
		{"no mount canonical GUID", WindowsVolume{VolumeGUID: guid}, guid, false},
		{"drive root remains preferred", WindowsVolume{VolumeGUID: guid, MountPaths: []string{`D:\`}}, `D:\`, false},
		{"directory mount does not use GUID", WindowsVolume{VolumeGUID: guid, MountPaths: []string{`D:\Mount\System\`}}, "", true},
		{"no mount empty GUID", WindowsVolume{}, "", true},
		{"no mount missing trailing slash", WindowsVolume{VolumeGUID: `\\?\Volume{3a886445-0000-0000-0000-100000000000}`}, "", true},
		{"no mount truncated GUID", WindowsVolume{VolumeGUID: `\\?\Volume{3a886445-0000-0000-0000-10000000000}\`}, "", true},
		{"no mount non-hex GUID", WindowsVolume{VolumeGUID: `\\?\Volume{3a886445-0000-0000-0000-10000000000g}\`}, "", true},
		{"no mount text after root", WindowsVolume{VolumeGUID: guid + `child`}, "", true},
		{"empty braces", WindowsVolume{VolumeGUID: `\\?\Volume{}\`}, "", true},
		{"drive root is not GUID", WindowsVolume{VolumeGUID: `C:\`}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := supportedVSSSource(tc.volume)
			if (err != nil) != tc.err || got != tc.want {
				t.Fatalf("source=%q error=%v, want %q error=%v", got, err, tc.want, tc.err)
			}
		})
	}
}

func TestBuildWindowsDiskPlanNoMountVolumeGUID(t *testing.T) {
	guid := `\\?\Volume{3a886445-0000-0000-0000-100000000000}\`
	volume := WindowsVolume{VolumeGUID: guid, Extents: []WindowsDiskExtent{{DiskNumber: 0, StartingOffset: 100, Length: 100}}}
	plan, err := buildWindowsDiskPlan(300, DiskLayoutGPT, 0, []DiskExtent{{Start: 100, End: 200}}, []WindowsVolume{volume})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 3 || plan[1].Raw || plan[1].VSSSource != guid {
		t.Fatalf("unexpected no-mount plan: %+v", plan)
	}
}

func TestVSSSourcesForPlanFailsClosedForMultipleVolumes(t *testing.T) {
	plan := []WindowsDiskPlanSegment{
		{Start: 0, End: 10, VSSSource: `C:\`},
		{Start: 10, End: 20, VSSSource: `D:\`},
	}
	if _, err := vssSourcesForPlan(plan); err == nil {
		t.Fatal("multiple VSS volumes must fail before snapshot side effects")
	}
}
