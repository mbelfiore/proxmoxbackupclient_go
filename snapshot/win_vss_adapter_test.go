//go:build windows
// +build windows

package snapshot

import (
	"errors"
	"syscall"
	"testing"
)

type fakeWindowsSystemProcedure struct {
	call func(...uintptr) (uintptr, uintptr, error)
}

func (p fakeWindowsSystemProcedure) Call(arguments ...uintptr) (uintptr, uintptr, error) {
	return p.call(arguments...)
}

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

func TestWindowsVSSCOMSecurityInitializerRunsOnce(t *testing.T) {
	wantErr := errors.New("security initialization failed")
	calls := 0
	initializer := &windowsVSSCOMSecurityInitializer{initialize: func() error {
		calls++
		return wantErr
	}}

	for attempt := 0; attempt < 2; attempt++ {
		if err := initializer.Initialize(); !errors.Is(err, wantErr) {
			t.Fatalf("attempt %d error=%v, want %v", attempt+1, err, wantErr)
		}
	}
	if calls != 1 {
		t.Fatalf("security initialization calls=%d, want exactly 1", calls)
	}
}

func TestReleaseGoVSSQueriedInterfaceBalancesBothReferences(t *testing.T) {
	releaseCalls := 0
	releaseGoVSSQueriedInterface(func() int32 {
		releaseCalls++
		return int32(2 - releaseCalls)
	})
	if releaseCalls != 2 {
		t.Fatalf("release calls=%d, want exactly 2", releaseCalls)
	}
}

func TestReleaseGoVSSQueriedInterfaceStopsWhenFirstReleaseReachesZero(t *testing.T) {
	releaseCalls := 0
	releaseGoVSSQueriedInterface(func() int32 {
		releaseCalls++
		return 0
	})
	if releaseCalls != 1 {
		t.Fatalf("release calls=%d, want exactly 1", releaseCalls)
	}
}

func TestWindowsVSSResolversRequestSystemDLLs(t *testing.T) {
	t.Run("COM security", func(t *testing.T) {
		calls := 0
		err := initializeWindowsVSSCOMSecurityWithResolver(func(dllName string, procedureName string) (windowsSystemProcedure, error) {
			calls++
			if dllName != windowsOle32DLL || procedureName != "CoInitializeSecurity" {
				t.Fatalf("resolved %s!%s", dllName, procedureName)
			}
			return fakeWindowsSystemProcedure{call: func(...uintptr) (uintptr, uintptr, error) {
				return 0, 0, nil
			}}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("resolver calls=%d, want 1", calls)
		}
	})

	t.Run("snapshot properties cleanup", func(t *testing.T) {
		resolverCalls := 0
		procedureCalls := 0
		cleanup, err := resolveWindowsVSSSnapshotPropertiesCleanupWithResolver(func(dllName string, procedureName string) (windowsSystemProcedure, error) {
			resolverCalls++
			if dllName != windowsVSSAPIDLL || procedureName != windowsVSSFreeSnapshotPropertiesProcedure {
				t.Fatalf("resolved %s!%s", dllName, procedureName)
			}
			return fakeWindowsSystemProcedure{call: func(...uintptr) (uintptr, uintptr, error) {
				procedureCalls++
				return 0, 0, nil
			}}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		cleanup(nil)
		if resolverCalls != 1 || procedureCalls != 1 {
			t.Fatalf("resolver calls=%d procedure calls=%d, want 1 each", resolverCalls, procedureCalls)
		}
	})
}

func TestResolveWindowsSystemProcedure(t *testing.T) {
	procedure, err := resolveWindowsSystemProcedure("Kernel32.dll", "GetCurrentProcessId")
	if err != nil {
		t.Fatal(err)
	}
	processID, _, callErr := procedure.Call()
	if callErr != syscall.Errno(0) || processID == 0 {
		t.Fatalf("GetCurrentProcessId result=%d error=%v", processID, callErr)
	}
}

func TestWindowsDeleteSnapshotsVTableIndex(t *testing.T) {
	if windowsIVssBackupComponentsDeleteSnapshotsComputedIndex != windowsIVssBackupComponentsDeleteSnapshotsVTableIndex {
		t.Fatalf("DeleteSnapshots vtable index=%d, want %d", windowsIVssBackupComponentsDeleteSnapshotsComputedIndex, windowsIVssBackupComponentsDeleteSnapshotsVTableIndex)
	}
}

func TestWindowsVSSDeleteSnapshotsErrorRecognizesObjectNotFound(t *testing.T) {
	err := windowsVSSDeleteSnapshotsError(uintptr(windowsVSSEObjectNotFound))
	if !errors.Is(err, errVSSSnapshotSetNotFound) {
		t.Fatalf("error=%v, want VSS_E_OBJECT_NOT_FOUND sentinel", err)
	}
}
