package pbscommon

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func waitTestSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}

func TestPBSUpgradeCancellationClosesOwnedConnection(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = serverConn.Close() })
	requestRead := make(chan struct{})
	peerClosed := make(chan struct{})
	go func() {
		reader := bufio.NewReader(serverConn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(peerClosed)
				return
			}
			if line == "\r\n" {
				close(requestRead)
				break
			}
		}
		_, _ = reader.ReadByte()
		close(peerClosed)
	}()

	pbs := &PBSClient{Manifest: BackupManifest{BackupTime: 1, BackupType: "host", BackupID: "test"}}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := pbs.upgradePBSConnection(ctx, clientConn, false)
		result <- err
	}()
	waitTestSignal(t, requestRead, "upgrade request was not written")
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("upgrade error=%v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked upgrade did not return after cancellation")
	}
	waitTestSignal(t, peerClosed, "owned upgrade connection remained open")
}

func TestPBSUpgradeCancellationBeforeStart(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pbs := &PBSClient{}
	conn, err := pbs.upgradePBSConnection(ctx, clientConn, false)
	if conn != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("conn=%v error=%v, want nil context cancellation", conn, err)
	}
	peerClosed := make(chan struct{})
	go func() {
		_, _ = serverConn.Read(make([]byte, 1))
		close(peerClosed)
	}()
	waitTestSignal(t, peerClosed, "pre-canceled upgrade connection remained open")
}

func TestPBSTLSHandshakeCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	helloRead := make(chan struct{})
	peerClosed := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			close(peerClosed)
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		if _, readErr := conn.Read(buf); readErr == nil {
			close(helloRead)
			_, _ = conn.Read(buf)
		}
		close(peerClosed)
	}()

	pbs := &PBSClient{TLSConfig: tls.Config{InsecureSkipVerify: true}}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, dialErr := pbs.dialPBSTLS(ctx, "tcp", listener.Addr().String())
		result <- dialErr
	}()
	waitTestSignal(t, helloRead, "TLS ClientHello was not received")
	cancel()
	select {
	case dialErr := <-result:
		if !errors.Is(dialErr, context.Canceled) {
			t.Fatalf("TLS dial error=%v, want context cancellation", dialErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TLS handshake did not return after cancellation")
	}
	waitTestSignal(t, peerClosed, "TLS handshake connection remained open")
}

func TestPBSUpgradeSuccessAndServerError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
		wantErr  bool
	}{
		{name: "success", response: "HTTP/1.1 101 Switching Protocols\r\n\r\n"},
		{name: "server error", response: "HTTP/1.1 401 Unauthorized\r\n\r\n", wantErr: true},
		{name: "malformed status line", response: "MALFORMED\r\n\r\n", wantErr: true},
		{name: "invalid status code", response: "HTTP/1.1 nope Invalid\r\n\r\n", wantErr: true},
		{name: "empty response", response: "", wantErr: true},
		{name: "truncated response", response: "HTTP/1.1 101 Switching", wantErr: true},
		{name: "oversized header", response: "HTTP/1.1 101 Switching Protocols\r\nX-Fill: " + strings.Repeat("x", maxPBSUpgradeHeaderSize), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			go func() {
				defer serverConn.Close()
				reader := bufio.NewReader(serverConn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil || line == "\r\n" {
						break
					}
				}
				_, _ = io.WriteString(serverConn, tc.response)
			}()
			pbs := &PBSClient{Manifest: BackupManifest{BackupTime: 1, BackupType: "host", BackupID: "test"}}
			conn, err := pbs.upgradePBSConnection(context.Background(), clientConn, false)
			if tc.wantErr {
				if err == nil {
					t.Fatal("server error produced nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()
		})
	}
}

type shortWriteConn struct {
	net.Conn
}

func (c shortWriteConn) Write(data []byte) (int, error) {
	return len(data) - 1, nil
}

func TestPBSUpgradeRejectsShortWrite(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	pbs := &PBSClient{Manifest: BackupManifest{BackupTime: 1, BackupType: "host", BackupID: "test"}}
	conn, err := pbs.upgradePBSConnection(context.Background(), shortWriteConn{Conn: clientConn}, false)
	if conn != nil || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("conn=%v error=%v, want nil io.ErrShortWrite", conn, err)
	}
}

