package main

import (
	"strings"
	"testing"
)

func TestRenderQEMUConfigIncludesCompleteDiskReference(t *testing.T) {
	config, err := renderQEMUConfig(QEMUConfigTemplate{
		VMGenId: "11111111-1111-1111-1111-111111111111",
		VMID:    110,
		VMName:  "backprealtest",
		Disks: []BackupDisk{
			{Index: 0, Size: 268435456000},
		},
		OS:     "win11",
		SMBIOS: "22222222-2222-2222-2222-222222222222",
	})
	if err != nil {
		t.Fatalf("renderQEMUConfig() error = %v", err)
	}

	want := "sata0: local:110/vm-110-disk-0.raw,cache=writeback,discard=on,iothread=1,size=268435456000"
	if !strings.Contains(string(config), want) {
		t.Fatalf("qemu-server.conf missing complete disk reference %q:\n%s", want, config)
	}
	if !strings.Contains(string(config), "vmgenid: 11111111-1111-1111-1111-111111111111") {
		t.Fatalf("qemu-server.conf was truncated:\n%s", config)
	}
}

func TestRenderQEMUConfigUsesRootVMIDForEveryDisk(t *testing.T) {
	config, err := renderQEMUConfig(QEMUConfigTemplate{
		VMGenId: "11111111-1111-1111-1111-111111111111",
		VMID:    123,
		VMName:  "multi-disk",
		Disks: []BackupDisk{
			{Index: 0, Size: 1024},
			{Index: 2, Size: 2048},
		},
		OS:     "win11",
		SMBIOS: "22222222-2222-2222-2222-222222222222",
	})
	if err != nil {
		t.Fatalf("renderQEMUConfig() error = %v", err)
	}

	for _, want := range []string{
		"sata0: local:123/vm-123-disk-0.raw",
		"sata2: local:123/vm-123-disk-2.raw",
	} {
		if !strings.Contains(string(config), want) {
			t.Errorf("qemu-server.conf missing %q:\n%s", want, config)
		}
	}
}
