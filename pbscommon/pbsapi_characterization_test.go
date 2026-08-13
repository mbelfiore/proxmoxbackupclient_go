package pbscommon

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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

func TestNonOKStatusCurrentBehavior(t *testing.T) {
	statuses := []int{400, 401, 403, 500}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			tests := []struct {
				name         string
				call         func(*PBSClient) error
				currentlyNil bool
			}{
				{"UploadBlob", func(p *PBSClient) error { return p.UploadBlob("x.blob", []byte("x")) }, true},
				{"UploadManifest", func(p *PBSClient) error { return p.UploadManifest() }, true},
				{"Finish", func(p *PBSClient) error { return p.Finish() }, true},
				{"AssignFixedChunks", func(p *PBSClient) error { return p.AssignFixedChunks(1, []string{"00"}, []uint64{0}) }, true},
				{"CloseFixedIndex", func(p *PBSClient) error { return p.CloseFixedIndex(1, "x", 1, 1) }, true},
			}
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					p, _ := protocolClient(t, status, map[string]string{"error": "forced"})
					err := tc.call(p)
					if tc.currentlyNil && err != nil {
						t.Fatalf("characterization changed: got %v", err)
					}
					if err == nil {
						t.Logf("BUG DEMONSTRATED: %s returns nil for HTTP %d", tc.name, status)
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
func TestFingerprintMismatchCurrentBehavior(t *testing.T) {
	p := &PBSClient{Insecure: true, CertFingerPrint: "00:11:22"}
	p.Connect(false, "host")
	if p.TLSConfig.VerifyPeerCertificate == nil {
		t.Fatal("expected fingerprint callback")
	}
	if err := p.TLSConfig.VerifyPeerCertificate([][]byte{selfSignedDER(t)}, nil); err != nil {
		t.Fatalf("characterization changed: mismatch rejected: %v", err)
	}
	t.Log("BUG DEMONSTRATED: a mismatching certificate fingerprint is accepted when Insecure is true")
}
