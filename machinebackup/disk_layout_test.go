package main

import "testing"

func TestDiskLayoutSyntheticValidation(t *testing.T) {
	tests := []struct {
		name string
		in   DiskLayout
		want []DiskExtent
		err  bool
	}{
		{"ordered", DiskLayout{100, DiskLayoutGPT, []DiskExtent{{10, 20, false}, {30, 40, false}}}, []DiskExtent{{0, 10, false}, {10, 20, true}, {20, 30, false}, {30, 40, true}, {40, 100, false}}, false},
		{"out of order normalized", DiskLayout{100, DiskLayoutMBR, []DiskExtent{{30, 40, false}, {10, 20, false}}}, []DiskExtent{{0, 10, false}, {10, 20, true}, {20, 30, false}, {30, 40, true}, {40, 100, false}}, false},
		{"overlap", DiskLayout{100, DiskLayoutGPT, []DiskExtent{{10, 30, false}, {20, 40, false}}}, nil, true},
		{"past disk", DiskLayout{100, DiskLayoutMBR, []DiskExtent{{90, 101, false}}}, nil, true},
		{"gaps all positions", DiskLayout{100, DiskLayoutGPT, []DiskExtent{{10, 20, false}, {30, 90, false}}}, []DiskExtent{{0, 10, false}, {10, 20, true}, {20, 30, false}, {30, 90, true}, {90, 100, false}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.in.Segments()
			if (err != nil) != tc.err {
				t.Fatalf("error=%v, want error=%v", err, tc.err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("segments=%v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("segment %d=%v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestLayoutDistinguishesSourceCorruptionFromWriterOverrun(t *testing.T) {
	bad := DiskLayout{Size: 100, Style: DiskLayoutGPT, Partitions: []DiskExtent{{Start: 90, End: 110}}}
	if _, err := bad.Segments(); err == nil {
		t.Fatal("source layout beyond physical disk must be rejected before writer sizing")
	}
	good := DiskLayout{Size: 100, Style: DiskLayoutGPT, Partitions: []DiskExtent{{Start: 10, End: 90}}}
	segments, err := good.Segments()
	if err != nil {
		t.Fatal(err)
	}
	var emitted uint64
	for _, s := range segments {
		emitted += s.End - s.Start
	}
	if emitted != good.Size {
		t.Fatalf("writer plan emits %d logical bytes, want %d", emitted, good.Size)
	}
}
