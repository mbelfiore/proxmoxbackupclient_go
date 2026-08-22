package snapshot

import (
	"fmt"
	"path/filepath"
)

type preparedVSSSnapshotRequest struct {
	key     string
	source  string
	subPath string
}

type vssSnapshotSymlink func(symlinkPath string, id string, deviceObjectPath string) (string, error)

func prepareVSSSnapshotRequests(paths []string, abs func(string) (string, error), volumeName func(string) string) ([]preparedVSSSnapshotRequest, []string, error) {
	requests := make([]preparedVSSSnapshotRequest, 0, len(paths))
	sources := make([]string, 0, len(paths))
	seenSources := make(map[string]struct{}, len(paths))

	for _, requestedPath := range paths {
		key, source, subPath, err := prepareSnapshotSource(requestedPath, abs, volumeName)
		if err != nil {
			return nil, nil, err
		}
		requests = append(requests, preparedVSSSnapshotRequest{
			key:     key,
			source:  source,
			subPath: subPath,
		})
		if _, exists := seenSources[source]; exists {
			continue
		}
		seenSources[source] = struct{}{}
		sources = append(sources, source)
	}

	return requests, sources, nil
}

func mapVSSSnapshotsToRequests(requests []preparedVSSSnapshotRequest, sourceSnapshots map[string]SnapShot, symlinkPath string, symlink vssSnapshotSymlink) (map[string]SnapShot, error) {
	if symlink == nil {
		return nil, fmt.Errorf("VSS snapshot symlink function is nil")
	}

	mapped := make(map[string]SnapShot, len(requests))
	symlinkRoots := make(map[string]string, len(sourceSnapshots))
	for _, request := range requests {
		sourceSnapshot, exists := sourceSnapshots[request.source]
		if !exists {
			return nil, fmt.Errorf("VSS source %q has no snapshot mapping", request.source)
		}

		root, exists := symlinkRoots[request.source]
		if !exists {
			var err error
			root, err = symlink(symlinkPath, sourceSnapshot.Id, sourceSnapshot.ObjectPath)
			if err != nil {
				return nil, fmt.Errorf("create VSS snapshot symlink for source %q: %w", request.source, err)
			}
			if root == "" {
				return nil, fmt.Errorf("create VSS snapshot symlink for source %q returned an empty path", request.source)
			}
			symlinkRoots[request.source] = root
		}

		mapped[request.key] = SnapShot{
			FullPath:      filepath.Join(root, request.subPath),
			Id:            sourceSnapshot.Id,
			SnapshotSetID: sourceSnapshot.SnapshotSetID,
			ObjectPath:    sourceSnapshot.ObjectPath,
			Valid:         sourceSnapshot.Valid,
		}
	}

	return mapped, nil
}

func createVSSSnapshotFromRequests(requests []preparedVSSSnapshotRequest, sources []string, session vssSnapshotSetSession, symlinkPath string, symlink vssSnapshotSymlink, backupCallback func(map[string]SnapShot) error) error {
	if backupCallback == nil {
		return coordinateVSSSnapshotSet(sources, session, nil)
	}

	return coordinateVSSSnapshotSetWithCleanup(sources, session, func(sourceSnapshots map[string]SnapShot) error {
		mapped, err := mapVSSSnapshotsToRequests(requests, sourceSnapshots, symlinkPath, symlink)
		if err != nil {
			return err
		}
		return backupCallback(mapped)
	})
}
