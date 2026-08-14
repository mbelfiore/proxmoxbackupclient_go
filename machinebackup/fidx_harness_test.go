package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
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
	beforeUpload            func([]byte)
	uploadResult            func([]byte) error
	uploadCalls             int
	activeUploads           int
	assignCalls             int
	closeCalls              int
}

type blockingReadCloser struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	close(r.started)
	<-r.closed
	return 0, errors.New("synthetic read interrupted by close")
}

func (r *blockingReadCloser) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

func newFixedIndexMock() *fixedIndexMock {
	return &fixedIndexMock{known: haxmap.New[string, bool](), chunks: map[string][]byte{}}
}
func (m *fixedIndexMock) GetKnownSha265FromFIDXContext(context.Context, string) (*haxmap.Map[string, bool], error) {
	return m.known, nil
}
func (m *fixedIndexMock) CreateFixedIndexContext(_ context.Context, r pbscommon.FixedIndexCreateReq) (uint64, error) {
	m.created = r
	return 7, nil
}
func (m *fixedIndexMock) UploadFixedCompressedChunkContext(ctx context.Context, _ uint64, d string, b []byte) error {
	m.mu.Lock()
	m.uploadCalls++
	m.activeUploads++
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.activeUploads--
		m.mu.Unlock()
	}()
	if m.beforeUpload != nil {
		m.beforeUpload(b)
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if m.uploadResult != nil {
		if err := m.uploadResult(b); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunks[d] = append([]byte(nil), b...)
	return nil
}
func (m *fixedIndexMock) AssignFixedChunksContext(_ context.Context, _ uint64, ds []string, os []uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assignCalls++
	for i := range ds {
		m.assignments = append(m.assignments, recordedAssignment{ds[i], os[i]})
	}
	return nil
}
func (m *fixedIndexMock) CloseFixedIndexContext(_ context.Context, _ uint64, c string, s, n uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeCalls++
	m.closedChecksum, m.closedSize, m.closedCount = c, s, n
	return nil
}

func waitWorkerResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("worker pipeline did not terminate")
		return nil
	}
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
func feedBlocks(data []byte) blockProducer {
	return func(_ context.Context, emit func([]byte) error) error {
		for len(data) > 0 {
			n := min(len(data), pbscommon.PBS_FIXED_CHUNK_SIZE)
			if err := emit(append([]byte(nil), data[:n]...)); err != nil {
				return err
			}
			data = data[n:]
		}
		return nil
	}
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
	started := make(chan byte, 3)
	release := map[byte]chan struct{}{1: make(chan struct{}), 2: make(chan struct{}), 3: make(chan struct{})}
	processed := make(chan uint64, 3)
	m.beforeUpload = func(b []byte) {
		started <- b[0]
		<-release[b[0]]
	}
	done := make(chan error, 1)
	go func() {
		done <- uploadWorkerWithProcessedHook(context.Background(), m, "disk.fidx", uint64(len(data)), feedBlocks(data), func(offset uint64) {
			processed <- offset
		})
	}()

	seen := map[byte]bool{}
	for range 3 {
		seen[<-started] = true
	}
	if !seen[1] || !seen[2] || !seen[3] {
		t.Fatalf("expected three concurrently started uploads, got %v", seen)
	}

	wantCompletion := []struct {
		block  byte
		offset uint64
	}{{3, 2 * pbscommon.PBS_FIXED_CHUNK_SIZE}, {2, pbscommon.PBS_FIXED_CHUNK_SIZE}, {1, 0}}
	for _, want := range wantCompletion {
		close(release[want.block])
		if got := <-processed; got != want.offset {
			t.Fatalf("processed offset=%d, want %d", got, want.offset)
		}
	}
	if err := <-done; err != nil {
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
	ordered := append([]recordedAssignment(nil), m.assignments...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].offset < ordered[j].offset })
	h := sha256.New()
	for _, assignment := range ordered {
		digest, err := hex.DecodeString(assignment.digest)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = h.Write(digest)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != m.closedChecksum {
		t.Fatalf("ordered index checksum=%s, want %s", got, m.closedChecksum)
	}
	t.Log("assignments are intentionally non-monotonic; PBS fixed indexes use each offset to write the digest directly at its calculated chunk position")
}

func TestFIDXWorkerSuccessLifecycle(t *testing.T) {
	blocks := make([][]byte, 8)
	data := make([]byte, 0, 8*pbscommon.PBS_FIXED_CHUNK_SIZE)
	for i := range blocks {
		blocks[i] = bytes.Repeat([]byte{byte(i + 1)}, pbscommon.PBS_FIXED_CHUNK_SIZE)
		data = append(data, blocks[i]...)
	}
	producerReturned := make(chan struct{})
	producer := func(_ context.Context, emit func([]byte) error) error {
		defer close(producerReturned)
		for _, block := range blocks {
			if err := emit(block); err != nil {
				return err
			}
		}
		return nil
	}
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	mock := newFixedIndexMock()
	mock.beforeUpload = func([]byte) {
		started <- struct{}{}
		<-release
	}
	done := make(chan error, 1)
	go func() { done <- uploadWorker(mock, "disk.fidx", uint64(len(data)), producer) }()
	for range 8 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("eight workers did not start concurrently")
		}
	}
	close(release)
	if err := waitWorkerResult(t, done); err != nil {
		t.Fatal(err)
	}
	select {
	case <-producerReturned:
	default:
		t.Fatal("producer remained active after successful return")
	}
	mock.mu.Lock()
	active := mock.activeUploads
	mock.mu.Unlock()
	if active != 0 {
		t.Fatalf("%d uploads remained active after successful return", active)
	}
	got, err := mock.reconstruct()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("successful concurrent pipeline changed reconstructed bytes")
	}
}

