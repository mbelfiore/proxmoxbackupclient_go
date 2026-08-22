//go:build windows
// +build windows

package snapshot

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

func SymlinkSnapshot(symlinkPath string, id string, deviceObjectPath string) (string, error) {

	snapshotSymLinkFolder := symlinkPath + "\\" + id + "\\"

	snapshotSymLinkFolder = filepath.Clean(snapshotSymLinkFolder)
	os.RemoveAll(snapshotSymLinkFolder)
	if err := os.MkdirAll(snapshotSymLinkFolder, 0700); err != nil {
		return "", fmt.Errorf("failed to create snapshot symlink folder for snapshot: %s, err: %s", id, err)
	}

	os.Remove(snapshotSymLinkFolder)

	fmt.Println("Symlink from: ", deviceObjectPath, " to: ", snapshotSymLinkFolder)

	if err := os.Symlink(deviceObjectPath, snapshotSymLinkFolder); err != nil {
		return "", fmt.Errorf("failed to create symlink from: %s to: %s, error: %s", deviceObjectPath, snapshotSymLinkFolder, err)
	}

	return snapshotSymLinkFolder, nil
}

func getAppDataFolder() (string, error) {
	// Get information about the current user
	currentUser, err := user.Current()
	if err != nil {
		return "", err
	}

	// Construct the path to the application data folder
	appDataFolder := filepath.Join(currentUser.HomeDir, "AppData", "Roaming", "PBSBackupGO")

	// Create the folder if it doesn't exist
	err = os.MkdirAll(appDataFolder, os.ModePerm)
	if err != nil {
		return "", err
	}

	return appDataFolder, nil
}

func CreateVSSSnapshot(paths []string, backup_callback func(sn map[string]SnapShot) error) error {
	requests, sources, err := prepareVSSSnapshotRequests(paths, filepath.Abs, filepath.VolumeName)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		if backup_callback == nil {
			return fmt.Errorf("VSS backup callback is nil")
		}
		return backup_callback(map[string]SnapShot{})
	}

	appDataFolder, err := getAppDataFolder()
	if err != nil {
		fmt.Println("Error:", err)
		return err
	}

	return withWindowsVSSSnapshotSetSession(sources, func(session vssSnapshotSetSession) error {
		fmt.Printf("Creating coordinated VSS snapshot set...\n")
		return createVSSSnapshotFromRequests(requests, sources, session, filepath.Join(appDataFolder, "VSS"), SymlinkSnapshot, backup_callback)
	})

}

func VSSCleanup() {

}
