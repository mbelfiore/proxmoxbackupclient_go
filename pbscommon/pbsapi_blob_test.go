package pbscommon

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestUploadBlobRecordsEncodedSizeAndChecksum(t *testing.T) {
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/blob" {
			t.Fatalf("request = %s %s, want POST /blob", r.Method, r.URL.Path)
		}

		var err error
		uploaded, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := r.URL.Query().Get("encoded-size"), strconv.Itoa(len(uploaded)); got != want {
			t.Errorf("encoded-size = %q, want %q", got, want)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	pbs := &PBSClient{BaseURL: server.URL, Client: *server.Client()}
	if err := pbs.UploadBlob("qemu-server.conf.blob", []byte("metadata")); err != nil {
		t.Fatal(err)
	}

	if len(pbs.Manifest.Files) != 1 {
		t.Fatalf("manifest files = %d, want 1", len(pbs.Manifest.Files))
	}
	file := pbs.Manifest.Files[0]
	if file.Size != int64(len(uploaded)) {
		t.Errorf("manifest size = %d, want encoded size %d", file.Size, len(uploaded))
	}
	digest := sha256.Sum256(uploaded)
	if want := hex.EncodeToString(digest[:]); file.Csum != want {
		t.Errorf("manifest checksum = %q, want SHA-256 of encoded blob %q", file.Csum, want)
	}
	if len(file.Csum) != sha256.Size*2 {
		t.Errorf("manifest checksum length = %d, want %d", len(file.Csum), sha256.Size*2)
	}
}
