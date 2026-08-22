package snapshot

import (
	"errors"
	"fmt"
)

type vssSnapshotSetDeleteResult struct {
	DeletedSnapshots     int
	NondeletedSnapshotID string
}

var errVSSSnapshotSetNotFound = errors.New("VSS snapshot set not found")

type vssSnapshotSetCleanupSession interface {
	vssSnapshotSetSession
	DeleteSnapshotSet(snapshotSetID string) (vssSnapshotSetDeleteResult, error)
	ReleaseVSSSession() error
}

type delayedVSSSnapshotSetRelease struct {
	vssSnapshotSetCleanupSession
	snapshotSetID      string
	snapshotSetStarted bool
	snapshotCreated    bool
	completed          bool
	aborted            bool
}

func (s *delayedVSSSnapshotSetRelease) StartSnapshotSet() (string, error) {
	id, err := s.vssSnapshotSetCleanupSession.StartSnapshotSet()
	if err == nil {
		s.snapshotSetID = id
		s.snapshotSetStarted = true
	}
	return id, err
}

func (s *delayedVSSSnapshotSetRelease) DoSnapshotSet() error {
	err := s.vssSnapshotSetCleanupSession.DoSnapshotSet()
	if err == nil {
		s.snapshotCreated = true
	}
	return err
}

func (s *delayedVSSSnapshotSetRelease) BackupComplete() error {
	err := s.vssSnapshotSetCleanupSession.BackupComplete()
	if err == nil {
		s.completed = true
	}
	return err
}

func (s *delayedVSSSnapshotSetRelease) AbortBackup() error {
	s.aborted = true
	return s.vssSnapshotSetCleanupSession.AbortBackup()
}

func (s *delayedVSSSnapshotSetRelease) Release() {
	// The coordinator owns the request to release. The real COM release is
	// delayed until the exact snapshot set has been deleted and validated.
}

func validateVSSSnapshotSetDelete(result vssSnapshotSetDeleteResult, expectedSnapshots int, requireExactCount bool, deleteErr error) error {
	if errors.Is(deleteErr, errVSSSnapshotSetNotFound) {
		// A failed DoSnapshotSet may leave no object behind. In that case there
		// is no residual snapshot set to clean up.
		return nil
	}
	var cleanupErr error
	if deleteErr != nil {
		cleanupErr = fmt.Errorf("delete VSS snapshot set: %w", deleteErr)
	}
	if requireExactCount && result.DeletedSnapshots != expectedSnapshots {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("deleted %d VSS snapshots, expected %d", result.DeletedSnapshots, expectedSnapshots))
	} else if !requireExactCount && (result.DeletedSnapshots < 0 || result.DeletedSnapshots > expectedSnapshots) {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("deleted %d VSS snapshots after partial creation, expected between 0 and %d", result.DeletedSnapshots, expectedSnapshots))
	}
	if result.NondeletedSnapshotID != "" {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("VSS snapshot %q was not deleted", result.NondeletedSnapshotID))
	}
	return cleanupErr
}

func coordinateVSSSnapshotSetWithCleanup(sources []string, session vssSnapshotSetSession, callback func(map[string]SnapShot) error) (returnErr error) {
	cleanupSession, ok := session.(vssSnapshotSetCleanupSession)
	if !ok {
		if session != nil {
			session.Release()
		}
		return fmt.Errorf("VSS snapshot session does not support deterministic snapshot-set cleanup")
	}
	uniqueSources, err := uniqueVSSSources(sources)
	if err != nil {
		return errors.Join(err, cleanupSession.ReleaseVSSSession())
	}
	delayed := &delayedVSSSnapshotSetRelease{vssSnapshotSetCleanupSession: cleanupSession}
	defer func() {
		if returnErr != nil && delayed.snapshotSetStarted && !delayed.completed && !delayed.aborted {
			returnErr = errors.Join(returnErr, delayed.AbortBackup())
		}
		if delayed.snapshotSetStarted {
			result, deleteErr := cleanupSession.DeleteSnapshotSet(delayed.snapshotSetID)
			returnErr = errors.Join(returnErr, validateVSSSnapshotSetDelete(result, len(uniqueSources), delayed.snapshotCreated, deleteErr))
		}
		returnErr = errors.Join(returnErr, cleanupSession.ReleaseVSSSession())
	}()

	return coordinateVSSSnapshotSet(sources, delayed, callback)
}
