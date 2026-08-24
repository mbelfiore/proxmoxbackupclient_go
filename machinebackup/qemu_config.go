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
sata{{.Index}}: local:{{$.VMID}}/vm-{{$.VMID}}-disk-{{.Index}}.raw,cache=writeback,discard=on,iothread=1,size={{.Size}}
{{end}}
vmgenid: {{.VMGenId}}
`

func renderQEMUConfig(config QEMUConfigTemplate) ([]byte, error) {
	tmpl, err := template.New("qemuconfig").Parse(qemuConfigTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse qemu-server.conf template: %w", err)
	}

	var output bytes.Buffer
	if err := tmpl.Execute(&output, config); err != nil {
		return nil, fmt.Errorf("render qemu-server.conf: %w", err)
	}
	return output.Bytes(), nil
}
