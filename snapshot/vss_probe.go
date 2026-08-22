package snapshot

import (
	"errors"
	"fmt"
)

type VSSSnapshotSetProbeResult struct {
	Source           string
	SnapshotID       string
	SnapshotSetID    string
	DeviceObjectPath string
}

func validateVSSSnapshotSetProbe(sources []string, snapshots map[string]SnapShot) ([]VSSSnapshotSetProbeResult, error) {
	uniqueSources, err := uniqueVSSSources(sources)
	if err != nil {
		return nil, err
	}
	if len(uniqueSources) != 2 {
		return nil, fmt.Errorf("VSS snapshot-set probe requires exactly two distinct sources, got %d", len(uniqueSources))
	}
	if len(snapshots) != len(uniqueSources) {
		return nil, fmt.Errorf("VSS snapshot-set probe received %d snapshots, expected %d", len(snapshots), len(uniqueSources))
	}

	results := make([]VSSSnapshotSetProbeResult, 0, len(uniqueSources))
	seenSnapshotIDs := make(map[string]string, len(uniqueSources))
	snapshotSetID := ""
	for _, source := range uniqueSources {
		snapshot, exists := snapshots[source]
		if !exists {
			return nil, fmt.Errorf("VSS snapshot-set probe has no mapping for source %q", source)
		}
		if snapshot.Id == "" {
			return nil, fmt.Errorf("VSS snapshot-set probe source %q has an empty snapshot ID", source)
		}
		if previousSource, duplicate := seenSnapshotIDs[snapshot.Id]; duplicate {
			return nil, fmt.Errorf("VSS snapshot-set probe sources %q and %q share snapshot ID %q", previousSource, source, snapshot.Id)
		}
		seenSnapshotIDs[snapshot.Id] = source
		if snapshot.SnapshotSetID == "" {
			return nil, fmt.Errorf("VSS snapshot-set probe source %q has an empty snapshot set ID", source)
		}
		if snapshotSetID == "" {
			snapshotSetID = snapshot.SnapshotSetID
		} else if snapshot.SnapshotSetID != snapshotSetID {
			return nil, fmt.Errorf("VSS snapshot-set probe source %q belongs to set %q, expected %q", source, snapshot.SnapshotSetID, snapshotSetID)
		}
		if snapshot.ObjectPath == "" {
			return nil, fmt.Errorf("VSS snapshot-set probe source %q has an empty device object path", source)
		}

		results = append(results, VSSSnapshotSetProbeResult{
			Source:           source,
			SnapshotID:       snapshot.Id,
			SnapshotSetID:    snapshot.SnapshotSetID,
			DeviceObjectPath: snapshot.ObjectPath,
		})
	}
	return results, nil
}

type vssSnapshotSetProbeSession struct {
	vssSnapshotSetSession
	completed       bool
	aborted         bool
	releaseAbortErr error
}

func (s *vssSnapshotSetProbeSession) BackupComplete() error {
	err := s.vssSnapshotSetSession.BackupComplete()
	if err == nil {
		s.completed = true
	}
	return err
}

func (s *vssSnapshotSetProbeSession) AbortBackup() error {
	s.aborted = true
	return s.vssSnapshotSetSession.AbortBackup()
}

func (s *vssSnapshotSetProbeSession) Release() {
	if !s.completed && !s.aborted {
		s.aborted = true
		s.releaseAbortErr = s.vssSnapshotSetSession.AbortBackup()
	}
	s.vssSnapshotSetSession.Release()
}

func coordinateVSSSnapshotSetProbe(sources []string, session vssSnapshotSetSession, callback func([]VSSSnapshotSetProbeResult) error) error {
	if session == nil {
		return fmt.Errorf("VSS snapshot-set probe session is nil")
	}
	probeSession := &vssSnapshotSetProbeSession{vssSnapshotSetSession: session}
	if callback == nil {
		return errors.Join(coordinateVSSSnapshotSet(sources, probeSession, nil), probeSession.releaseAbortErr)
	}
	err := coordinateVSSSnapshotSet(sources, probeSession, func(snapshots map[string]SnapShot) error {
		results, err := validateVSSSnapshotSetProbe(sources, snapshots)
		if err != nil {
			return err
		}
		return callback(results)
	})
	return errors.Join(err, probeSession.releaseAbortErr)
}
