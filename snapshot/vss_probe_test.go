package snapshot

import (
	"errors"
	"reflect"
	"testing"
)

func validProbeSnapshots(sources []string) map[string]SnapShot {
	return map[string]SnapShot{
		sources[0]: {
			Id:            "snapshot-1",
			SnapshotSetID: "set-1",
			ObjectPath:    `\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy1\`,
			Valid:         true,
		},
		sources[1]: {
			Id:            "snapshot-2",
			SnapshotSetID: "set-1",
			ObjectPath:    `\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy2\`,
			Valid:         true,
		},
	}
}

func TestCoordinateVSSSnapshotSetProbeValidatesMappingAndCleanupOrder(t *testing.T) {
	sources := []string{`\\?\Volume{3a886445-0000-0000-0000-100000000000}\`, `C:\`}
	session := newFakeVSSSnapshotSetSession(sources...)
	var got []VSSSnapshotSetProbeResult

	err := coordinateVSSSnapshotSetProbe(sources, session, func(results []VSSSnapshotSetProbeResult) error {
		session.events = append(session.events, "probe-callback")
		got = results
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantResults := []VSSSnapshotSetProbeResult{
		{
			Source:           sources[0],
			SnapshotID:       "snapshot-1",
			SnapshotSetID:    "set-1",
			DeviceObjectPath: session.properties["snapshot-1"].DeviceObjectPath,
		},
		{
			Source:           sources[1],
			SnapshotID:       "snapshot-2",
			SnapshotSetID:    "set-1",
			DeviceObjectPath: session.properties["snapshot-2"].DeviceObjectPath,
		},
	}
	if !reflect.DeepEqual(got, wantResults) {
		t.Fatalf("results=%+v, want %+v", got, wantResults)
	}
	wantEvents := []string{
		"start",
		"add:" + sources[0],
		"add:" + sources[1],
		"prepare",
		"do",
		"properties:snapshot-1",
		"properties:snapshot-2",
		"probe-callback",
		"complete",
		"delete:set-1",
		"release",
	}
	if !reflect.DeepEqual(session.events, wantEvents) {
		t.Fatalf("events=%q, want %q", session.events, wantEvents)
	}
}

func TestValidateVSSSnapshotSetProbeFailsClosed(t *testing.T) {
	sources := []string{`\\?\Volume{3a886445-0000-0000-0000-100000000000}\`, `C:\`}
	tests := []struct {
		name      string
		sources   []string
		configure func(map[string]SnapShot)
	}{
		{
			name:    "duplicate source",
			sources: []string{`C:\`, `C:\`},
		},
		{
			name: "incomplete mapping",
			configure: func(snapshots map[string]SnapShot) {
				delete(snapshots, sources[1])
			},
		},
		{
			name: "duplicate snapshot ID",
			configure: func(snapshots map[string]SnapShot) {
				snapshot := snapshots[sources[1]]
				snapshot.Id = snapshots[sources[0]].Id
				snapshots[sources[1]] = snapshot
			},
		},
		{
			name: "different snapshot set ID",
			configure: func(snapshots map[string]SnapShot) {
				snapshot := snapshots[sources[1]]
				snapshot.SnapshotSetID = "set-2"
				snapshots[sources[1]] = snapshot
			},
		},
		{
			name: "missing snapshot ID",
			configure: func(snapshots map[string]SnapShot) {
				snapshot := snapshots[sources[0]]
				snapshot.Id = ""
				snapshots[sources[0]] = snapshot
			},
		},
		{
			name: "missing snapshot set ID",
			configure: func(snapshots map[string]SnapShot) {
				snapshot := snapshots[sources[0]]
				snapshot.SnapshotSetID = ""
				snapshots[sources[0]] = snapshot
			},
		},
		{
			name: "missing device object path",
			configure: func(snapshots map[string]SnapShot) {
				snapshot := snapshots[sources[0]]
				snapshot.ObjectPath = ""
				snapshots[sources[0]] = snapshot
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testSources := sources
			if tc.sources != nil {
				testSources = tc.sources
			}
			snapshots := validProbeSnapshots(sources)
			if tc.configure != nil {
				tc.configure(snapshots)
			}
			if _, err := validateVSSSnapshotSetProbe(testSources, snapshots); err == nil {
				t.Fatal("expected fail-closed validation error")
			}
		})
	}
}

func TestCoordinateVSSSnapshotSetProbeAbortsBeforeReleaseOnValidationFailure(t *testing.T) {
	sources := []string{`C:\`, `D:\`}
	session := newFakeVSSSnapshotSetSession(sources...)
	session.snapshotIDs[sources[1]] = "snapshot-1"

	err := coordinateVSSSnapshotSetProbe(sources, session, func([]VSSSnapshotSetProbeResult) error {
		t.Fatal("probe callback must not run after validation failure")
		return nil
	})
	if err == nil {
		t.Fatal("expected duplicate snapshot validation error")
	}
	wantTail := []string{"abort", "delete:set-1", "release"}
	if got := session.events[len(session.events)-len(wantTail):]; !reflect.DeepEqual(got, wantTail) {
		t.Fatalf("cleanup events=%q, want %q", got, wantTail)
	}
	if session.completeCalls != 0 || session.abortCalls != 1 || session.releaseCalls != 1 {
		t.Fatalf("complete=%d abort=%d release=%d", session.completeCalls, session.abortCalls, session.releaseCalls)
	}
}

func TestCoordinateVSSSnapshotSetProbePropagatesCleanupErrors(t *testing.T) {
	sources := []string{`C:\`, `D:\`}
	t.Run("abort after add failure", func(t *testing.T) {
		operationErr := errors.New("add failed")
		cleanupErr := errors.New("abort failed")
		session := newFakeVSSSnapshotSetSession(sources...)
		session.failAddCall = 2
		session.addErr = operationErr
		session.abortErr = cleanupErr
		session.deleteResult = vssSnapshotSetDeleteResult{}
		session.deleteErr = errVSSSnapshotSetNotFound

		err := coordinateVSSSnapshotSetProbe(sources, session, func([]VSSSnapshotSetProbeResult) error { return nil })
		if !errors.Is(err, operationErr) || !errors.Is(err, cleanupErr) {
			t.Fatalf("error=%v, want operation and cleanup errors", err)
		}
		wantEvents := []string{"start", `add:C:\`, `add:D:\`, "abort", "delete:set-1", "release"}
		if !reflect.DeepEqual(session.events, wantEvents) {
			t.Fatalf("events=%q, want %q", session.events, wantEvents)
		}
	})

	t.Run("backup complete then abort", func(t *testing.T) {
		completeErr := errors.New("complete failed")
		abortErr := errors.New("abort failed")
		session := newFakeVSSSnapshotSetSession(sources...)
		session.completeErr = completeErr
		session.abortErr = abortErr

		err := coordinateVSSSnapshotSetProbe(sources, session, func([]VSSSnapshotSetProbeResult) error { return nil })
		if !errors.Is(err, completeErr) || !errors.Is(err, abortErr) {
			t.Fatalf("error=%v, want complete and abort errors", err)
		}
		wantTail := []string{"complete", "abort", "delete:set-1", "release"}
		if got := session.events[len(session.events)-len(wantTail):]; !reflect.DeepEqual(got, wantTail) {
			t.Fatalf("cleanup events=%q, want %q", got, wantTail)
		}
	})
}

func TestCoordinateVSSSnapshotSetProbeValidatesDeleteResultAndReleaseErrors(t *testing.T) {
	sources := []string{`C:\`, `D:\`}
	tests := []struct {
		name         string
		deleteResult vssSnapshotSetDeleteResult
		deleteErr    error
		releaseErr   error
	}{
		{
			name:         "delete error",
			deleteResult: vssSnapshotSetDeleteResult{DeletedSnapshots: 2},
			deleteErr:    errors.New("delete failed"),
		},
		{
			name:         "wrong deleted count",
			deleteResult: vssSnapshotSetDeleteResult{DeletedSnapshots: 1},
		},
		{
			name: "nondeleted snapshot ID",
			deleteResult: vssSnapshotSetDeleteResult{
				DeletedSnapshots:     2,
				NondeletedSnapshotID: "snapshot-2",
			},
		},
		{
			name:         "release error",
			deleteResult: vssSnapshotSetDeleteResult{DeletedSnapshots: 2},
			releaseErr:   errors.New("release failed"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			session := newFakeVSSSnapshotSetSession(sources...)
			session.deleteResult = tc.deleteResult
			session.deleteErr = tc.deleteErr
			session.releaseErr = tc.releaseErr

			err := coordinateVSSSnapshotSetProbe(sources, session, func([]VSSSnapshotSetProbeResult) error { return nil })
			if err == nil {
				t.Fatal("expected cleanup validation error")
			}
			if tc.deleteErr != nil && !errors.Is(err, tc.deleteErr) {
				t.Fatalf("error=%v, want delete error", err)
			}
			if tc.releaseErr != nil && !errors.Is(err, tc.releaseErr) {
				t.Fatalf("error=%v, want release error", err)
			}
			if !reflect.DeepEqual(session.deletedSetIDs, []string{"set-1"}) {
				t.Fatalf("deleted set IDs=%q, want exact session set", session.deletedSetIDs)
			}
			wantTail := []string{"complete", "delete:set-1", "release"}
			if got := session.events[len(session.events)-len(wantTail):]; !reflect.DeepEqual(got, wantTail) {
				t.Fatalf("cleanup events=%q, want %q", got, wantTail)
			}
		})
	}
}

func TestCoordinateVSSSnapshotSetProbeCleansUpFailedDoWhenSetDoesNotExist(t *testing.T) {
	sources := []string{`C:\`, `D:\`}
	wantErr := errors.New("snapshot creation failed")
	session := newFakeVSSSnapshotSetSession(sources...)
	session.doErr = wantErr
	session.deleteResult = vssSnapshotSetDeleteResult{}
	session.deleteErr = errVSSSnapshotSetNotFound

	err := coordinateVSSSnapshotSetProbe(sources, session, func([]VSSSnapshotSetProbeResult) error { return nil })
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	if session.deleteCalls != 1 || !reflect.DeepEqual(session.deletedSetIDs, []string{"set-1"}) {
		t.Fatalf("delete calls=%d set IDs=%q, want exact created set", session.deleteCalls, session.deletedSetIDs)
	}
	wantTail := []string{"do", "abort", "delete:set-1", "release"}
	if got := session.events[len(session.events)-len(wantTail):]; !reflect.DeepEqual(got, wantTail) {
		t.Fatalf("cleanup events=%q, want %q", got, wantTail)
	}
}

func TestCoordinateVSSSnapshotSetProbeCleansUpPartiallyCreatedFailedDo(t *testing.T) {
	sources := []string{`C:\`, `D:\`}
	wantErr := errors.New("snapshot creation partially failed")
	session := newFakeVSSSnapshotSetSession(sources...)
	session.doErr = wantErr
	session.deleteResult = vssSnapshotSetDeleteResult{DeletedSnapshots: 1}

	err := coordinateVSSSnapshotSetProbe(sources, session, func([]VSSSnapshotSetProbeResult) error { return nil })
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want only operation error %v", err, wantErr)
	}
	wantTail := []string{"do", "abort", "delete:set-1", "release"}
	if got := session.events[len(session.events)-len(wantTail):]; !reflect.DeepEqual(got, wantTail) {
		t.Fatalf("cleanup events=%q, want %q", got, wantTail)
	}
}

func TestCoordinateVSSSnapshotSetProbeAggregatesFailedDoCleanupErrors(t *testing.T) {
	sources := []string{`C:\`, `D:\`}
	doErr := errors.New("snapshot creation failed")
	deleteErr := errors.New("partial set deletion failed")
	releaseErr := errors.New("release failed")
	session := newFakeVSSSnapshotSetSession(sources...)
	session.doErr = doErr
	session.deleteResult = vssSnapshotSetDeleteResult{DeletedSnapshots: 1}
	session.deleteErr = deleteErr
	session.releaseErr = releaseErr

	err := coordinateVSSSnapshotSetProbe(sources, session, func([]VSSSnapshotSetProbeResult) error { return nil })
	for _, wantErr := range []error{doErr, deleteErr, releaseErr} {
		if !errors.Is(err, wantErr) {
			t.Fatalf("error=%v, want aggregated %v", err, wantErr)
		}
	}
	wantTail := []string{"do", "abort", "delete:set-1", "release"}
	if got := session.events[len(session.events)-len(wantTail):]; !reflect.DeepEqual(got, wantTail) {
		t.Fatalf("cleanup events=%q, want %q", got, wantTail)
	}
}

func TestCoordinateVSSSnapshotSetProbeTreatsObjectNotFoundAsAlreadyClean(t *testing.T) {
	sources := []string{`C:\`, `D:\`}
	session := newFakeVSSSnapshotSetSession(sources...)
	session.deleteResult = vssSnapshotSetDeleteResult{}
	session.deleteErr = errVSSSnapshotSetNotFound

	if err := coordinateVSSSnapshotSetProbe(sources, session, func([]VSSSnapshotSetProbeResult) error { return nil }); err != nil {
		t.Fatalf("VSS_E_OBJECT_NOT_FOUND must mean no residual set: %v", err)
	}
	wantTail := []string{"complete", "delete:set-1", "release"}
	if got := session.events[len(session.events)-len(wantTail):]; !reflect.DeepEqual(got, wantTail) {
		t.Fatalf("cleanup events=%q, want %q", got, wantTail)
	}
}

func TestCoordinateVSSSnapshotSetProbeDoesNotDeleteWithoutCreatedSetID(t *testing.T) {
	sources := []string{`C:\`, `D:\`}
	wantErr := errors.New("start failed")
	session := newFakeVSSSnapshotSetSession(sources...)
	session.startErr = wantErr

	err := coordinateVSSSnapshotSetProbe(sources, session, func([]VSSSnapshotSetProbeResult) error { return nil })
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	if session.deleteCalls != 0 || session.abortCalls != 0 {
		t.Fatalf("delete=%d abort=%d, want neither before StartSnapshotSet succeeds", session.deleteCalls, session.abortCalls)
	}
	wantEvents := []string{"start", "release"}
	if !reflect.DeepEqual(session.events, wantEvents) {
		t.Fatalf("events=%q, want %q", session.events, wantEvents)
	}
}
