//go:build !windows
// +build !windows

package snapshot

import "fmt"

func ProbeVSSSnapshotSet([]string, func([]VSSSnapshotSetProbeResult) error) error {
	return fmt.Errorf("VSS snapshot-set probe is only supported on Windows")
}
