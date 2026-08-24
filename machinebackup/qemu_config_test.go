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

	configText := string(config)
	if configText == "" {
		t.Fatal("qemu-server.conf is empty")
	}

	want := "sata0: local:110/vm-110-disk-0.raw,cache=writeback,discard=on,size=268435456000"
	if !strings.Contains(configText, want) {
		t.Fatalf("qemu-server.conf missing complete disk reference %q:\n%s", want, config)
	}
	if got := strings.Count(configText, "sata0:"); got != 1 {
		t.Fatalf("qemu-server.conf sata0 line count = %d, want 1:\n%s", got, config)
	}
	if strings.Contains(configText, "iothread") {
		t.Fatalf("qemu-server.conf must not emit iothread for SATA disks:\n%s", config)
	}
	for _, required := range []string{
		"boot: order=sata0",
		"machine: q35",
		"ostype: win11",
		"cache=writeback",
		"discard=on",
		"size=268435456000",
	} {
		if !strings.Contains(configText, required) {
			t.Errorf("qemu-server.conf missing %q:\n%s", required, config)
		}
	}
	if !strings.Contains(configText, "vmgenid: 11111111-1111-1111-1111-111111111111") {
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

	configText := string(config)
	for _, want := range []string{
		"sata0: local:123/vm-123-disk-0.raw",
		"sata2: local:123/vm-123-disk-2.raw",
	} {
		if !strings.Contains(configText, want) {
			t.Errorf("qemu-server.conf missing %q:\n%s", want, config)
		}
	}
	if strings.Contains(configText, "iothread") {
		t.Fatalf("qemu-server.conf must not emit iothread for any SATA disk:\n%s", config)
	}
}
