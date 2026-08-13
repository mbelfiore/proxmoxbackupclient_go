package main

import (
	"fmt"
	"sort"
)

type DiskLayoutStyle string

const (
	DiskLayoutMBR DiskLayoutStyle = "mbr"
	DiskLayoutGPT DiskLayoutStyle = "gpt"
)

type DiskExtent struct {
	Start uint64
	End   uint64
	Data  bool
}

type DiskLayout struct {
	Size       uint64
	Style      DiskLayoutStyle
	Partitions []DiskExtent
}

// Segments validates a synthetic layout and returns complete, ordered disk
// coverage. Data=false segments are raw gaps; Data=true segments are partitions.
func (l DiskLayout) Segments() ([]DiskExtent, error) {
	if l.Style != DiskLayoutMBR && l.Style != DiskLayoutGPT {
		return nil, fmt.Errorf("unsupported disk layout style %q", l.Style)
	}
	parts := append([]DiskExtent(nil), l.Partitions...)
	sort.Slice(parts, func(i, j int) bool { return parts[i].Start < parts[j].Start })
	result := make([]DiskExtent, 0, len(parts)*2+1)
	pos := uint64(0)
	for _, p := range parts {
		if p.Start >= p.End {
			return nil, fmt.Errorf("invalid partition [%d,%d)", p.Start, p.End)
		}
		if p.End > l.Size {
			return nil, fmt.Errorf("partition [%d,%d) exceeds disk size %d", p.Start, p.End, l.Size)
		}
		if p.Start < pos {
			return nil, fmt.Errorf("overlapping partition at %d", p.Start)
		}
		if p.Start > pos {
			result = append(result, DiskExtent{Start: pos, End: p.Start})
		}
		p.Data = true
		result = append(result, p)
		pos = p.End
	}
	if pos < l.Size {
		result = append(result, DiskExtent{Start: pos, End: l.Size})
	}
	return result, nil
}
