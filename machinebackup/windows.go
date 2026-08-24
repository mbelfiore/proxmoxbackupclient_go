//go:build windows
// +build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"pbscommon"
	"snapshot"
	"strings"
	"syscall"
	"unsafe"

	"github.com/tawesoft/golib/v2/dialog"
	"golang.org/x/sys/windows"
)

type PARTITION_STYLE uint32

const (
	PartitionStyleMBR PARTITION_STYLE = 0
	PartitionStyleGPT PARTITION_STYLE = 1
)

type DRIVE_LAYOUT_INFORMATION_MBR struct {
	Signature uint32
	CheckSum  uint32
}

type DRIVE_LAYOUT_INFORMATION_GPT struct {
	DiskId windows.GUID
}

type GET_LENGTH_INFORMATION struct {
	Length int64
}

type DRIVE_LAYOUT_INFORMATION_EX struct {
	PartitionStyle uint32
	PartitionCount uint32
	/*DUMMYUNIONNAME struct {
		Mbr DRIVE_LAYOUT_INFORMATION_MBR
		Gpt DRIVE_LAYOUT_INFORMATION_GPT
	}*/
	PlaceHolder    [36]byte
	PartitionEntry [128]partitionInformationEX
}

const IOCTL_DISK_GET_DRIVE_LAYOUT_EX = 0x00070050
const IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS = 0x00560000
const IOCTL_DISK_GET_LENGTH_INFO = 0x0007405C

var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procFindFirstVolumeW             = modkernel32.NewProc("FindFirstVolumeW")
	procFindNextVolumeW              = modkernel32.NewProc("FindNextVolumeW")
	procFindVolumeClose              = modkernel32.NewProc("FindVolumeClose")
	procGetDriveTypeW                = modkernel32.NewProc("GetDriveTypeW")
	procGetVolumePathNamesForVolumeW = modkernel32.NewProc("GetVolumePathNamesForVolumeNameW")
)

const maxVolumeQueryBuffer = 1024 * 1024

func getVolumeMountPaths(volumeGUID string) ([]string, error) {
	volumeName, err := windows.UTF16PtrFromString(volumeGUID)
	if err != nil {
		return nil, err
	}
	buffer := make([]uint16, 256)
	for {
		var returnLength uint32
		result, _, callErr := procGetVolumePathNamesForVolumeW.Call(
			uintptr(unsafe.Pointer(volumeName)),
			uintptr(unsafe.Pointer(&buffer[0])),
			uintptr(len(buffer)),
			uintptr(unsafe.Pointer(&returnLength)),
		)
		if result != 0 {
			return parseUTF16MultiSZ(buffer, returnLength)
		}
		if !errors.Is(callErr, windows.ERROR_MORE_DATA) && !errors.Is(callErr, windows.ERROR_INSUFFICIENT_BUFFER) {
			return nil, fmt.Errorf("GetVolumePathNamesForVolumeNameW(%s): %w", volumeGUID, callErr)
		}
		nextSize := int(returnLength)
		if nextSize <= len(buffer) {
			nextSize = len(buffer) * 2
		}
		if nextSize <= 0 || nextSize > maxVolumeQueryBuffer/2 {
			return nil, fmt.Errorf("mount path buffer for %s exceeds safety limit", volumeGUID)
		}
		buffer = make([]uint16, nextSize)
	}
}

func getVolumeDiskExtents(handle windows.Handle, volumeGUID string) ([]WindowsDiskExtent, error) {
	buffer := make([]byte, volumeDiskExtentsHeaderSize+volumeDiskExtentSize)
	for {
		var bytesReturned uint32
		err := windows.DeviceIoControl(
			handle,
			IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS,
			nil,
			0,
			&buffer[0],
			uint32(len(buffer)),
			&bytesReturned,
			nil,
		)
		if err == nil {
			return parseVolumeDiskExtents(buffer, bytesReturned)
		}
		if !errors.Is(err, windows.ERROR_MORE_DATA) && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
			return nil, fmt.Errorf("IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS(%s): %w", volumeGUID, err)
		}
		if len(buffer) >= maxVolumeQueryBuffer {
			return nil, fmt.Errorf("extent buffer for %s exceeds safety limit", volumeGUID)
		}
		nextSize := len(buffer) * 2
		if nextSize > maxVolumeQueryBuffer {
			nextSize = maxVolumeQueryBuffer
		}
		buffer = make([]byte, nextSize)
	}
}

