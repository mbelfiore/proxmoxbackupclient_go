package main

import "testing"

func TestPhysicalDriveRecognitionCurrentCaseSensitiveBehavior(t *testing.T) {
	if idx, ok := physicalDriveIndex(`\\.\PhysicalDrive0`); !ok || idx != 0 {
		t.Fatalf("canonical spelling was not recognized: index=%d ok=%v", idx, ok)
	}
	for _, path := range []string{`\\.\PHYSICALDRIVE0`, `\\.\physicaldrive0`, `\\.\PhYsIcAlDrIvE42`} {
		if _, ok := physicalDriveIndex(path); ok {
			t.Fatalf("characterization changed: %q is now recognized", path)
		}
		t.Logf("BUG DEMONSTRATED (upstream #75): %q is routed as a generic file/device because recognition is case-sensitive", path)
	}
	for _, path := range []string{`C:\file`, `PhysicalDrive0`, `\\.\PhysicalDriveX`, `\\.\PhysicalDrive0.tmp`} {
		if _, ok := physicalDriveIndex(path); ok {
			t.Errorf("%q must not be recognized", path)
		}
	}
}
