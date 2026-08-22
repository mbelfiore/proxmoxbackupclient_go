package main

import (
	"fmt"
	"os"

	"snapshot"
)

var probeSources = []string{
	`\\?\Volume{3a886445-0000-0000-0000-100000000000}\`,
	`C:\`,
}

func main() {
	err := snapshot.ProbeVSSSnapshotSet(probeSources, func(results []snapshot.VSSSnapshotSetProbeResult) error {
		for _, result := range results {
			fmt.Printf("Source: %s\n", result.Source)
			fmt.Printf("SnapshotID: %s\n", result.SnapshotID)
			fmt.Printf("SnapshotSetID: %s\n", result.SnapshotSetID)
			fmt.Printf("DeviceObjectPath: %s\n", result.DeviceObjectPath)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS")
}