func enumWindowsVolumes() (volumes []WindowsVolume, returnErr error) {
	volumeName := make([]uint16, windows.MAX_PATH)

	result, _, callErr := procFindFirstVolumeW.Call(
		uintptr(unsafe.Pointer(&volumeName[0])),
		uintptr(len(volumeName)),
	)
	if windows.Handle(result) == windows.InvalidHandle {
		return nil, fmt.Errorf("FindFirstVolumeW: %w", callErr)
	}
	findHandle := windows.Handle(result)
	defer func() {
		closed, _, closeErr := procFindVolumeClose.Call(uintptr(findHandle))
		if closed == 0 && returnErr == nil {
			returnErr = fmt.Errorf("FindVolumeClose: %w", closeErr)
		}
	}()

	for {
		volumeGUID := windows.UTF16ToString(volumeName)
		if volumeGUID == "" {
			return nil, fmt.Errorf("FindVolume returned an empty volume GUID")
		}
		fmt.Println(volumeGUID)
		volumeRoot, err := windows.UTF16PtrFromString(volumeGUID)
		if err != nil {
			return nil, err
		}
		driveType, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(volumeRoot)))
		if skipWindowsVolumeInventory(WindowsDriveType(driveType)) {
			fmt.Printf("Skipping verified CD/DVD volume %s during disk extent inventory\n", volumeGUID)
		} else {
			openName, err := windows.UTF16PtrFromString(strings.TrimSuffix(volumeGUID, "\\"))
			if err != nil {
				return nil, err
			}
			handle, err := windows.CreateFile(
				openName,
				windows.GENERIC_READ,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
				nil,
				windows.OPEN_EXISTING,
				0,
				0,
			)
			if err != nil {
				return nil, fmt.Errorf("CreateFile(%s): %w", volumeGUID, err)
			}
			extents, extentErr := getVolumeDiskExtents(handle, volumeGUID)
			closeErr := windows.CloseHandle(handle)
			if extentErr != nil {
				return nil, extentErr
			}
			if closeErr != nil {
				return nil, fmt.Errorf("CloseHandle(%s): %w", volumeGUID, closeErr)
			}
			mountPaths, err := getVolumeMountPaths(volumeGUID)
			if err != nil {
				return nil, err
			}
			volumes = append(volumes, WindowsVolume{VolumeGUID: volumeGUID, MountPaths: mountPaths, Extents: extents})
		}

		next, _, nextErr := procFindNextVolumeW.Call(
			uintptr(findHandle),
			uintptr(unsafe.Pointer(&volumeName[0])),
			uintptr(len(volumeName)),
		)
		if next == 0 {
			if errors.Is(nextErr, windows.ERROR_NO_MORE_FILES) {
				return volumes, nil
			}
			return nil, fmt.Errorf("FindNextVolumeW: %w", nextErr)
		}
	}
}

func GetDiskLength(path string) (int64, error) {
	// Open the device (e.g., \\.\PhysicalDrive0 or \\.\C:)
	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return 0, fmt.Errorf("CreateFile failed: %w", err)
	}
	defer windows.CloseHandle(handle)

	var lengthInfo GET_LENGTH_INFORMATION
	var bytesReturned uint32

	err = windows.DeviceIoControl(
		handle,
		IOCTL_DISK_GET_LENGTH_INFO,
		nil,
		0,
		(*byte)(unsafe.Pointer(&lengthInfo)),
		uint32(unsafe.Sizeof(lengthInfo)),
		&bytesReturned,
		nil,
	)
	if err != nil {
		return 0, fmt.Errorf("DeviceIoControl failed: %w", err)
	}

	return lengthInfo.Length, nil
}

