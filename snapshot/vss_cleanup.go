package snapshot

import (
	"errors"
	"fmt"
)

type vssSnapshotSetDeleteResult struct {
	DeletedSnapshots     int
	NondeletedSnapshotID string
}

type vssSnapshotSetCleanupSession interface {
	vssSnapshotSetSession
	DeleteSnapshotSet(snapshotSetID string) (vssSnapshotSetDeleteResult, error)
	ReleaseVSSSession() error
}

type delayedVSSSnapshotSetRelease struct {
	vssSnapshotSetCleanupSession
	snapshotSetID   string
	snapshotCreated bool
	completed       bool
	aborted         bool
}

func (s *delayedVSSSnapshotSetRelease) StartSnapshotSet() (string, error) {
	id, err := s.vssSnapshotSetCleanupSession.StartSnapshotSet()
	if err == nil {
		s.snapshotSetID = id
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

func validateVSSSnapshotSetDelete(result vssSnapshotSetDeleteResult, expectedSnapshots int, deleteErr error) error {
	var cleanupErr error
	if deleteErr != nil {
		cleanupErr = fmt.Errorf("delete VSS snapshot set: %w", deleteErr)
	}
	if result.DeletedSnapshots != expectedSnapshots {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("deleted %d VSS snapshots, expected %d", result.DeletedSnapshots, expectedSnapshots))
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
		if returnErr != nil && !delayed.completed && !delayed.aborted {
			returnErr = errors.Join(returnErr, delayed.AbortBackup())
		}
		if delayed.snapshotCreated {
			result, deleteErr := cleanupSession.DeleteSnapshotSet(delayed.snapshotSetID)
			returnErr = errors.Join(returnErr, validateVSSSnapshotSetDelete(result, len(uniqueSources), deleteErr))
		}
		returnErr = errors.Join(returnErr, cleanupSession.ReleaseVSSSession())
	}()

	return coordinateVSSSnapshotSet(sources, delayed, callback)
}
