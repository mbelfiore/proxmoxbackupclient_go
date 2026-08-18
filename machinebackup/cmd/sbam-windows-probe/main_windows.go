//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	vss "github.com/st-matskevich/go-vss"
	"golang.org/x/sys/windows"
)

const (
	ioctlVolumeGetVolumeDiskExtents = 0x00560000
	maxQueryBuffer                  = 1024 * 1024
)

var (
	kernel32                         = windows.NewLazySystemDLL("kernel32.dll")
	findFirstVolumeW                 = kernel32.NewProc("FindFirstVolumeW")
	findNextVolumeW                  = kernel32.NewProc("FindNextVolumeW")
	findVolumeClose                  = kernel32.NewProc("FindVolumeClose")
	getVolumePathNamesForVolumeNameW = kernel32.NewProc("GetVolumePathNamesForVolumeNameW")
)

func main() {
	fmt.Println("SBAM WINDOWS RUNTIME PROBE")
	fmt.Println()
	if err := runProbe(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR=%v\n", err)
		os.Exit(1)
	}
}

func runProbe(output *os.File) error {
	// COM/VSS interfaces are apartment-bound. Keep initialization, queries,
	// release, and uninitialization on one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := ole.CoInitialize(0); err != nil {
		return fmt.Errorf("CoInitialize: %w", err)
	}
	defer ole.CoUninitialize()

	components, err := vss.LoadAndInitVSS()
	if err != nil {
		return fmt.Errorf("initialize VSS for read-only support queries: %w", err)
	}
	defer components.Release()

	return enumerateVolumes(func(volume probeVolume) error {
		volume.GUID = ensureTrailingBackslash(volume.GUID)
		volume.GUIDSupport = queryVolumeSupport(components, volume.GUID)
		volume.DriveRootSupport = make(map[string]supportResult)
		for _, mountPath := range volume.MountPaths {
			if classifyMountPath(mountPath) == mountDriveRoot {
				volume.DriveRootSupport[mountPath] = queryVolumeSupport(components, mountPath)
			}
		}
		writeVolumeReport(output, volume)
		return nil
	})
}

func queryVolumeSupport(components *vss.IVssBackupComponents, path string) supportResult {
	supported, err := components.IsVolumeSupported(path)
	result := supportResult{Supported: supported}
	if err != nil {
		result.Err = err.Error()
	}
	return result
}

func enumerateVolumes(visit func(probeVolume) error) (returnErr error) {
	name := make([]uint16, windows.MAX_PATH)
	result, _, callErr := findFirstVolumeW.Call(uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	if windows.Handle(result) == windows.InvalidHandle {
		return fmt.Errorf("FindFirstVolumeW: %w", callErr)
	}
	handle := windows.Handle(result)
	defer func() {
		closed, _, closeErr := findVolumeClose.Call(uintptr(handle))
		if closed == 0 && returnErr == nil {
			returnErr = fmt.Errorf("FindVolumeClose: %w", closeErr)
		}
	}()

	for {
		guid := ensureTrailingBackslash(windows.UTF16ToString(name))
		if guid == `\` {
			return fmt.Errorf("FindVolume returned an empty volume GUID")
		}
		mountPaths, err := volumeMountPaths(guid)
		if err != nil {
			return err
		}
		extents, err := volumeDiskExtents(guid)
		extentError := ""
		if err != nil {
			extents = nil
			extentError = err.Error()
		}
		if err := visit(probeVolume{GUID: guid, MountPaths: mountPaths, Extents: extents, ExtentError: extentError}); err != nil {
			return err
		}

		next, _, nextErr := findNextVolumeW.Call(uintptr(handle), uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
		if next != 0 {
			continue
		}
		if errors.Is(nextErr, windows.ERROR_NO_MORE_FILES) {
			return nil
		}
		return fmt.Errorf("FindNextVolumeW: %w", nextErr)
	}
}

func volumeMountPaths(guid string) ([]string, error) {
	guidPointer, err := windows.UTF16PtrFromString(guid)
	if err != nil {
		return nil, err
	}
	buffer := make([]uint16, 256)
	for {
		var used uint32
		result, _, callErr := getVolumePathNamesForVolumeNameW.Call(
			uintptr(unsafe.Pointer(guidPointer)),
			uintptr(unsafe.Pointer(&buffer[0])),
			uintptr(len(buffer)),
			uintptr(unsafe.Pointer(&used)),
		)
		if result != 0 {
			return parseMultiSZ(buffer, used)
		}
		if !errors.Is(callErr, windows.ERROR_MORE_DATA) && !errors.Is(callErr, windows.ERROR_INSUFFICIENT_BUFFER) {
			return nil, fmt.Errorf("GetVolumePathNamesForVolumeNameW(%s): %w", guid, callErr)
		}
		nextSize := int(used)
		if nextSize <= len(buffer) {
			nextSize = len(buffer) * 2
		}
		if nextSize <= 0 || nextSize > maxQueryBuffer/2 {
			return nil, fmt.Errorf("mount path buffer for %s exceeds safety limit", guid)
		}
		buffer = make([]uint16, nextSize)
	}
}

func volumeDiskExtents(guid string) (extents []probeExtent, returnErr error) {
	openPath, err := windows.UTF16PtrFromString(guid[:len(guid)-1])
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		openPath,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("CreateFile(volume %s): %w", guid, err)
	}
	defer func() {
		if closeErr := windows.CloseHandle(handle); closeErr != nil && returnErr == nil {
			returnErr = fmt.Errorf("CloseHandle(volume %s): %w", guid, closeErr)
		}
	}()

	buffer := make([]byte, extentHeaderSize+extentSize)
	for {
		var used uint32
		err = windows.DeviceIoControl(handle, ioctlVolumeGetVolumeDiskExtents, nil, 0, &buffer[0], uint32(len(buffer)), &used, nil)
		if err == nil {
			return parseExtents(buffer, used)
		}
		if !errors.Is(err, windows.ERROR_MORE_DATA) && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
			return nil, fmt.Errorf("IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS(%s): %w", guid, err)
		}
		if len(buffer) >= maxQueryBuffer {
			return nil, fmt.Errorf("extent buffer for %s exceeds safety limit", guid)
		}
		nextSize := len(buffer) * 2
		if nextSize > maxQueryBuffer {
			nextSize = maxQueryBuffer
		}
		buffer = make([]byte, nextSize)
	}
}
