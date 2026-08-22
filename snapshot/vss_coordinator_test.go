package snapshot

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type fakeVSSSnapshotSetSession struct {
	events        []string
	snapshotSetID string
	snapshotIDs   map[string]string
	properties    map[string]vssSnapshotProperties
	addCalls      int
	failAddCall   int
	startErr      error
	addErr        error
	prepareErr    error
	doErr         error
	propertiesErr error
	completeErr   error
	abortErr      error
	deleteErr     error
	releaseErr    error
	deleteResult  vssSnapshotSetDeleteResult
	deletedSetIDs []string
	deleteCalls   int
	releaseCalls  int
	abortCalls    int
	completeCalls int
}

func newFakeVSSSnapshotSetSession(sources ...string) *fakeVSSSnapshotSetSession {
	session := &fakeVSSSnapshotSetSession{
		snapshotSetID: "set-1",
		snapshotIDs:   make(map[string]string, len(sources)),
		properties:    make(map[string]vssSnapshotProperties, len(sources)),
		deleteResult:  vssSnapshotSetDeleteResult{DeletedSnapshots: len(sources)},
	}
	for i, source := range sources {
		suffix := strconv.Itoa(i + 1)
		snapshotID := "snapshot-" + suffix
		session.snapshotIDs[source] = snapshotID
		session.properties[snapshotID] = vssSnapshotProperties{
			SnapshotID:       snapshotID,
			SnapshotSetID:    session.snapshotSetID,
			DeviceObjectPath: `\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy` + suffix + `\`,
		}
	}
	return session
}

func (s *fakeVSSSnapshotSetSession) StartSnapshotSet() (string, error) {
	s.events = append(s.events, "start")
	return s.snapshotSetID, s.startErr
}

func (s *fakeVSSSnapshotSetSession) AddToSnapshotSet(source string) (string, error) {
	s.addCalls++
	s.events = append(s.events, "add:"+source)
	if s.failAddCall == s.addCalls {
		return "", s.addErr
	}
	return s.snapshotIDs[source], nil
}

func (s *fakeVSSSnapshotSetSession) PrepareForBackup() error {
	s.events = append(s.events, "prepare")
	return s.prepareErr
}

func (s *fakeVSSSnapshotSetSession) DoSnapshotSet() error {
	s.events = append(s.events, "do")
	return s.doErr
}

func (s *fakeVSSSnapshotSetSession) GetSnapshotProperties(snapshotID string) (vssSnapshotProperties, error) {
	s.events = append(s.events, "properties:"+snapshotID)
	if s.propertiesErr != nil {
		return vssSnapshotProperties{}, s.propertiesErr
	}
	return s.properties[snapshotID], nil
}

func (s *fakeVSSSnapshotSetSession) BackupComplete() error {
	s.completeCalls++
	s.events = append(s.events, "complete")
	return s.completeErr
}

func (s *fakeVSSSnapshotSetSession) AbortBackup() error {
	s.abortCalls++
	s.events = append(s.events, "abort")
	return s.abortErr
}

func (s *fakeVSSSnapshotSetSession) DeleteSnapshotSet(snapshotSetID string) (vssSnapshotSetDeleteResult, error) {
	s.deleteCalls++
	s.deletedSetIDs = append(s.deletedSetIDs, snapshotSetID)
	s.events = append(s.events, "delete:"+snapshotSetID)
	return s.deleteResult, s.deleteErr
}

func (s *fakeVSSSnapshotSetSession) Release() {
	s.releaseCalls++
	s.events = append(s.events, "release")
}

func (s *fakeVSSSnapshotSetSession) ReleaseVSSSession() error {
	s.Release()
	return s.releaseErr
}

func TestCoordinateVSSSnapshotSetOrdersOneMultiVolumeSet(t *testing.T) {
	systemReserved := `\\?\Volume{3a886445-0000-0000-0000-100000000000}\`
	sources := []string{systemReserved, `C:\`}
	session := newFakeVSSSnapshotSetSession(sources...)
	callbackCalls := 0

	err := coordinateVSSSnapshotSet(sources, session, func(snapshots map[string]SnapShot) error {
		callbackCalls++
		session.events = append(session.events, "callback")
		for _, source := range sources {
			snapshotID := session.snapshotIDs[source]
			got, ok := snapshots[source]
			if !ok {
				t.Fatalf("missing snapshot mapping for %q", source)
			}
			if got.Id != snapshotID || got.ObjectPath != session.properties[snapshotID].DeviceObjectPath || !got.Valid {
				t.Fatalf("snapshot mapping for %q=%+v", source, got)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	wantEvents := []string{
		"start",
		"add:" + systemReserved,
		`add:C:\`,
		"prepare",
		"do",
		"properties:snapshot-1",
		"properties:snapshot-2",
		"callback",
		"complete",
		"release",
	}
	if !reflect.DeepEqual(session.events, wantEvents) {
		t.Fatalf("events=%q, want %q", session.events, wantEvents)
	}
	if callbackCalls != 1 || session.addCalls != 2 || session.completeCalls != 1 || session.abortCalls != 0 || session.releaseCalls != 1 {
		t.Fatalf("calls callback=%d add=%d complete=%d abort=%d release=%d", callbackCalls, session.addCalls, session.completeCalls, session.abortCalls, session.releaseCalls)
	}
}

func TestCoordinateVSSSnapshotSetSecondAddFailsClosed(t *testing.T) {
	sources := []string{`C:\`, `D:\`}
	wantErr := errors.New("second add failed")
	session := newFakeVSSSnapshotSetSession(sources...)
	session.failAddCall = 2
	session.addErr = wantErr
	callbackCalled := false

	err := coordinateVSSSnapshotSet(sources, session, func(map[string]SnapShot) error {
		callbackCalled = true
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	wantEvents := []string{"start", `add:C:\`, `add:D:\`, "release"}
	if !reflect.DeepEqual(session.events, wantEvents) || callbackCalled || session.abortCalls != 0 || session.releaseCalls != 1 {
		t.Fatalf("events=%q callback=%v abort=%d release=%d", session.events, callbackCalled, session.abortCalls, session.releaseCalls)
	}
}

func TestCoordinateVSSSnapshotSetPrepareAndDoFailuresFailClosed(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*fakeVSSSnapshotSetSession, error)
		wantEvents []string
		wantAbort  int
	}{
		{
			name: "prepare",
			configure: func(session *fakeVSSSnapshotSetSession, err error) {
				session.prepareErr = err
			},
			wantEvents: []string{"start", `add:C:\`, "prepare", "release"},
		},
		{
			name: "do snapshot set",
			configure: func(session *fakeVSSSnapshotSetSession, err error) {
				session.doErr = err
			},
			wantEvents: []string{"start", `add:C:\`, "prepare", "do", "abort", "release"},
			wantAbort:  1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantErr := errors.New(tc.name + " failed")
			session := newFakeVSSSnapshotSetSession(`C:\`)
			tc.configure(session, wantErr)
			callbackCalled := false
			err := coordinateVSSSnapshotSet([]string{`C:\`}, session, func(map[string]SnapShot) error {
				callbackCalled = true
				return nil
			})
			if !errors.Is(err, wantErr) {
				t.Fatalf("error=%v, want %v", err, wantErr)
			}
			if !reflect.DeepEqual(session.events, tc.wantEvents) || callbackCalled || session.abortCalls != tc.wantAbort || session.releaseCalls != 1 {
				t.Fatalf("events=%q callback=%v abort=%d release=%d", session.events, callbackCalled, session.abortCalls, session.releaseCalls)
			}
		})
	}
}

func TestCoordinateVSSSnapshotSetCallbackErrorCleansUp(t *testing.T) {
	wantErr := errors.New("callback failed")
	session := newFakeVSSSnapshotSetSession(`C:\`)
	err := coordinateVSSSnapshotSet([]string{`C:\`}, session, func(map[string]SnapShot) error {
		session.events = append(session.events, "callback")
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	wantEvents := []string{"start", `add:C:\`, "prepare", "do", "properties:snapshot-1", "callback", "abort", "release"}
	if !reflect.DeepEqual(session.events, wantEvents) || session.completeCalls != 0 || session.abortCalls != 1 || session.releaseCalls != 1 {
		t.Fatalf("events=%q complete=%d abort=%d release=%d", session.events, session.completeCalls, session.abortCalls, session.releaseCalls)
	}
}

func TestCoordinateVSSSnapshotSetDeduplicatesSourcesInFirstSeenOrder(t *testing.T) {
	sources := []string{`C:\`, `C:\`, `D:\`, `C:\`, `D:\`}
	session := newFakeVSSSnapshotSetSession(`C:\`, `D:\`)
	var mapped map[string]SnapShot
	err := coordinateVSSSnapshotSet(sources, session, func(snapshots map[string]SnapShot) error {
		mapped = snapshots
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(mapped) != 2 || mapped[`C:\`].Id != "snapshot-1" || mapped[`D:\`].Id != "snapshot-2" {
		t.Fatalf("snapshot mapping=%+v", mapped)
	}
	wantAdds := []string{`add:C:\`, `add:D:\`}
	gotAdds := make([]string, 0)
	for _, event := range session.events {
		if strings.HasPrefix(event, "add:") {
			gotAdds = append(gotAdds, event)
		}
	}
	if !reflect.DeepEqual(gotAdds, wantAdds) || session.addCalls != 2 {
		t.Fatalf("add events=%q calls=%d", gotAdds, session.addCalls)
	}
}

func TestCoordinateVSSSnapshotSetSingleVolumeDoesNotRegress(t *testing.T) {
	session := newFakeVSSSnapshotSetSession(`C:\`)
	callbackCalls := 0
	err := coordinateVSSSnapshotSet([]string{`C:\`}, session, func(snapshots map[string]SnapShot) error {
		callbackCalls++
		if len(snapshots) != 1 || snapshots[`C:\`].Id != "snapshot-1" {
			t.Fatalf("single-volume mapping=%+v", snapshots)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if callbackCalls != 1 || session.addCalls != 1 || session.completeCalls != 1 || session.abortCalls != 0 || session.releaseCalls != 1 {
		t.Fatalf("calls callback=%d add=%d complete=%d abort=%d release=%d", callbackCalls, session.addCalls, session.completeCalls, session.abortCalls, session.releaseCalls)
	}
}

func TestCoordinateVSSSnapshotSetPropertiesErrorFailsClosed(t *testing.T) {
	wantErr := errors.New("properties failed")
	session := newFakeVSSSnapshotSetSession(`C:\`)
	session.propertiesErr = wantErr
	callbackCalled := false

	err := coordinateVSSSnapshotSet([]string{`C:\`}, session, func(map[string]SnapShot) error {
		callbackCalled = true
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	wantEvents := []string{"start", `add:C:\`, "prepare", "do", "properties:snapshot-1", "abort", "release"}
	if !reflect.DeepEqual(session.events, wantEvents) || callbackCalled || session.abortCalls != 1 || session.releaseCalls != 1 {
		t.Fatalf("events=%q callback=%v abort=%d release=%d", session.events, callbackCalled, session.abortCalls, session.releaseCalls)
	}
}

func TestCoordinateVSSSnapshotSetRejectsMismatchedSetID(t *testing.T) {
	session := newFakeVSSSnapshotSetSession(`C:\`)
	properties := session.properties["snapshot-1"]
	properties.SnapshotSetID = "different-set"
	session.properties["snapshot-1"] = properties
	callbackCalled := false

	err := coordinateVSSSnapshotSet([]string{`C:\`}, session, func(map[string]SnapShot) error {
		callbackCalled = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "belongs to snapshot set") {
		t.Fatalf("unexpected error=%v", err)
	}
	wantEvents := []string{"start", `add:C:\`, "prepare", "do", "properties:snapshot-1", "abort", "release"}
	if !reflect.DeepEqual(session.events, wantEvents) || callbackCalled || session.abortCalls != 1 || session.releaseCalls != 1 {
		t.Fatalf("events=%q callback=%v abort=%d release=%d", session.events, callbackCalled, session.abortCalls, session.releaseCalls)
	}
}

func TestCoordinateVSSSnapshotSetBackupCompleteErrorFailsClosed(t *testing.T) {
	wantErr := errors.New("backup complete failed")
	session := newFakeVSSSnapshotSetSession(`C:\`)
	session.completeErr = wantErr

	err := coordinateVSSSnapshotSet([]string{`C:\`}, session, func(map[string]SnapShot) error {
		session.events = append(session.events, "callback")
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	wantEvents := []string{"start", `add:C:\`, "prepare", "do", "properties:snapshot-1", "callback", "complete", "abort", "release"}
	if !reflect.DeepEqual(session.events, wantEvents) || session.completeCalls != 1 || session.abortCalls != 1 || session.releaseCalls != 1 {
		t.Fatalf("events=%q complete=%d abort=%d release=%d", session.events, session.completeCalls, session.abortCalls, session.releaseCalls)
	}
}

func TestCoordinateVSSSnapshotSetCallbackPanicRemainsVisibleAndCleansUp(t *testing.T) {
	wantPanic := errors.New("callback panic")
	session := newFakeVSSSnapshotSetSession(`C:\`)
	var recovered any

	func() {
		defer func() {
			recovered = recover()
		}()
		_ = coordinateVSSSnapshotSet([]string{`C:\`}, session, func(map[string]SnapShot) error {
			session.events = append(session.events, "callback")
			panic(wantPanic)
		})
	}()

	if recovered != wantPanic {
		t.Fatalf("recovered panic=%v, want %v", recovered, wantPanic)
	}
	wantEvents := []string{"start", `add:C:\`, "prepare", "do", "properties:snapshot-1", "callback", "abort", "release"}
	if !reflect.DeepEqual(session.events, wantEvents) || session.completeCalls != 0 || session.abortCalls != 1 || session.releaseCalls != 1 {
		t.Fatalf("events=%q complete=%d abort=%d release=%d", session.events, session.completeCalls, session.abortCalls, session.releaseCalls)
	}
}

func TestCoordinateVSSSnapshotSetAbortErrorPreservesOriginalError(t *testing.T) {
	originalErr := errors.New("callback failed")
	abortErr := errors.New("abort failed")
	session := newFakeVSSSnapshotSetSession(`C:\`)
	session.abortErr = abortErr

	err := coordinateVSSSnapshotSet([]string{`C:\`}, session, func(map[string]SnapShot) error {
		session.events = append(session.events, "callback")
		return originalErr
	})
	if !errors.Is(err, originalErr) {
		t.Fatalf("error=%v does not preserve original error %v", err, originalErr)
	}
	if !errors.Is(err, abortErr) {
		t.Fatalf("error=%v does not include abort error %v", err, abortErr)
	}
	wantEvents := []string{"start", `add:C:\`, "prepare", "do", "properties:snapshot-1", "callback", "abort", "release"}
	if !reflect.DeepEqual(session.events, wantEvents) || session.abortCalls != 1 || session.releaseCalls != 1 {
		t.Fatalf("events=%q abort=%d release=%d", session.events, session.abortCalls, session.releaseCalls)
	}
}
