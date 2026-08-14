package main

import "testing"

func TestPhysicalDriveRecognitionIsCaseInsensitive(t *testing.T) {
	for path, want := range map[string]int{`\\.\PhysicalDrive0`: 0, `\\.\PHYSICALDRIVE0`: 0, `\\.\physicaldrive0`: 0, `\\.\PhYsIcAlDrIvE42`: 42} {
		idx, ok := physicalDriveIndex(path)
		if !ok || idx != want {
			t.Errorf("physicalDriveIndex(%q)=(%d,%v), want (%d,true)", path, idx, ok, want)
		}
	}
	for _, path := range []string{`C:\file`, `PhysicalDrive0`, `\\.\PhysicalDriveX`, `\\.\PhysicalDrive0.tmp`} {
		if _, ok := physicalDriveIndex(path); ok {
			t.Errorf("%q must not be recognized", path)
		}
	}
}