func TestFIDXUploadErrorsTerminatePipeline(t *testing.T) {
	for _, failingBlock := range []byte{1, 2, 3} {
		t.Run(fmt.Sprintf("block_%d", failingBlock), func(t *testing.T) {
			sentinel := fmt.Errorf("upload block %d failed", failingBlock)
			producerReturned := make(chan struct{})
			producer := func(ctx context.Context, emit func([]byte) error) error {
				defer close(producerReturned)
				for _, value := range []byte{1, 2, 3} {
					if err := emit([]byte{value}); err != nil {
						return err
					}
				}
				return nil
			}
			mock := newFixedIndexMock()
			mock.uploadResult = func(block []byte) error {
				if block[0] == failingBlock {
					return sentinel
				}
				return nil
			}
			done := make(chan error, 1)
			go func() { done <- uploadWorker(mock, "disk.fidx", 3, producer) }()
			err := waitWorkerResult(t, done)
			if !errors.Is(err, sentinel) {
				t.Fatalf("error=%v, want %v", err, sentinel)
			}
			select {
			case <-producerReturned:
			default:
				t.Fatal("producer remained active after upload error")
			}
			mock.mu.Lock()
			active, assignCalls, closeCalls := mock.activeUploads, mock.assignCalls, mock.closeCalls
			mock.mu.Unlock()
			if active != 0 {
				t.Fatalf("%d uploads remained active", active)
			}
			if assignCalls != 0 || closeCalls != 0 {
				t.Fatalf("pipeline continued after error: assign=%d close=%d", assignCalls, closeCalls)
			}
		})
	}
}