func backupWindowsDisk(client *pbscommon.PBSClient, index int) (int64, error) {
	parts := make([]Partition, 0)
	diskdev := fmt.Sprintf("\\\\.\\PhysicalDrive%d", index)
	volumeHandle, err := syscall.CreateFile(
		syscall.StringToUTF16Ptr(diskdev), // Example volume C:
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil,
		syscall.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		dialog.Error(err.Error())
		return 0, err
	}
	defer syscall.CloseHandle(volumeHandle)
	var volumeDiskExtents DRIVE_LAYOUT_INFORMATION_EX
	var bytesReturned uint32

	// First call to get the required size (if needed)
	// ...

	// Second call with a properly sized buffer
	err = syscall.DeviceIoControl(
		volumeHandle,
		IOCTL_DISK_GET_DRIVE_LAYOUT_EX, // Define this constant
		nil,
		0,
		(*byte)(unsafe.Pointer(&volumeDiskExtents)), // Output buffer
		uint32(unsafe.Sizeof(volumeDiskExtents)),    // Size of output buffer
		&bytesReturned,
		nil,
	)

	if err != nil {
		dialog.Error(err.Error())
		return 0, err
	}

	partitionEntriesOffset := uint64(unsafe.Offsetof(volumeDiskExtents.PartitionEntry))
	partitionEntrySize := uint64(unsafe.Sizeof(volumeDiskExtents.PartitionEntry[0]))
	if uint64(bytesReturned) < partitionEntriesOffset {
		return 0, fmt.Errorf("drive layout response too short: %d", bytesReturned)
	}
	if int(volumeDiskExtents.PartitionCount) > len(volumeDiskExtents.PartitionEntry) {
		return 0, fmt.Errorf("partition count %d exceeds supported layout buffer", volumeDiskExtents.PartitionCount)
	}
	requiredLayoutBytes := partitionEntriesOffset + uint64(volumeDiskExtents.PartitionCount)*partitionEntrySize
	if requiredLayoutBytes < partitionEntriesOffset || uint64(bytesReturned) < requiredLayoutBytes {
		return 0, fmt.Errorf("drive layout count %d requires %d bytes, got %d", volumeDiskExtents.PartitionCount, requiredLayoutBytes, bytesReturned)
	}
	total, err := GetDiskLength(diskdev)
	if err != nil {
		return 0, err
	}
	if total < 0 {
		return 0, fmt.Errorf("negative disk length %d", total)
	}
	partitionExtents := make([]DiskExtent, 0, volumeDiskExtents.PartitionCount)
	partitionIdentities := make([]WindowsPartitionIdentity, 0, volumeDiskExtents.PartitionCount)
	for i := 0; i < int(volumeDiskExtents.PartitionCount); i++ {
		entry := volumeDiskExtents.PartitionEntry[i]
		if entry.PartitionNumber == 0 {
			continue //Windows API sometimes wrongly returns a partition that is effectively null, probably in case of MBR it is fixed 4 partitions anyway
		}
		if entry.StartingOffset < 0 || entry.PartitionLength <= 0 {
			return 0, fmt.Errorf("partition %d has invalid offset/length", entry.PartitionNumber)
		}
		start, length := uint64(entry.StartingOffset), uint64(entry.PartitionLength)
		if start > ^uint64(0)-length {
			return 0, fmt.Errorf("partition %d offset/length overflows", entry.PartitionNumber)
		}
		identity, err := partitionIdentity(entry)
		if err != nil {
			return 0, fmt.Errorf("partition %d: %w", entry.PartitionNumber, err)
		}
		fmt.Printf("Part: %d %s %s\n", entry.PartitionNumber, BytesToString(entry.StartingOffset), BytesToString(entry.PartitionLength))
		partitionExtents = append(partitionExtents, DiskExtent{Start: start, End: start + length})
		partitionIdentities = append(partitionIdentities, identity)
	}
	style := DiskLayoutMBR
	if PARTITION_STYLE(volumeDiskExtents.PartitionStyle) == PartitionStyleGPT {
		style = DiskLayoutGPT
	} else if PARTITION_STYLE(volumeDiskExtents.PartitionStyle) != PartitionStyleMBR {
		return 0, fmt.Errorf("unsupported Windows partition style %d", volumeDiskExtents.PartitionStyle)
	}
	if err := validateWindowsPartitionIdentities(partitionIdentities); err != nil {
		return 0, err
	}
	for i, identity := range partitionIdentities {
		if identity.Style != style {
			return 0, fmt.Errorf("partition %d style %q differs from disk style %q", i, identity.Style, style)
		}
	}
	volumes, err := enumWindowsVolumes()
	if err != nil {
		dialog.Error(err.Error())
		return 0, err
	}
	plan, err := buildValidatedWindowsDiskPlan(uint64(total), style, uint32(index), partitionExtents, partitionIdentities, volumes)
	if err != nil {
		return 0, err
	}
	plan, preflight, err := prepareWindowsVSSPlan(plan)
	if err != nil {
		return 0, fmt.Errorf("Windows backup preflight for PhysicalDrive%d: %w", index, err)
	}
	fmt.Printf("Windows backup preflight PhysicalDrive%d: %d VSS source(s)\n", index, len(preflight.Sources))
	for _, entry := range preflight.Entries {
		fmt.Printf("VSS segment %d..%d volume=%q source=%q normalized=%q\n", entry.SegmentStart, entry.SegmentEnd, entry.VolumeGUID, entry.OriginalSource, entry.NormalizedSource)
	}
	for _, segment := range plan {
		parts = append(parts, Partition{StartByte: segment.Start, EndByte: segment.End, RequiresVSS: !segment.Raw, VSSSource: segment.VSSSource})
	}
	snapshotPaths := preflight.Sources

	return total, snapshot.CreateVSSSnapshot(snapshotPaths, func(snapshots map[string]snapshot.SnapShot) error {
		if err := validateWindowsSnapshotMapping(snapshotPaths, snapshots); err != nil {
			return err
		}

		/*hostname, err := os.Hostname()
		if err != nil {
			fmt.Println("Failed to retrieve hostname:", err)
			hostname = "unknown"
		}*/

		fmt.Printf("%+v\n", parts)

		//begin := time.Now()
		physicalDisk, err := os.Open(diskdev)
		if err != nil {
			return err
		}
		defer physicalDisk.Close()

		producer := func(ctx context.Context, emit func([]byte) error) error {
			stopClose := closeOnCancellation(ctx, physicalDisk)
			defer stopClose()
			buffer := make([]byte, 0)
			emitBuffered := func(data []byte) error {
				buffer = append(buffer, data...)
				for len(buffer) >= pbscommon.PBS_FIXED_CHUNK_SIZE {
					block := append([]byte(nil), buffer[:pbscommon.PBS_FIXED_CHUNK_SIZE]...)
					if err := emit(block); err != nil {
						return err
					}
					buffer = buffer[pbscommon.PBS_FIXED_CHUNK_SIZE:]
				}
				return nil
			}

			for idx, partition := range parts {
				fmt.Printf("Partition: %d\n", idx)
				if !partition.RequiresVSS {
					if _, err := physicalDisk.Seek(int64(partition.StartByte), io.SeekStart); err != nil {
						return err
					}
					block := make([]byte, pbscommon.PBS_FIXED_CHUNK_SIZE)
					position := partition.StartByte
					for position < partition.EndByte {
						bytesRead, err := physicalDisk.Read(block[:min(uint64(len(block)), partition.EndByte-position)])
						if bytesRead > 0 {
							if emitErr := emitBuffered(block[:bytesRead]); emitErr != nil {
								return emitErr
							}
							position += uint64(bytesRead)
						}
						if err != nil {
							return err
						}
						if bytesRead == 0 {
							return fmt.Errorf("failed to read partition at %d", position)
						}
					}
					if position != partition.EndByte {
						return fmt.Errorf("failed to read partition entirely %d/%d", position, partition.EndByte)
					}
					continue
				}

				snapshot, ok := snapshots[partition.VSSSource]
				if !ok {
					return fmt.Errorf("cannot find snapshot for VSS source %s", partition.VSSSource)
				}
				snapshotPath := strings.TrimRight(snapshot.ObjectPath, "\\")
				snapshotFile, err := os.Open(snapshotPath)
				if err != nil {
					return err
				}
				stopSnapshotClose := closeOnCancellation(ctx, snapshotFile)
				snapshotLength, err := GetDiskLength(snapshotPath)
				if err != nil {
					stopSnapshotClose()
					snapshotFile.Close()
					return err
				}
				if snapshotLength < 0 || uint64(snapshotLength) > partition.EndByte-partition.StartByte {
					stopSnapshotClose()
					snapshotFile.Close()
					return fmt.Errorf("VSS snapshot length %d exceeds partition length %d", snapshotLength, partition.EndByte-partition.StartByte)
				}
				if partition.EndByte != partition.StartByte+uint64(snapshotLength) {
					log.Printf("Harmless warning: VSS snapshot is smaller than partition ( probably FS is too ), will pad with zeros")
				}

				position := partition.StartByte
				block := make([]byte, pbscommon.PBS_FIXED_CHUNK_SIZE)
				for {
					bytesRead, readErr := snapshotFile.Read(block)
					if bytesRead > 0 {
						if position+uint64(bytesRead) > partition.EndByte {
							stopSnapshotClose()
							snapshotFile.Close()
							return fmt.Errorf("fatal: went outside partition space while reading VSS snapshot")
						}
						if emitErr := emitBuffered(block[:bytesRead]); emitErr != nil {
							stopSnapshotClose()
							snapshotFile.Close()
							return emitErr
						}
						position += uint64(bytesRead)
					}
					if readErr == io.EOF {
						break
					}
					if readErr != nil {
						stopSnapshotClose()
						snapshotFile.Close()
						return readErr
					}
					if bytesRead == 0 {
						stopSnapshotClose()
						snapshotFile.Close()
						return fmt.Errorf("failed to read VSS snapshot at %d", position)
					}
				}
				stopSnapshotClose()
				if err := snapshotFile.Close(); err != nil {
					return err
				}

				padding := partition.EndByte - position
				zeroBlock := make([]byte, pbscommon.PBS_FIXED_CHUNK_SIZE)
				for padding > 0 {
					log.Printf("Padding %d", padding)
					piece := zeroBlock[:min(uint64(len(zeroBlock)), padding)]
					if err := emitBuffered(piece); err != nil {
						return err
					}
					position += uint64(len(piece))
					padding -= uint64(len(piece))
				}
				if position != partition.EndByte {
					return fmt.Errorf("failed to read partition entirely %d/%d", position, partition.EndByte)
				}
			}

			if len(buffer) > 0 {
				if err := emit(append([]byte(nil), buffer...)); err != nil {
					return err
				}
			}
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			default:
				return nil
			}
		}

		return uploadWorker(client, qemuArchiveFileName(index), uint64(total), producer)
	})
}

func sysTraySetup() {
	//TODO
}
