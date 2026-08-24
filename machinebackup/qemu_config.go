package main

import (
	"bytes"
	"fmt"
	"text/template"
)

type QEMUConfigTemplate struct {
	VMGenId string
	VMID    int64
	VMName  string
	Disks   []BackupDisk
	OS      string
	SMBIOS  string
}

func qemuArchiveDeviceName(index int) string {
	return fmt.Sprintf("drive-sata%d", index)
}

func qemuArchiveFileName(index int) string {
	return qemuArchiveDeviceName(index) + ".img.fidx"
}

const qemuConfigTemplate = `boot: order=sata0
cores: 4
machine: q35
memory: 2048
name: {{.VMName}}
numa: 0
onboot: 0
ostype: {{.OS}}
scsihw: virtio-scsi-single
smbios1: uuid={{.SMBIOS}}
sockets: 1
{{range .Disks}}
sata{{.Index}}: local:{{$.VMID}}/vm-{{$.VMID}}-disk-{{.Index}}.raw,cache=writeback,discard=on,size={{.Size}}
#qmdump#map:sata{{.Index}}:{{archiveDevice .Index}}:local:raw:
{{end}}
vmgenid: {{.VMGenId}}
`

func renderQEMUConfig(config QEMUConfigTemplate) ([]byte, error) {
	seenDisks := make(map[int]struct{}, len(config.Disks))
	for _, disk := range config.Disks {
		if disk.Index < 0 {
			return nil, fmt.Errorf("render qemu-server.conf: invalid disk index %d", disk.Index)
		}
		if _, exists := seenDisks[disk.Index]; exists {
			return nil, fmt.Errorf("render qemu-server.conf: duplicate disk index %d", disk.Index)
		}
		seenDisks[disk.Index] = struct{}{}
	}

	tmpl, err := template.New("qemuconfig").Funcs(template.FuncMap{
		"archiveDevice": qemuArchiveDeviceName,
	}).Parse(qemuConfigTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse qemu-server.conf template: %w", err)
	}

	var output bytes.Buffer
	if err := tmpl.Execute(&output, config); err != nil {
		return nil, fmt.Errorf("render qemu-server.conf: %w", err)
	}
	return output.Bytes(), nil
}