func TestFixedChunkRequestObservesContextCancellation(t *testing.T) {
	started := make(chan struct{})
	requestCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(requestCanceled)
	}))
	t.Cleanup(server.Close)

	client := &PBSClient{BaseURL: server.URL, Client: *server.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- client.UploadFixedCompressedChunkContext(ctx, 1, strings.Repeat("0", 64), []byte{1})
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fixed chunk request did not start")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled fixed chunk request returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fixed chunk request did not return after cancellation")
	}
	select {
	case <-requestCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP server did not observe request context cancellation")
	}
}

func protocolClient(t *testing.T, status int, body any) (*PBSClient, *[]string) {
	t.Helper()
	calls := []string{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
	t.Cleanup(s.Close)
	return &PBSClient{BaseURL: s.URL, Client: *s.Client(), WritersManifest: map[uint64]int{1: 0}, Manifest: BackupManifest{Files: []File{{Filename: "disk.fidx"}}}}, &calls
}

func TestPBSProtocolMockRecordsSuccessfulPipeline(t *testing.T) {
	p, calls := protocolClient(t, http.StatusOK, map[string]int{"data": 1})
	wid, err := p.CreateFixedIndex(FixedIndexCreateReq{ArchiveName: "disk.fidx", Size: 1})
	if err != nil || wid != 1 {
		t.Fatalf("create: wid=%d err=%v", wid, err)
	}
	if err = p.UploadFixedCompressedChunk(wid, "00", []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err = p.AssignFixedChunks(wid, []string{"00"}, []uint64{0}); err != nil {
		t.Fatal(err)
	}
	if err = p.CloseFixedIndex(wid, "csum", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err = p.UploadBlob("meta.blob", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err = p.UploadManifest(); err != nil {
		t.Fatal(err)
	}
	if err = p.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 7 {
		t.Fatalf("recorded calls=%v", *calls)
	}
}

func TestNonOKStatusReturnsError(t *testing.T) {
	statuses := []int{400, 401, 403, 500}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			tests := []struct {
				name   string
				method string
				path   string
				call   func(*PBSClient) error
			}{
				{"UploadBlob", http.MethodPost, "/blob", func(p *PBSClient) error { return p.UploadBlob("x.blob", []byte("x")) }},
				{"UploadManifest", http.MethodPost, "/blob", func(p *PBSClient) error { return p.UploadManifest() }},
				{"Finish", http.MethodPost, "/finish", func(p *PBSClient) error { return p.Finish() }},
				{"AssignFixedChunks", http.MethodPut, "/fixed_index", func(p *PBSClient) error { return p.AssignFixedChunks(1, []string{"00"}, []uint64{0}) }},
				{"CloseFixedIndex", http.MethodPost, "/fixed_close", func(p *PBSClient) error { return p.CloseFixedIndex(1, "x", 1, 1) }},
				{"CreateFixedIndex", http.MethodPost, "/fixed_index", func(p *PBSClient) error {
					_, err := p.CreateFixedIndex(FixedIndexCreateReq{ArchiveName: "disk.fidx", Size: 1})
					return err
				}},
				{"UploadFixedCompressedChunk", http.MethodPost, "/fixed_chunk", func(p *PBSClient) error { return p.UploadFixedCompressedChunk(1, "00", []byte{1}) }},
			}
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					p, _ := protocolClient(t, status, map[string]string{"error": "forced"})
					err := tc.call(p)
					if err == nil {
						t.Fatalf("%s returned nil for HTTP %d", tc.name, status)
					}
					for _, want := range []string{tc.method, tc.path, http.StatusText(status), "forced"} {
						if !strings.Contains(err.Error(), want) {
							t.Errorf("error %q does not contain %q", err, want)
						}
					}
				})
			}
		})
	}
}

