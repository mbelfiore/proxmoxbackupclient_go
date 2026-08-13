package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/alphadose/haxmap"
	"pbscommon"
)

type recordedAssignment struct {
	digest string
	offset uint64
}
type fixedIndexMock struct {
	mu                      sync.Mutex
	known                   *haxmap.Map[string, bool]
	chunks                  map[string][]byte
	assignments             []recordedAssignment
	created                 pbscommon.FixedIndexCreateReq
	closedChecksum          string
	closedSize, closedCount uint64
	delay                   func([]byte) time.Duration
}

func newFixedIndexMock() *fixedIndexMock {
	return &fixedIndexMock{known: haxmap.New[string, bool](), chunks: map[string][]byte{}}
}
func (m *fixedIndexMock) GetKnownSha265FromFIDX(string) (*haxmap.Map[string, bool], error) {
	return m.known, nil
}
func (m *fixedIndexMock) CreateFixedIndex(r pbscommon.FixedIndexCreateReq) (uint64, error) {
	m.created = r
	return 7, nil
}
func (m *fixedIndexMock) UploadFixedCompressedChunk(_ uint64, d string, b []byte) error {
	if m.delay != nil {
		time.Sleep(m.delay(b))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunks[d] = append([]byte(nil), b...)
	return nil
}
func (m *fixedIndexMock) AssignFixedChunks(_ uint64, ds []string, os []uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range ds {
		m.assignments = append(m.assignments, recordedAssignment{ds[i], os[i]})
	}
	return nil
}
func (m *fixedIndexMock) CloseFixedIndex(_ uint64, c string, s, n uint64) error {
	m.closedChecksum, m.closedSize, m.closedCount = c, s, n
	return nil
}
func (m *fixedIndexMock) reconstruct() ([]byte, error) {
	a := append([]recordedAssignment(nil), m.assignments...)
	sort.Slice(a, func(i, j int) bool { return a[i].offset < a[j].offset })
	out := make([]byte, 0, m.closedSize)
	var pos uint64
	for _, x := range a {
		if x.offset != pos {
			return nil, errors.New("non-contiguous offsets")
		}
		b, ok := m.chunks[x.digest]
		if !ok {
			return nil, errors.New("missing chunk")
		}
		out = append(out, b...)
		pos += uint64(len(b))
	}
	if pos != m.closedSize {
		return nil, errors.New("wrong reconstructed size")
	}
	return out, nil
}
func feedBlocks(data []byte) chan []byte {
	ch := make(chan []byte)
	go func() {
		defer close(ch)
		for len(data) > 0 {
			n := min(len(data), pbscommon.PBS_FIXED_CHUNK_SIZE)
			ch <- append([]byte(nil), data[:n]...)
			data = data[n:]
		}
	}()
	return ch
}
func deterministicData(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i*131 + 17) % 251)
	}
	return b
}

func TestFIDXWriterReconstructsByteForByte(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"zero bytes", nil}, {"one byte", []byte{1}}, {"less than chunk", deterministicData(12345)},
		{"exact chunk", deterministicData(pbscommon.PBS_FIXED_CHUNK_SIZE)},
		{"chunk plus one", deterministicData(pbscommon.PBS_FIXED_CHUNK_SIZE + 1)},
		{"multiple chunks", deterministicData(3*pbscommon.PBS_FIXED_CHUNK_SIZE + 99)},
		{"all zero", make([]byte, 2*pbscommon.PBS_FIXED_CHUNK_SIZE)},
	}
	dup := bytes.Repeat([]byte{0x5a}, pbscommon.PBS_FIXED_CHUNK_SIZE)
	cases = append(cases, struct {
		name string
		data []byte
	}{"duplicate chunks", append(append([]byte{}, dup...), dup...)})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newFixedIndexMock()
			if err := uploadWorker(m, "disk.fidx", uint64(len(tc.data)), feedBlocks(tc.data)); err != nil {
				t.Fatal(err)
			}
			got, err := m.reconstruct()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.data) {
				t.Fatal("reconstruction differs byte-for-byte")
			}
			if m.closedSize != uint64(len(tc.data)) {
				t.Fatalf("closed size=%d", m.closedSize)
			}
			ordered := append([]recordedAssignment(nil), m.assignments...)
			sort.Slice(ordered, func(i, j int) bool { return ordered[i].offset < ordered[j].offset })
			h := sha256.New()
			for _, a := range ordered {
				d, _ := hex.DecodeString(a.digest)
				h.Write(d)
			}
			if hex.EncodeToString(h.Sum(nil)) != m.closedChecksum {
				t.Fatal("ordered index checksum mismatch")
			}
		})
	}
}

func TestFIDXOutOfOrderCompletionCharacterizesOffsetSemantics(t *testing.T) {
	data := append(bytes.Repeat([]byte{1}, pbscommon.PBS_FIXED_CHUNK_SIZE), bytes.Repeat([]byte{2}, pbscommon.PBS_FIXED_CHUNK_SIZE)...)
	data = append(data, bytes.Repeat([]byte{3}, pbscommon.PBS_FIXED_CHUNK_SIZE)...)
	m := newFixedIndexMock()
	m.delay = func(b []byte) time.Duration { return time.Duration(4-int(b[0])) * 20 * time.Millisecond }
	if err := uploadWorker(m, "disk.fidx", uint64(len(data)), feedBlocks(data)); err != nil {
		t.Fatal(err)
	}
	monotonic := true
	for i := 1; i < len(m.assignments); i++ {
		if m.assignments[i].offset < m.assignments[i-1].offset {
			monotonic = false
		}
	}
	if monotonic {
		t.Fatal("test failed to force out-of-order worker completion")
	}
	got, err := m.reconstruct()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("offset-addressed reconstruction failed: %v", err)
	}
	t.Log("assignments are non-monotonic, while offset-addressed reconstruction and ordered checksum remain valid; PBS acceptance still requires authoritative protocol confirmation")
}
