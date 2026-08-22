package snapshot

import (
	"errors"
	"fmt"
)

// vssSnapshotSetSession is the narrow VSS surface required to coordinate one
// snapshot set. A Windows adapter will implement this seam in a later step;
// keeping the orchestration independent permits deterministic tests without
// invoking COM or creating real shadow copies.
type vssSnapshotSetSession interface {
	StartSnapshotSet() (string, error)
	AddToSnapshotSet(source string) (string, error)
	PrepareForBackup() error
	DoSnapshotSet() error
	GetSnapshotProperties(snapshotID string) (vssSnapshotProperties, error)
	BackupComplete() error
	AbortBackup() error
	Release()
}

type vssSnapshotProperties struct {
	SnapshotID       string
	SnapshotSetID    string
	DeviceObjectPath string
}

func uniqueVSSSources(sources []string) ([]string, error) {
	unique := make([]string, 0, len(sources))
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if source == "" {
			return nil, fmt.Errorf("VSS snapshot source is empty")
		}
		if _, exists := seen[source]; exists {
			continue
		}
		seen[source] = struct{}{}
		unique = append(unique, source)
	}
	return unique, nil
}

// coordinateVSSSnapshotSet owns session cleanup. Every distinct source is
// added to one set before preparation, and the callback runs only after the
// complete set has been created and validated.
func coordinateVSSSnapshotSet(sources []string, session vssSnapshotSetSession, backupCallback func(map[string]SnapShot) error) (returnErr error) {
	if session == nil {
		return fmt.Errorf("VSS snapshot session is nil")
	}
	defer session.Release()

	uniqueSources, err := uniqueVSSSources(sources)
	if err != nil {
		return err
	}
	if len(uniqueSources) == 0 {
		return fmt.Errorf("VSS snapshot set requires at least one source")
	}
	if backupCallback == nil {
		return fmt.Errorf("VSS backup callback is nil")
	}

	prepared := false
	completed := false
	defer func() {
		if prepared && !completed {
			returnErr = errors.Join(returnErr, session.AbortBackup())
		}
	}()

	snapshotSetID, err := session.StartSnapshotSet()
	if err != nil {
		return fmt.Errorf("start VSS snapshot set: %w", err)
	}
	if snapshotSetID == "" {
		return fmt.Errorf("start VSS snapshot set returned an empty set ID")
	}

	snapshotIDs := make(map[string]string, len(uniqueSources))
	for _, source := range uniqueSources {
		snapshotID, err := session.AddToSnapshotSet(source)
		if err != nil {
			return fmt.Errorf("add VSS source %q to snapshot set: %w", source, err)
		}
		if snapshotID == "" {
			return fmt.Errorf("add VSS source %q returned an empty snapshot ID", source)
		}
		snapshotIDs[source] = snapshotID
	}

	if err := session.PrepareForBackup(); err != nil {
		return fmt.Errorf("prepare VSS snapshot set: %w", err)
	}
	prepared = true

	if err := session.DoSnapshotSet(); err != nil {
		return fmt.Errorf("create VSS snapshot set: %w", err)
	}

	snapshots := make(map[string]SnapShot, len(uniqueSources))
	for _, source := range uniqueSources {
		snapshotID := snapshotIDs[source]
		properties, err := session.GetSnapshotProperties(snapshotID)
		if err != nil {
			return fmt.Errorf("get VSS snapshot properties for source %q: %w", source, err)
		}
		if properties.SnapshotID != snapshotID {
			return fmt.Errorf("VSS source %q returned snapshot ID %q, expected %q", source, properties.SnapshotID, snapshotID)
		}
		if properties.SnapshotSetID != snapshotSetID {
			return fmt.Errorf("VSS source %q belongs to snapshot set %q, expected %q", source, properties.SnapshotSetID, snapshotSetID)
		}
		if properties.DeviceObjectPath == "" {
			return fmt.Errorf("VSS source %q returned an empty device object path", source)
		}
		snapshots[source] = SnapShot{
			Id:            snapshotID,
			SnapshotSetID: properties.SnapshotSetID,
			ObjectPath:    properties.DeviceObjectPath,
			Valid:         true,
		}
	}

	if err := backupCallback(snapshots); err != nil {
		return fmt.Errorf("VSS snapshot callback: %w", err)
	}
	if err := session.BackupComplete(); err != nil {
		return fmt.Errorf("complete VSS backup session: %w", err)
	}
	completed = true
	return nil
}
