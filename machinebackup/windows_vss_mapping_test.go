package main

import (
	"strings"
	"testing"

	"snapshot"
)

func validWindowsSnapshot(id string, setID string, objectPath string) snapshot.SnapShot {
	return snapshot.SnapShot{
		Id:            id,
		SnapshotSetID: setID,
		ObjectPath:    objectPath,
		Valid:         true,
	}
}

func TestValidateWindowsSnapshotMappingAcceptsCoordinatedSet(t *testing.T) {
	systemGUID := `\\?\Volume{3a886445-0000-0000-0000-100000000000}\`
	sources := []string{systemGUID, `C:\`}
	snapshots := map[string]snapshot.SnapShot{
		systemGUID: validWindowsSnapshot("snapshot-system", "set-1", `\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy4\`),
		`C:\`:      validWindowsSnapshot("snapshot-c", "set-1", `\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy5\`),
	}
	if err := validateWindowsSnapshotMapping(sources, snapshots); err != nil {
		t.Fatal(err)
	}
}

func TestValidateWindowsSnapshotMappingAllowsNoVSSSources(t *testing.T) {
	if err := validateWindowsSnapshotMapping(nil, map[string]snapshot.SnapShot{}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateWindowsSnapshotMappingRejectsSetMismatch(t *testing.T) {
	sources := []string{`C:\`, `D:\`}
	snapshots := map[string]snapshot.SnapShot{
		`C:\`: validWindowsSnapshot("snapshot-c", "set-1", "device-c"),
		`D:\`: validWindowsSnapshot("snapshot-d", "set-2", "device-d"),
	}
	if err := validateWindowsSnapshotMapping(sources, snapshots); err == nil {
		t.Fatal("snapshots from different sets must fail closed")
	}
}

func TestValidateWindowsSnapshotMappingRejectsMissingAndUnexpected(t *testing.T) {
	snapshots := map[string]snapshot.SnapShot{
		`D:\`: validWindowsSnapshot("snapshot-d", "set-1", "device-d"),
	}
	err := validateWindowsSnapshotMapping([]string{`C:\`}, snapshots)
	if err == nil || !strings.Contains(err.Error(), `missing=["C:\\"]`) || !strings.Contains(err.Error(), `unexpected=["D:\\"]`) {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateWindowsSnapshotMappingRejectsInvalidProperties(t *testing.T) {
	valid := validWindowsSnapshot("snapshot-c", "set-1", "device-c")
	tests := []struct {
		name     string
		snapshot snapshot.SnapShot
	}{
		{"invalid flag", snapshot.SnapShot{Id: "snapshot-c", SnapshotSetID: "set-1", ObjectPath: "device-c"}},
		{"empty ID", validWindowsSnapshot("", "set-1", "device-c")},
		{"empty set ID", validWindowsSnapshot("snapshot-c", "", "device-c")},
		{"empty object path", validWindowsSnapshot("snapshot-c", "set-1", "")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateWindowsSnapshotMapping([]string{`C:\`}, map[string]snapshot.SnapShot{`C:\`: tc.snapshot}); err == nil {
				t.Fatal("invalid snapshot properties must fail closed")
			}
		})
	}
	if err := validateWindowsSnapshotMapping([]string{`C:\`, `D:\`}, map[string]snapshot.SnapShot{
		`C:\`: valid,
		`D:\`: validWindowsSnapshot("snapshot-c", "set-1", "device-d"),
	}); err == nil {
		t.Fatal("duplicate snapshot IDs must fail closed")
	}
}

func TestValidateWindowsSnapshotMappingRequiresNormalizedUniqueSources(t *testing.T) {
	tests := [][]string{
		{`c:/`},
		{`C:\`, `C:\`},
	}
	for _, sources := range tests {
		if err := validateWindowsSnapshotMapping(sources, map[string]snapshot.SnapShot{}); err == nil {
			t.Fatalf("sources %q must fail before backup I/O", sources)
		}
	}
}
