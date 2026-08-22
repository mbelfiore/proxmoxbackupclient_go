//go:build windows
// +build windows

package snapshot

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	vss "github.com/st-matskevich/go-vss"
)

const windowsVSSOperationTimeout = 180 * 1000
const windowsVSSFreeSnapshotPropertiesProcedure = "VssFreeSnapshotPropertiesInternal"

type windowsVSSSnapshotPropertiesCleanup func(*vss.VssSnapshotProperties)
type windowsVSSSnapshotPropertiesCleanupResolver func() (windowsVSSSnapshotPropertiesCleanup, error)

type windowsVSSSnapshotPropertiesReader interface {
	GetSnapshotId() string
	GetSnapshotSetId() string
	GetSnapshotDeviceObject() string
}

type windowsVSSSnapshotSetSession struct {
	components             *vss.IVssBackupComponents
	snapshotIDs            map[string]ole.GUID
	freeSnapshotProperties windowsVSSSnapshotPropertiesCleanup
}

func resolveWindowsVSSSnapshotPropertiesCleanup() (windowsVSSSnapshotPropertiesCleanup, error) {
	procedure := syscall.NewLazyDLL("VssApi.dll").NewProc(windowsVSSFreeSnapshotPropertiesProcedure)
	if err := procedure.Find(); err != nil {
		return nil, fmt.Errorf("resolve VSS snapshot properties cleanup procedure %q: %w", windowsVSSFreeSnapshotPropertiesProcedure, err)
	}
	return func(properties *vss.VssSnapshotProperties) {
		procedure.Call(uintptr(unsafe.Pointer(properties)))
	}, nil
}

func requireWindowsVSSSnapshotPropertiesCleanup(resolve windowsVSSSnapshotPropertiesCleanupResolver) (windowsVSSSnapshotPropertiesCleanup, error) {
	if resolve == nil {
		return nil, fmt.Errorf("VSS snapshot properties cleanup resolver is nil")
	}
	cleanup, err := resolve()
	if err != nil {
		return nil, fmt.Errorf("VSS snapshot properties cleanup is unavailable: %w", err)
	}
	if cleanup == nil {
		return nil, fmt.Errorf("VSS snapshot properties cleanup resolver returned nil")
	}
	return cleanup, nil
}

