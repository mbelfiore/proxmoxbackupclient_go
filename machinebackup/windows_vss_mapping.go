package main

import (
	"fmt"
	"sort"

	"snapshot"
)

func validateWindowsSnapshotMapping(expectedSources []string, snapshots map[string]snapshot.SnapShot) error {
	expected := make(map[string]struct{}, len(expectedSources))
	for i, source := range expectedSources {
		normalized, err := normalizeWindowsVSSSource(source)
		if err != nil {
			return fmt.Errorf("expected Windows VSS source %d: %w", i, err)
		}
		if normalized != source {
			return fmt.Errorf("expected Windows VSS source %q is not normalized as %q", source, normalized)
		}
		if _, exists := expected[source]; exists {
			return fmt.Errorf("expected Windows VSS source %q is duplicated", source)
		}
		expected[source] = struct{}{}
	}

	missing := make([]string, 0)
	for source := range expected {
		if _, exists := snapshots[source]; !exists {
			missing = append(missing, source)
		}
	}
	unexpected := make([]string, 0)
	for source := range snapshots {
		if _, exists := expected[source]; !exists {
			unexpected = append(unexpected, source)
		}
	}
	if len(missing) != 0 || len(unexpected) != 0 {
		sort.Strings(missing)
		sort.Strings(unexpected)
		return fmt.Errorf("Windows VSS snapshot mapping differs from plan: missing=%q unexpected=%q", missing, unexpected)
	}

	var snapshotSetID string
	snapshotIDs := make(map[string]string, len(snapshots))
	for _, source := range expectedSources {
		mapped := snapshots[source]
		if !mapped.Valid {
			return fmt.Errorf("Windows VSS source %q returned an invalid snapshot", source)
		}
		if mapped.Id == "" {
			return fmt.Errorf("Windows VSS source %q returned an empty snapshot ID", source)
		}
		if mapped.SnapshotSetID == "" {
			return fmt.Errorf("Windows VSS source %q returned an empty snapshot set ID", source)
		}
		if mapped.ObjectPath == "" {
			return fmt.Errorf("Windows VSS source %q returned an empty device object path", source)
		}
		if existingSource, exists := snapshotIDs[mapped.Id]; exists {
			return fmt.Errorf("Windows VSS sources %q and %q returned duplicate snapshot ID %q", existingSource, source, mapped.Id)
		}
		snapshotIDs[mapped.Id] = source
		if snapshotSetID == "" {
			snapshotSetID = mapped.SnapshotSetID
		} else if mapped.SnapshotSetID != snapshotSetID {
			return fmt.Errorf("Windows VSS source %q belongs to snapshot set %q, expected %q", source, mapped.SnapshotSetID, snapshotSetID)
		}
	}

	return nil
}
