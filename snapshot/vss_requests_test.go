package snapshot

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func prepareWindowsTestRequests(t *testing.T, paths ...string) ([]preparedVSSSnapshotRequest, []string) {
	t.Helper()
	requests, sources, err := prepareVSSSnapshotRequests(paths, func(path string) (string, error) {
		return path, nil
	}, func(path string) string {
		if len(path) >= 2 && path[1] == ':' {
			return path[:2]
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return requests, sources
}

func deterministicSnapshotSymlink(symlinkPath string, id string, _ string) (string, error) {
	return filepath.Join(symlinkPath, id), nil
}

func TestVSSRequestsOnSameVolumeUseOneSnapshotAndRetainKeys(t *testing.T) {
	paths := []string{`C:\Data`, `C:\Users`}
	requests, sources := prepareWindowsTestRequests(t, paths...)
	if want := []string{`C:\`}; !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources=%q, want %q", sources, want)
	}

	session := newFakeVSSSnapshotSetSession(sources...)
	symlinkCalls := 0
	var got map[string]SnapShot
	err := createVSSSnapshotFromRequests(requests, sources, session, `C:\snapshot-links`, func(path string, id string, objectPath string) (string, error) {
		symlinkCalls++
		return deterministicSnapshotSymlink(path, id, objectPath)
	}, func(snapshots map[string]SnapShot) error {
		got = snapshots
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.addCalls != 1 || symlinkCalls != 1 {
		t.Fatalf("AddToSnapshotSet calls=%d, symlink calls=%d; want 1 each", session.addCalls, symlinkCalls)
	}
	for _, key := range paths {
		if _, exists := got[key]; !exists {
			t.Fatalf("callback lost requested key %q: %#v", key, got)
		}
	}
	root := filepath.Join(`C:\snapshot-links`, "snapshot-1")
	if got[paths[0]].FullPath != filepath.Join(root, "Data") || got[paths[1]].FullPath != filepath.Join(root, "Users") {
		t.Fatalf("FullPath mapping: Data=%q Users=%q", got[paths[0]].FullPath, got[paths[1]].FullPath)
	}
}

func TestVSSRequestsMapCanonicalVolumeGUIDAndNormalPath(t *testing.T) {
	guid := `\\?\Volume{3a886445-0000-0000-0000-100000000000}\`
	normalPath := `C:\Data`
	requests, sources := prepareWindowsTestRequests(t, guid, normalPath)
	if want := []string{guid, `C:\`}; !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources=%q, want %q", sources, want)
	}

	session := newFakeVSSSnapshotSetSession(sources...)
	var got map[string]SnapShot
	err := createVSSSnapshotFromRequests(requests, sources, session, `C:\snapshot-links`, deterministicSnapshotSymlink, func(snapshots map[string]SnapShot) error {
		got = snapshots
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[guid].FullPath != filepath.Join(`C:\snapshot-links`, "snapshot-1") {
		t.Fatalf("canonical Volume GUID FullPath=%q", got[guid].FullPath)
	}
	if got[normalPath].FullPath != filepath.Join(`C:\snapshot-links`, "snapshot-2", "Data") {
		t.Fatalf("normal path FullPath=%q", got[normalPath].FullPath)
	}
	if got[guid].Id != "snapshot-1" || got[normalPath].Id != "snapshot-2" || !got[guid].Valid || !got[normalPath].Valid {
		t.Fatalf("mapped snapshots=%#v", got)
	}
}

func TestVSSRequestsSingleVolumePreservesSnapshotFields(t *testing.T) {
	requestedPath := `D:\Backup\Source`
	requests, sources := prepareWindowsTestRequests(t, requestedPath)
	session := newFakeVSSSnapshotSetSession(sources...)
	var got SnapShot
	err := createVSSSnapshotFromRequests(requests, sources, session, `C:\snapshot-links`, deterministicSnapshotSymlink, func(snapshots map[string]SnapShot) error {
		got = snapshots[requestedPath]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSource := `D:\`
	wantID := session.snapshotIDs[wantSource]
	wantObjectPath := session.properties[wantID].DeviceObjectPath
	wantFullPath := filepath.Join(`C:\snapshot-links`, wantID, "Backup", "Source")
	if got.FullPath != wantFullPath || got.Id != wantID || got.ObjectPath != wantObjectPath || !got.Valid {
		t.Fatalf("snapshot=%+v, want FullPath=%q Id=%q ObjectPath=%q Valid=true", got, wantFullPath, wantID, wantObjectPath)
	}
	if session.addCalls != 1 || session.completeCalls != 1 || session.abortCalls != 0 || session.releaseCalls != 1 {
		t.Fatalf("calls add=%d complete=%d abort=%d release=%d", session.addCalls, session.completeCalls, session.abortCalls, session.releaseCalls)
	}
}

func TestVSSRequestMappingFailuresFailClosed(t *testing.T) {
	wantErr := errors.New("symlink failed")
	tests := []struct {
		name     string
		requests []preparedVSSSnapshotRequest
		symlink  vssSnapshotSymlink
	}{
		{
			name: "symlink",
			requests: []preparedVSSSnapshotRequest{{
				key: `C:\Data`, source: `C:\`, subPath: "Data",
			}},
			symlink: func(string, string, string) (string, error) {
				return "", wantErr
			},
		},
		{
			name: "missing source mapping",
			requests: []preparedVSSSnapshotRequest{{
				key: `D:\Data`, source: `D:\`, subPath: "Data",
			}},
			symlink: deterministicSnapshotSymlink,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sources := []string{`C:\`}
			session := newFakeVSSSnapshotSetSession(sources...)
			callbackCalled := false
			err := createVSSSnapshotFromRequests(tc.requests, sources, session, `C:\snapshot-links`, tc.symlink, func(map[string]SnapShot) error {
				callbackCalled = true
				return nil
			})
			if err == nil {
				t.Fatal("expected mapping error")
			}
			if tc.name == "symlink" && !errors.Is(err, wantErr) {
				t.Fatalf("error=%v, want %v", err, wantErr)
			}
			if callbackCalled || session.completeCalls != 0 || session.abortCalls != 1 || session.releaseCalls != 1 {
				t.Fatalf("callback=%v complete=%d abort=%d release=%d", callbackCalled, session.completeCalls, session.abortCalls, session.releaseCalls)
			}
		})
	}
}

func TestCreateVSSSnapshotFromRequestsNilCallbackUsesDeterministicCleanup(t *testing.T) {
	sources := []string{`C:\`}
	wantReleaseErr := errors.New("release failed")
	session := newFakeVSSSnapshotSetSession(sources...)
	session.releaseErr = wantReleaseErr

	err := createVSSSnapshotFromRequests(nil, sources, session, `C:\snapshot-links`, deterministicSnapshotSymlink, nil)
	if err == nil || !errors.Is(err, wantReleaseErr) {
		t.Fatalf("error=%v, want nil callback and release errors", err)
	}
	if session.deleteCalls != 0 || session.abortCalls != 0 || session.releaseCalls != 1 {
		t.Fatalf("delete=%d abort=%d release=%d, want only deterministic release", session.deleteCalls, session.abortCalls, session.releaseCalls)
	}
	if wantEvents := []string{"release"}; !reflect.DeepEqual(session.events, wantEvents) {
		t.Fatalf("events=%q, want %q", session.events, wantEvents)
	}
}
