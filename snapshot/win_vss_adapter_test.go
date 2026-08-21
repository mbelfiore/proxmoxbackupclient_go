//go:build windows
// +build windows

package snapshot

import (
	"errors"
	"testing"
)

type fakeWindowsVSSSnapshotProperties struct {
	snapshotID       string
	snapshotSetID    string
	deviceObjectPath string
}

func (p *fakeWindowsVSSSnapshotProperties) GetSnapshotId() string {
	return p.snapshotID
}

func (p *fakeWindowsVSSSnapshotProperties) GetSnapshotSetId() string {
	return p.snapshotSetID
}

func (p *fakeWindowsVSSSnapshotProperties) GetSnapshotDeviceObject() string {
	return p.deviceObjectPath
}

func TestCopyAndReleaseWindowsVSSSnapshotProperties(t *testing.T) {
	properties := &fakeWindowsVSSSnapshotProperties{
		snapshotID:       "snapshot-1",
		snapshotSetID:    "set-1",
		deviceObjectPath: `\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy1`,
	}
	cleanupCalls := 0

	got, err := copyAndReleaseWindowsVSSSnapshotProperties(properties, func() {
		cleanupCalls++
	})
	if err != nil {
		t.Fatal(err)
	}
	want := vssSnapshotProperties{
		SnapshotID:       "snapshot-1",
		SnapshotSetID:    "set-1",
		DeviceObjectPath: `\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy1\`,
	}
	if got != want {
		t.Fatalf("properties=%+v, want %+v", got, want)
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls=%d, want exactly 1", cleanupCalls)
	}
}

func TestRequireWindowsVSSSnapshotPropertiesCleanupFailsClosed(t *testing.T) {
	wantErr := errors.New("procedure unavailable")
	tests := []struct {
		name    string
		resolve windowsVSSSnapshotPropertiesCleanupResolver
		wantErr error
	}{
		{
			name: "resolution error",
			resolve: func() (windowsVSSSnapshotPropertiesCleanup, error) {
				return nil, wantErr
			},
			wantErr: wantErr,
		},
		{
			name: "nil cleanup",
			resolve: func() (windowsVSSSnapshotPropertiesCleanup, error) {
				return nil, nil
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cleanup, err := requireWindowsVSSSnapshotPropertiesCleanup(tc.resolve)
			if err == nil {
				t.Fatal("expected cleanup resolution to fail closed")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error=%v, want %v", err, tc.wantErr)
			}
			if cleanup != nil {
				t.Fatal("cleanup must be nil after resolution failure")
			}
		})
	}
}