func selfSignedDER(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "wrong-cert"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
func TestTLSFingerprintPolicy(t *testing.T) {
	der := selfSignedDER(t)
	digest := sha256.Sum256(der)
	plain := hex.EncodeToString(digest[:])
	colonUpper := strings.ToUpper(strings.Join(splitEvery(plain, 2), ":"))

	for _, tc := range []struct {
		name        string
		fingerprint string
		insecure    bool
		wantErr     bool
	}{
		{"correct", plain, false, false},
		{"normalized equivalent", colonUpper, false, false},
		{"wrong", strings.Repeat("00", sha256.Size), false, true},
		{"insecure does not bypass pin", strings.Repeat("00", sha256.Size), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &PBSClient{Insecure: tc.insecure, CertFingerPrint: tc.fingerprint}
			p.Connect(false, "host")
			if !p.TLSConfig.InsecureSkipVerify {
				t.Fatal("certificate pinning must replace CA validation to support self-signed certificates")
			}
			if p.TLSConfig.VerifyPeerCertificate == nil {
				t.Fatal("fingerprint requires verification callback")
			}
			err := p.TLSConfig.VerifyPeerCertificate([][]byte{der}, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("verification error=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestTLSWithoutFingerprint(t *testing.T) {
	for _, tc := range []struct {
		name     string
		insecure bool
	}{
		{"normal CA validation", false},
		{"explicit insecure", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &PBSClient{Insecure: tc.insecure}
			p.Connect(false, "host")
			if p.TLSConfig.InsecureSkipVerify != tc.insecure {
				t.Fatalf("InsecureSkipVerify=%v, want %v", p.TLSConfig.InsecureSkipVerify, tc.insecure)
			}
			if p.TLSConfig.VerifyPeerCertificate != nil {
				t.Fatal("no fingerprint must not install pin callback")
			}
		})
	}
}

func splitEvery(s string, width int) []string {
	parts := make([]string, 0, len(s)/width)
	for len(s) > 0 {
		parts = append(parts, s[:width])
		s = s[width:]
	}
	return parts
}

func TestCriticalServerFailurePreventsWorkflowSuccess(t *testing.T) {
	calls := make([]string, 0)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if r.URL.Path == "/fixed_close" {
			http.Error(w, "close failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		if r.URL.Path == "/fixed_index" && r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"data":1}`))
		}
	}))
	defer s.Close()
	p := &PBSClient{BaseURL: s.URL, Client: *s.Client(), WritersManifest: make(map[uint64]int)}
	workflow := func() error {
		wid, err := p.CreateFixedIndex(FixedIndexCreateReq{ArchiveName: "disk.fidx", Size: 1})
		if err != nil {
			return err
		}
		if err = p.UploadFixedCompressedChunk(wid, "00", []byte{1}); err != nil {
			return err
		}
		if err = p.AssignFixedChunks(wid, []string{"00"}, []uint64{0}); err != nil {
			return err
		}
		if err = p.CloseFixedIndex(wid, "checksum", 1, 1); err != nil {
			return err
		}
		if err = p.UploadManifest(); err != nil {
			return err
		}
		return p.Finish()
	}
	if err := workflow(); err == nil {
		t.Fatal("critical close failure was reported as workflow success")
	}
	for _, path := range calls {
		if path == "/blob" || path == "/finish" {
			t.Fatalf("workflow continued to %s after critical failure", path)
		}
	}
}

type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

type trackingTransport struct {
	status int
	body   *closeTrackingBody
}

func (t *trackingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.status,
		Status:     fmt.Sprintf("%d %s", t.status, http.StatusText(t.status)),
		Header:     make(http.Header),
		Body:       t.body,
	}, nil
}

func TestPBSResponseBodiesAreClosed(t *testing.T) {
	tests := []struct {
		name string
		body string
		call func(*PBSClient) error
	}{
		{"CreateFixedIndex", `{"data":1}`, func(p *PBSClient) error {
			_, err := p.CreateFixedIndex(FixedIndexCreateReq{ArchiveName: "disk.fidx", Size: 1})
			return err
		}},
		{"UploadFixedCompressedChunk", "", func(p *PBSClient) error { return p.UploadFixedCompressedChunk(1, "00", []byte{1}) }},
		{"AssignFixedChunks", "", func(p *PBSClient) error { return p.AssignFixedChunks(1, []string{"00"}, []uint64{0}) }},
		{"CloseFixedIndex", "", func(p *PBSClient) error { return p.CloseFixedIndex(1, "checksum", 1, 1) }},
		{"UploadBlob", "", func(p *PBSClient) error { return p.UploadBlob("meta.blob", []byte("x")) }},
		{"UploadManifest", "", func(p *PBSClient) error { return p.UploadManifest() }},
		{"Finish", "", func(p *PBSClient) error { return p.Finish() }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := &closeTrackingBody{Reader: strings.NewReader(tc.body)}
			transport := &trackingTransport{status: http.StatusOK, body: body}
			p := &PBSClient{
				BaseURL:         "http://pbs.invalid",
				Client:          http.Client{Transport: transport},
				WritersManifest: map[uint64]int{1: 0},
				Manifest:        BackupManifest{Files: []File{{Filename: "disk.fidx"}}},
			}
			if err := tc.call(p); err != nil {
				t.Fatal(err)
			}
			if !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}
}