func TestFIDXCancellationStopsQueuedWork(t *testing.T) {
	const blockCount = 16
	gates := make(map[byte]chan struct{}, blockCount)
	for i := 1; i <= blockCount; i++ {
		gates[byte(i)] = make(chan struct{})
	}
	started := make(chan byte, 8)
	producerReturned := make(chan struct{})
	producer := func(ctx context.Context, emit func([]byte) error) error {
		defer close(producerReturned)
		for i := 1; i <= blockCount; i++ {
			if err := emit([]byte{byte(i)}); err != nil {
				return err
			}
		}
		return nil
	}

	sentinel := errors.New("forced upload failure")
	mock := newFixedIndexMock()
	mock.beforeUpload = func(block []byte) {
		started <- block[0]
		<-gates[block[0]]
	}
	mock.uploadResult = func(block []byte) error {
		if block[0] == 1 {
			return sentinel
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- uploadWorker(mock, "disk.fidx", blockCount, producer) }()

	startedBlocks := make([]byte, 0, 8)
	for range 8 {
		select {
		case block := <-started:
			startedBlocks = append(startedBlocks, block)
		case <-time.After(5 * time.Second):
			t.Fatal("eight workers did not begin concurrent uploads")
		}
	}
	foundFailingBlock := false
	for _, block := range startedBlocks {
		if block == 1 {
			foundFailingBlock = true
			break
		}
	}
	if !foundFailingBlock {
		t.Fatalf("failing block was not among initial workers: %v", startedBlocks)
	}
	close(gates[1])
	select {
	case <-producerReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("producer did not observe cancellation")
	}
	for _, block := range startedBlocks {
		if block != 1 {
			close(gates[block])
		}
	}
	if err := waitWorkerResult(t, done); !errors.Is(err, sentinel) {
		t.Fatalf("error=%v, want %v", err, sentinel)
	}
	mock.mu.Lock()
	uploadCalls, active, assignCalls, closeCalls := mock.uploadCalls, mock.activeUploads, mock.assignCalls, mock.closeCalls
	mock.mu.Unlock()
	if uploadCalls != 8 {
		t.Fatalf("upload calls=%d, want only the 8 already in flight", uploadCalls)
	}
	if active != 0 || assignCalls != 0 || closeCalls != 0 {
		t.Fatalf("pipeline survived cancellation: active=%d assign=%d close=%d", active, assignCalls, closeCalls)
	}
}

func TestFIDXProducerFailureIsReturned(t *testing.T) {
	sentinel := errors.New("producer read failed")
	producerReturned := make(chan struct{})
	producer := func(_ context.Context, emit func([]byte) error) error {
		defer close(producerReturned)
		if err := emit([]byte{1}); err != nil {
			return err
		}
		return sentinel
	}
	mock := newFixedIndexMock()
	done := make(chan error, 1)
	go func() { done <- uploadWorker(mock, "disk.fidx", 2, producer) }()
	if err := waitWorkerResult(t, done); !errors.Is(err, sentinel) {
		t.Fatalf("error=%v, want %v", err, sentinel)
	}
	select {
	case <-producerReturned:
	default:
		t.Fatal("failed producer remained active")
	}
	mock.mu.Lock()
	assignCalls, closeCalls := mock.assignCalls, mock.closeCalls
	mock.mu.Unlock()
	if assignCalls != 0 || closeCalls != 0 {
		t.Fatalf("pipeline finalized after producer failure: assign=%d close=%d", assignCalls, closeCalls)
	}
}

func TestFIDXProducerPanicBecomesError(t *testing.T) {
	mock := newFixedIndexMock()
	producer := func(context.Context, func([]byte) error) error {
		panic("synthetic producer panic")
	}
	done := make(chan error, 1)
	go func() { done <- uploadWorker(mock, "disk.fidx", 1, producer) }()
	err := waitWorkerResult(t, done)
	if err == nil || !strings.Contains(err.Error(), "synthetic producer panic") {
		t.Fatalf("panic error=%v", err)
	}
	mock.mu.Lock()
	assignCalls, closeCalls := mock.assignCalls, mock.closeCalls
	mock.mu.Unlock()
	if assignCalls != 0 || closeCalls != 0 {
		t.Fatalf("pipeline finalized after producer panic: assign=%d close=%d", assignCalls, closeCalls)
	}
}

func TestFIDXWriterOverrunReturnsError(t *testing.T) {
	mock := newFixedIndexMock()
	producer := func(_ context.Context, emit func([]byte) error) error {
		return emit([]byte{1, 2})
	}
	done := make(chan error, 1)
	go func() { done <- uploadWorker(mock, "disk.fidx", 1, producer) }()
	err := waitWorkerResult(t, done)
	if err == nil || !strings.Contains(err.Error(), "more data than specified size") {
		t.Fatalf("overrun error=%v", err)
	}
	mock.mu.Lock()
	assignCalls, closeCalls := mock.assignCalls, mock.closeCalls
	mock.mu.Unlock()
	if assignCalls != 0 || closeCalls != 0 {
		t.Fatalf("pipeline finalized after overrun: assign=%d close=%d", assignCalls, closeCalls)
	}
}

func TestFIDXPipelineCancellationInterruptsPBSUpload(t *testing.T) {
	failingDigest := sha256.Sum256([]byte{1})
	failingDigestHex := hex.EncodeToString(failingDigest[:])
	uploadStarted := make(chan string, 2)
	releaseFailure := make(chan struct{})
	requestCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/previous":
			http.Error(w, "no previous index", http.StatusNotFound)
		case r.URL.Path == "/fixed_index" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"data":7}`))
		case r.URL.Path == "/fixed_chunk":
			_, _ = io.Copy(io.Discard, r.Body)
			digest := r.URL.Query().Get("digest")
			uploadStarted <- digest
			if digest == failingDigestHex {
				<-releaseFailure
				http.Error(w, "forced worker failure", http.StatusInternalServerError)
				return
			}
			<-r.Context().Done()
			close(requestCanceled)
		default:
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	client := &pbscommon.PBSClient{
		BaseURL:         server.URL,
		Client:          *server.Client(),
		WritersManifest: map[uint64]int{},
	}
	producer := func(_ context.Context, emit func([]byte) error) error {
		if err := emit([]byte{1}); err != nil {
			return err
		}
		return emit([]byte{2})
	}
	done := make(chan error, 1)
	go func() {
		done <- uploadWorker(client, "disk.fidx", 2, producer)
	}()
	started := map[string]bool{}
	for range 2 {
		select {
		case digest := <-uploadStarted:
			started[digest] = true
		case <-time.After(5 * time.Second):
			t.Fatal("two concurrent PBS uploads did not start")
		}
	}
	if !started[failingDigestHex] {
		t.Fatal("forced-failure upload did not start")
	}
	close(releaseFailure)
	if err := waitWorkerResult(t, done); err == nil {
		t.Fatal("failed PBS upload returned nil")
	}
	select {
	case <-requestCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("PBS request did not observe pipeline cancellation")
	}
}

func TestFIDXProducerCancellationClosesBlockingSource(t *testing.T) {
	source := &blockingReadCloser{started: make(chan struct{}), closed: make(chan struct{})}
	producer := func(ctx context.Context, _ func([]byte) error) error {
		stopClose := closeOnCancellation(ctx, source)
		defer stopClose()
		_, err := source.Read(make([]byte, 1))
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- uploadWorkerWithProcessedHook(ctx, newFixedIndexMock(), "disk.fidx", 1, producer, nil)
	}()
	select {
	case <-source.started:
	case <-time.After(5 * time.Second):
		t.Fatal("synthetic blocking read did not start")
	}
	cancel()
	if err := waitWorkerResult(t, done); err == nil {
		t.Fatal("canceled blocking producer returned nil")
	}
	select {
	case <-source.closed:
	default:
		t.Fatal("cancellation did not close the active source")
	}
}
