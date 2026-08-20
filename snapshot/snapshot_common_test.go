package snapshot

import (
	"errors"
	"testing"
)

func TestCanonicalWindowsVolumeGUIDRoot(t *testing.T) {
	valid := `\\?\Volume{3a886445-0000-0000-0000-100000000000}\`
	tests := []struct {
		value string
		want  bool
	}{
		{valid, true},
		{`\\?\Volume{3A886445-0000-0000-0000-100000000000}\`, true},
		{"", false},
		{`\\?\Volume{}\`, false},
		{`\\?\Volume{3a886445-0000-0000-0000-100000000000}`, false},
		{`\\?\Volume{3a886445-0000-0000-0000-10000000000}\`, false},
		{`\\?\Volume{3a886445-0000-0000-0000-10000000000g}\`, false},
		{valid + `child`, false},
		{`C:\`, false},
	}
	for _, tc := range tests {
		if got := isCanonicalWindowsVolumeGUIDRoot(tc.value); got != tc.want {
			t.Errorf("isCanonicalWindowsVolumeGUIDRoot(%q)=%v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestPrepareSnapshotSource(t *testing.T) {
	guid := `\\?\Volume{3a886445-0000-0000-0000-100000000000}\`
	absCalled, volumeCalled := false, false
	key, volume, subPath, err := prepareSnapshotSource(guid, func(string) (string, error) {
		absCalled = true
		return "", nil
	}, func(string) string {
		volumeCalled = true
		return ""
	})
	if err != nil || key != guid || volume != guid || subPath != "" || absCalled || volumeCalled {
		t.Fatalf("GUID preparation=(%q,%q,%q,%v), abs=%v volumeName=%v", key, volume, subPath, err, absCalled, volumeCalled)
	}

	key, volume, subPath, err = prepareSnapshotSource(`relative\directory`, func(path string) (string, error) {
		if path != `relative\directory` {
			t.Fatalf("unexpected path to Abs: %q", path)
		}
		return `C:\base\relative\directory`, nil
	}, func(path string) string {
		if path != `C:\base\relative\directory` {
			t.Fatalf("unexpected path to VolumeName: %q", path)
		}
		return `C:`
	})
	if err != nil || key != `C:\base\relative\directory` || volume != `C:\` || subPath != `base\relative\directory` {
		t.Fatalf("ordinary preparation=(%q,%q,%q,%v)", key, volume, subPath, err)
	}

	wantErr := errors.New("abs failed")
	if _, _, _, err := prepareSnapshotSource("relative", func(string) (string, error) { return "", wantErr }, func(string) string { return "" }); !errors.Is(err, wantErr) {
		t.Fatalf("Abs error=%v, want %v", err, wantErr)
	}
}
