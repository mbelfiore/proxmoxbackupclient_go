package main

import (
	"regexp"
	"strings"
	"testing"
)

var proxmoxQMDumpMapPattern = regexp.MustCompile(`^#qmdump#map:(\S+):(\S+):(\S*):(\S*):$`)

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
	var sata0Lines []string
	for _, line := range strings.Split(configText, "\n") {
		if strings.HasPrefix(line, "sata0:") {
			sata0Lines = append(sata0Lines, line)
		}
	}
	if len(sata0Lines) != 1 {
		t.Fatalf("qemu-server.conf sata0 line count = %d, want 1:\n%s", len(sata0Lines), config)
	}
	if strings.Contains(configText, "iothread") {
		t.Fatalf("qemu-server.conf must not emit iothread for SATA disks:\n%s", config)
	}
	var mappingLines []string
	for _, line := range strings.Split(configText, "\n") {
		if strings.HasPrefix(line, "#qmdump#map:") {
			mappingLines = append(mappingLines, line)
		}
	}
	if len(mappingLines) != 1 {
		t.Fatalf("qemu-server.conf mapping count = %d, want 1:\n%s", len(mappingLines), config)
	}
	mapping := mappingLines[0]
	if want := "#qmdump#map:sata0:drive-sata0:local:raw:"; mapping != want {
		t.Fatalf("qemu-server.conf mapping = %q, want %q", mapping, want)
	}
	matches := proxmoxQMDumpMapPattern.FindStringSubmatch(mapping)
	if len(matches) != 5 {
		t.Fatalf("mapping %q does not match Proxmox qmdump syntax", mapping)
	}
	for index, want := range []string{"sata0", "drive-sata0", "local", "raw"} {
		if got := matches[index+1]; got != want {
			t.Errorf("mapping field %d = %q, want %q", index, got, want)
		}
	}
	if got, want := qemuArchiveFileName(0), "drive-sata0.img.fidx"; got != want {
		t.Fatalf("qemuArchiveFileName(0) = %q, want manifest archive %q", got, want)
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

	repeated, err := renderQEMUConfig(QEMUConfigTemplate{
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
		t.Fatalf("second renderQEMUConfig() error = %v", err)
	}
	if string(repeated) != configText {
		t.Fatalf("renderQEMUConfig() output is not deterministic:\nfirst:\n%s\nsecond:\n%s", config, repeated)
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
		"#qmdump#map:sata0:drive-sata0:local:raw:",
		"#qmdump#map:sata2:drive-sata2:local:raw:",
	} {
		if !strings.Contains(configText, want) {
			t.Errorf("qemu-server.conf missing %q:\n%s", want, config)
		}
	}
	if strings.Contains(configText, "iothread") {
		t.Fatalf("qemu-server.conf must not emit iothread for any SATA disk:\n%s", config)
	}
	if got, want := strings.Count(configText, "#qmdump#map:"), 2; got != want {
		t.Fatalf("qemu-server.conf mapping count = %d, want %d:\n%s", got, want, config)
	}
}

func TestRenderQEMUConfigRejectsDuplicateDiskIndex(t *testing.T) {
	_, err := renderQEMUConfig(QEMUConfigTemplate{
		Disks: []BackupDisk{
			{Index: 0, Size: 1024},
			{Index: 0, Size: 2048},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate disk index 0") {
		t.Fatalf("renderQEMUConfig() error = %v, want duplicate disk index error", err)
	}
}
