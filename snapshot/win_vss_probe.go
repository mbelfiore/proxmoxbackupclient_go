//go:build windows
// +build windows

package snapshot

func ProbeVSSSnapshotSet(sources []string, callback func([]VSSSnapshotSetProbeResult) error) error {
	return withWindowsVSSSnapshotSetSession(sources, func(session vssSnapshotSetSession) error {
		return coordinateVSSSnapshotSetProbe(sources, session, callback)
	})
}