func copyAndReleaseWindowsVSSSnapshotProperties(properties windowsVSSSnapshotPropertiesReader, cleanup func()) (vssSnapshotProperties, error) {
	if cleanup == nil {
		return vssSnapshotProperties{}, fmt.Errorf("VSS snapshot properties cleanup is nil")
	}
	defer cleanup()
	if properties == nil {
		return vssSnapshotProperties{}, fmt.Errorf("VSS snapshot properties are nil")
	}

	deviceObjectPath := properties.GetSnapshotDeviceObject()
	if deviceObjectPath != "" && !strings.HasSuffix(deviceObjectPath, `\`) {
		deviceObjectPath += `\`
	}
	return vssSnapshotProperties{
		SnapshotID:       properties.GetSnapshotId(),
		SnapshotSetID:    properties.GetSnapshotSetId(),
		DeviceObjectPath: deviceObjectPath,
	}, nil
}

func waitForWindowsVSSOperation(operation string, async *vss.IVssAsync) error {
	if async == nil {
		return fmt.Errorf("%s returned a nil IVssAsync", operation)
	}
	defer func() {
		_ = async.Cancel()
		async.Release()
	}()

	if err := async.Wait(windowsVSSOperationTimeout); err != nil {
		return fmt.Errorf("%s wait: %w", operation, err)
	}
	status, err := async.QueryStatus()
	if err != nil {
		return fmt.Errorf("%s query status: %w", operation, err)
	}
	switch status {
	case vss.VSS_S_ASYNC_FINISHED:
		return nil
	case vss.VSS_S_ASYNC_CANCELLED:
		return fmt.Errorf("%s was cancelled", operation)
	case vss.VSS_S_ASYNC_PENDING:
		return fmt.Errorf("%s is still pending", operation)
	default:
		return fmt.Errorf("%s returned status 0x%x", operation, status)
	}
}

func newWindowsVSSSnapshotSetSession(sources []string) (*windowsVSSSnapshotSetSession, error) {
	freeSnapshotProperties, err := requireWindowsVSSSnapshotPropertiesCleanup(resolveWindowsVSSSnapshotPropertiesCleanup)
	if err != nil {
		return nil, err
	}
	components, err := vss.LoadAndInitVSS()
	if err != nil {
		return nil, fmt.Errorf("initialize VSS backup components: %w", err)
	}
	session := &windowsVSSSnapshotSetSession{
		components:             components,
		snapshotIDs:            make(map[string]ole.GUID, len(sources)),
		freeSnapshotProperties: freeSnapshotProperties,
	}
	succeeded := false
	defer func() {
		if !succeeded {
			session.Release()
		}
	}()

	if err := components.SetContext(vss.VSS_CTX_BACKUP); err != nil {
		return nil, fmt.Errorf("set VSS backup context: %w", err)
	}
	if err := components.SetBackupState(false, false, vss.VSS_BT_COPY, false); err != nil {
		return nil, fmt.Errorf("set VSS backup state: %w", err)
	}
	async, err := components.GatherWriterMetadata()
	if err != nil {
		return nil, fmt.Errorf("gather VSS writer metadata: %w", err)
	}
	if err := waitForWindowsVSSOperation("gather VSS writer metadata", async); err != nil {
		return nil, err
	}
	for _, source := range sources {
		supported, err := components.IsVolumeSupported(source)
		if err != nil {
			return nil, fmt.Errorf("check VSS support for source %q: %w", source, err)
		}
		if !supported {
			return nil, fmt.Errorf("VSS snapshots are not supported for source %q", source)
		}
	}

	succeeded = true
	return session, nil
}

func (s *windowsVSSSnapshotSetSession) StartSnapshotSet() (string, error) {
	var snapshotSetID ole.GUID
	if err := s.components.StartSnapshotSet(&snapshotSetID); err != nil {
		return "", err
	}
	return snapshotSetID.String(), nil
}

func (s *windowsVSSSnapshotSetSession) AddToSnapshotSet(source string) (string, error) {
	var snapshotID ole.GUID
	if err := s.components.AddToSnapshotSet(source, &snapshotID); err != nil {
		return "", err
	}
	id := snapshotID.String()
	s.snapshotIDs[id] = snapshotID
	return id, nil
}

func (s *windowsVSSSnapshotSetSession) PrepareForBackup() error {
	async, err := s.components.PrepareForBackup()
	if err != nil {
		return err
	}
	return waitForWindowsVSSOperation("prepare VSS backup", async)
}

func (s *windowsVSSSnapshotSetSession) DoSnapshotSet() error {
	async, err := s.components.DoSnapshotSet()
	if err != nil {
		return err
	}
	return waitForWindowsVSSOperation("create VSS snapshot set", async)
}

func (s *windowsVSSSnapshotSetSession) GetSnapshotProperties(snapshotID string) (vssSnapshotProperties, error) {
	if s.freeSnapshotProperties == nil {
		return vssSnapshotProperties{}, fmt.Errorf("VSS snapshot properties cleanup is unavailable")
	}
	id, exists := s.snapshotIDs[snapshotID]
	if !exists {
		return vssSnapshotProperties{}, fmt.Errorf("unknown VSS snapshot ID %q", snapshotID)
	}
	properties := vss.VssSnapshotProperties{}
	if err := s.components.GetSnapshotProperties(id, &properties); err != nil {
		return vssSnapshotProperties{}, err
	}
	return copyAndReleaseWindowsVSSSnapshotProperties(&properties, func() {
		s.freeSnapshotProperties(&properties)
	})
}

func (s *windowsVSSSnapshotSetSession) BackupComplete() error {
	async, err := s.components.BackupComplete()
	if err != nil {
		return err
	}
	return waitForWindowsVSSOperation("complete VSS backup", async)
}

func (s *windowsVSSSnapshotSetSession) AbortBackup() error {
	return s.components.AbortBackup()
}

func (s *windowsVSSSnapshotSetSession) Release() {
	if s.components == nil {
		return
	}
	s.components.Release()
	s.components = nil
}

var _ vssSnapshotSetSession = (*windowsVSSSnapshotSetSession)(nil)

func withWindowsVSSSnapshotSetSession(sources []string, run func(vssSnapshotSetSession) error) error {
	if run == nil {
		return fmt.Errorf("VSS snapshot-set session callback is nil")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitialize(0); err != nil {
		return fmt.Errorf("initialize COM for VSS: %w", err)
	}
	defer ole.CoUninitialize()

	session, err := newWindowsVSSSnapshotSetSession(sources)
	if err != nil {
		return err
	}
	return run(session)
}
