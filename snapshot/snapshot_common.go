package snapshot

import "fmt"

type SnapShot struct {
	FullPath      string
	Id            string
	SnapshotSetID string
	ObjectPath    string
	Valid         bool
}

func isCanonicalWindowsVolumeGUIDRoot(path string) bool {
	const prefix = `\\?\Volume{`
	const suffix = `}\`
	if len(path) != len(prefix)+36+len(suffix) || path[:len(prefix)] != prefix || path[len(path)-len(suffix):] != suffix {
		return false
	}
	guid := path[len(prefix) : len(path)-len(suffix)]
	for i, char := range guid {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func prepareSnapshotSource(source string, abs func(string) (string, error), volumeName func(string) string) (key string, volume string, subPath string, err error) {
	if isCanonicalWindowsVolumeGUIDRoot(source) {
		return source, source, "", nil
	}
	path, err := abs(source)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve snapshot source %q: %w", source, err)
	}
	volume = volumeName(path) + `\`
	return path, volume, path[len(volume):], nil
}
