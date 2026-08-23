package pbscommon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alphadose/haxmap"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/net/http2"
)

// PBS upgrade responses contain only a small HTTP/1.1 header. Keep a generous
// 64 KiB ceiling while preventing an unbounded response from consuming memory.
const maxPBSUpgradeHeaderSize = 64 * 1024

type IndexCreateResp struct {
	WriterID int `json:"data"`
}

type IndexPutReq struct {
	DigestList []string `json:"digest-list"`
	OffsetList []uint64 `json:"offset-list"`
	WriterID   uint64   `json:"wid"`
}

type IndexCloseReq struct {
	ChunkCount uint64 `json:"chunk-count"`
	CheckSum   string `json:"csum"`
	Size       uint64 `json:"size"`
	WriterID   uint64 `json:"wid"`
}

type File struct {
	CryptMode string `json:"crypt-mode"`
	Csum      string `json:"csum"`
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
}

type ChunkUploadStats struct {
	CompressedSize int64 `json:"compressed_size"`
	Count          int   `json:"count"`
	Duplicates     int   `json:"duplicates"`
	Size           int64 `json:"size"`
}

type FixedIndexCreateReq struct {
	ArchiveName string `json:"archive-name"`
	Size        int64  `json:"size"`
}

type Unprotected struct {
	ChunkUploadStats ChunkUploadStats `json:"chunk_upload_stats"`
}

type BackupManifest struct {
	BackupID    string      `json:"backup-id"`
	BackupTime  int64       `json:"backup-time"`
	BackupType  string      `json:"backup-type"`
	Files       []File      `json:"files"`
	Signature   interface{} `json:"signature"`
	Unprotected Unprotected `json:"unprotected"`
}

type AuthErr struct {
}

func (e *AuthErr) Error() string {
	return "Authentication error"
}

type PBSClient struct {
	BaseURL         string
	CertFingerPrint string
	APIToken        string
	Secret          string
	AuthID          string

	Datastore string
	Namespace string
	Manifest  BackupManifest

	Insecure bool

	Client    http.Client
	TLSConfig tls.Config
	ZSTDDec   *zstd.Decoder

	WritersManifest map[uint64]int
}

const PBS_FIXED_CHUNK_SIZE = 4 * 1024 * 1024

var blobCompressedMagic = []byte{49, 185, 88, 66, 111, 182, 163, 127}
var blobUncompressedMagic = []byte{66, 171, 56, 7, 190, 131, 112, 161}

const maxHTTPErrorBody = 8 * 1024

func checkHTTPResponse(req *http.Request, resp *http.Response) error {
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxHTTPErrorBody+1))
	truncated := len(body) > maxHTTPErrorBody
	if truncated {
		body = body[:maxHTTPErrorBody]
	}
	detail := strings.TrimSpace(string(body))
	if truncated {
		detail += " [truncated]"
	}
	if readErr != nil {
		detail = fmt.Sprintf("unable to read response body: %v", readErr)
	}
	if detail == "" {
		detail = "empty response body"
	}
	return fmt.Errorf("PBS %s %s failed: %d %s: %s", req.Method, req.URL.Path, resp.StatusCode, http.StatusText(resp.StatusCode), detail)
}

func normalizeFingerprint(value string) ([]byte, error) {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), ":", ""))
	decoded, err := hex.DecodeString(normalized)
	if err != nil || len(decoded) != sha256.Size {
		return nil, fmt.Errorf("invalid SHA-256 certificate fingerprint")
	}
	return decoded, nil
}

func (pbs *PBSClient) configureTLS(config *tls.Config) {
	fingerprint := strings.TrimSpace(pbs.CertFingerPrint)
	*config = tls.Config{InsecureSkipVerify: pbs.Insecure}
	if fingerprint == "" {
		return
	}

	// A configured fingerprint is the trust policy, including for self-signed
	// certificates, so normal CA verification is replaced by exact pinning.
	config.InsecureSkipVerify = true
	expected, fingerprintErr := normalizeFingerprint(fingerprint)
	config.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if fingerprintErr != nil {
			return fingerprintErr
		}
		if len(rawCerts) == 0 {
			return fmt.Errorf("no certificates presented by the peer")
		}
		calculated := sha256.Sum256(rawCerts[0])
		if !bytes.Equal(calculated[:], expected) {
			return fmt.Errorf("certificate fingerprint does not match")
		}
		return nil
	}
}

type SnapshotsResp struct {
	Data []BackupManifest `json:"data"`
}

func (pbs *PBSClient) ListSnapshots() ([]BackupManifest, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	if pbs.Insecure || strings.TrimSpace(pbs.CertFingerPrint) != "" {
		tlsConfig := &tls.Config{}
		pbs.configureTLS(tlsConfig)
		tr := &http.Transport{
			TLSClientConfig: tlsConfig,
		}
		client.Transport = tr
	}

	ret := make([]BackupManifest, 0)
	var r SnapshotsResp
	params := url.Values{}
	params.Add("ns", pbs.Namespace)
	fullURL := fmt.Sprintf("%s/api2/json/admin/datastore/%s/snapshots?%s", pbs.BaseURL, pbs.Datastore, params.Encode())

	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return ret, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Add("Authorization", fmt.Sprintf("PBSAPIToken=%s:%s", pbs.AuthID, pbs.Secret))
	resp, err := client.Do(req)
	if err != nil {
		return ret, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return ret, fmt.Errorf("HTTP error: %d - %s", resp.StatusCode, string(body))
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return ret, err
	}
	return r.Data, nil

}

func (pbs *PBSClient) CreateFixedIndex(fic FixedIndexCreateReq) (uint64, error) {
	return pbs.CreateFixedIndexContext(context.Background(), fic)
}

func (pbs *PBSClient) CreateFixedIndexContext(ctx context.Context, fic FixedIndexCreateReq) (uint64, error) {
	jd, err := json.Marshal(fic)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", pbs.BaseURL+"/fixed_index", bytes.NewBuffer(jd))
	if err != nil {
		return 0, err
	}
	req.Header.Add("Authorization", fmt.Sprintf("PBSAPIToken=%s:%s", pbs.AuthID, pbs.Secret))
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp2, err := pbs.Client.Do(req)
	if err != nil {
		fmt.Println("Error making request:", err)
		return 0, err
	}
	defer resp2.Body.Close()

	if err := checkHTTPResponse(req, resp2); err != nil {
		return 0, err
	}

	resp1, err := io.ReadAll(resp2.Body)
	if err != nil {
		return 0, err
	}
	var R IndexCreateResp
	err = json.Unmarshal(resp1, &R)
	if err != nil {
		fmt.Println("Error parsing JSON:", err)
		return 0, err
	}
	fmt.Println("Writer id: ", R.WriterID)
	f := File{
		CryptMode: "none",
		Csum:      "",
		Filename:  fic.ArchiveName,
		Size:      0,
	}
	pbs.Manifest.Files = append(pbs.Manifest.Files, f)
	pbs.WritersManifest[uint64(R.WriterID)] = len(pbs.Manifest.Files) - 1
	return uint64(R.WriterID), nil

}

func (pbs *PBSClient) AssignFixedChunks(writerid uint64, digests []string, offsets []uint64) error {
	return pbs.AssignFixedChunksContext(context.Background(), writerid, digests, offsets)
}

func (pbs *PBSClient) AssignFixedChunksContext(ctx context.Context, writerid uint64, digests []string, offsets []uint64) error {
	indexput := &IndexPutReq{
		WriterID:   writerid,
		DigestList: digests,
		OffsetList: offsets,
	}

	jsondata, err := json.Marshal(indexput)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", pbs.BaseURL+"/fixed_index", bytes.NewBuffer(jsondata))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	resp2, err := pbs.Client.Do(req)
	if err != nil {
		fmt.Println("Error making request:", err)
		return err
	}
	defer resp2.Body.Close()
	if err := checkHTTPResponse(req, resp2); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp2.Body)
	return nil
}

func (pbs *PBSClient) CloseFixedIndex(writerid uint64, checksum string, totalsize uint64, chunkcount uint64) error {
	return pbs.CloseFixedIndexContext(context.Background(), writerid, checksum, totalsize, chunkcount)
}

func (pbs *PBSClient) CloseFixedIndexContext(ctx context.Context, writerid uint64, checksum string, totalsize uint64, chunkcount uint64) error {
	finishreq := &IndexCloseReq{
		WriterID:   writerid,
		CheckSum:   checksum,
		Size:       totalsize,
		ChunkCount: chunkcount,
	}
	jsonpayload, err := json.Marshal(finishreq)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", pbs.BaseURL+"/fixed_close", bytes.NewBuffer(jsonpayload))
	if err != nil {
		return err
	}
	req.Header.Add("Authorization", fmt.Sprintf("PBSAPIToken=%s:%s", pbs.AuthID, pbs.Secret))
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp2, err := pbs.Client.Do(req)
	if err != nil {
		fmt.Println("Error making request:", err)
		return err
	}
	defer resp2.Body.Close()
	if err := checkHTTPResponse(req, resp2); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp2.Body)

	f := &pbs.Manifest.Files[pbs.WritersManifest[writerid]]

	f.Csum = checksum
	f.Size = int64(totalsize)

	return nil
}

func (pbs *PBSClient) CreateDynamicIndex(name string) (uint64, error) {

	req, err := http.NewRequest("POST", pbs.BaseURL+"/dynamic_index", bytes.NewBuffer([]byte(fmt.Sprintf("{\"archive-name\": \"%s\"}", name))))
	if err != nil {
		return 0, err
	}

	req.Header.Add("Authorization", fmt.Sprintf("PBSAPIToken=%s:%s", pbs.AuthID, pbs.Secret))
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp2, err := pbs.Client.Do(req)
	if err != nil {
		fmt.Println("Error making request:", err)
		return 0, err
	}
	defer resp2.Body.Close()

	if err := checkHTTPResponse(req, resp2); err != nil {
		return 0, err
	}

	resp1, err := io.ReadAll(resp2.Body)
	if err != nil {
		return 0, err
	}
	var R IndexCreateResp
	err = json.Unmarshal(resp1, &R)
	if err != nil {
		fmt.Println("Error parsing JSON:", err)
		return 0, err
	}
	fmt.Println("Writer id: ", R.WriterID)
	f := File{
		CryptMode: "none",
		Csum:      "",
		Filename:  name,
		Size:      0,
	}
	pbs.Manifest.Files = append(pbs.Manifest.Files, f)
	pbs.WritersManifest[uint64(R.WriterID)] = len(pbs.Manifest.Files) - 1
	return uint64(R.WriterID), nil
}

func (pbs *PBSClient) UploadDynamicUncompressedChunk(writerid uint64, digest string, chunkdata []byte) error {
	return pbs.UploadChunk(writerid, digest, chunkdata, true, false)
}
func (pbs *PBSClient) UploadFixedUncompressedChunk(writerid uint64, digest string, chunkdata []byte) error {
	return pbs.UploadChunk(writerid, digest, chunkdata, false, false)
}
func (pbs *PBSClient) UploadDynamicCompressedChunk(writerid uint64, digest string, chunkdata []byte) error {
	return pbs.UploadChunk(writerid, digest, chunkdata, true, true)
}
func (pbs *PBSClient) UploadFixedCompressedChunk(writerid uint64, digest string, chunkdata []byte) error {
	return pbs.UploadFixedCompressedChunkContext(context.Background(), writerid, digest, chunkdata)
}
func (pbs *PBSClient) UploadFixedCompressedChunkContext(ctx context.Context, writerid uint64, digest string, chunkdata []byte) error {
	return pbs.UploadChunkContext(ctx, writerid, digest, chunkdata, false, true)
}

func (pbs *PBSClient) UploadChunk(writerid uint64, digest string, chunkdata []byte, dynamic bool, compressed bool) error {
	return pbs.UploadChunkContext(context.Background(), writerid, digest, chunkdata, dynamic, compressed)
}

func (pbs *PBSClient) UploadChunkContext(ctx context.Context, writerid uint64, digest string, chunkdata []byte, dynamic bool, compressed bool) error {
	outBuffer := make([]byte, 0)
	if compressed {
		outBuffer = append(outBuffer, blobCompressedMagic...)
		compressedData := make([]byte, 0)

		//opt := zstd.WithEncoderLevel(zstd.SpeedFastest)
		w, _ := zstd.NewWriter(nil)
		compressedData = w.EncodeAll(chunkdata, compressedData)
		checksum := crc32.Checksum(compressedData, crc32.IEEETable)
		//binary.Write(outBuffer, binary.LittleEndian, checksum)
		outBuffer = binary.LittleEndian.AppendUint32(outBuffer, checksum)

		//fmt.Printf("Appended checksum %08x , len: %d\n", checksum, len(outBuffer))

		outBuffer = append(outBuffer, compressedData...)

		if len(compressedData) > len(chunkdata) {
			return pbs.UploadChunkContext(ctx, writerid, digest, chunkdata, dynamic, false)
		}
	} else {
		outBuffer = append(outBuffer, blobUncompressedMagic...)
		checksum := crc32.Checksum(chunkdata, crc32.IEEETable)
		outBuffer = binary.LittleEndian.AppendUint32(outBuffer, checksum)
		outBuffer = append(outBuffer, chunkdata...)
	}

	//fmt.Printf("Compressed: %d , Orig: %d\n", len(compressedData), len(chunkdata))

	q := &url.Values{}
	q.Add("digest", digest)
	q.Add("encoded-size", fmt.Sprintf("%d", len(outBuffer)))
	q.Add("size", fmt.Sprintf("%d", len(chunkdata)))
	q.Add("wid", fmt.Sprintf("%d", writerid))
	suburl := "/dynamic_chunk?"
	if !dynamic {
		suburl = "/fixed_chunk?"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", pbs.BaseURL+suburl+q.Encode(), bytes.NewBuffer(outBuffer))
	if err != nil {
		fmt.Println("Error making request:", err)
		return err
	}
	resp2, err := pbs.Client.Do(req)
	if err != nil {
		fmt.Println("Error making request:", err)
		return err
	}
	defer resp2.Body.Close()

	if err := checkHTTPResponse(req, resp2); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp2.Body)
	return nil
}

func (pbs *PBSClient) AssignDynamicChunks(writerid uint64, digests []string, offsets []uint64) error {
	indexput := &IndexPutReq{
		WriterID:   writerid,
		DigestList: digests,
		OffsetList: offsets,
	}

	jsondata, err := json.Marshal(indexput)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("PUT", pbs.BaseURL+"/dynamic_index", bytes.NewBuffer(jsondata))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	resp2, err := pbs.Client.Do(req)
	if err != nil {
		fmt.Println("Error making request:", err)
		return err
	}
	defer resp2.Body.Close()
	if err := checkHTTPResponse(req, resp2); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp2.Body)
	return nil
}

func (pbs *PBSClient) CloseDynamicIndex(writerid uint64, checksum string, totalsize uint64, chunkcount uint64) error {
	finishreq := &IndexCloseReq{
		WriterID:   writerid,
		CheckSum:   checksum,
		Size:       totalsize,
		ChunkCount: chunkcount,
	}
	jsonpayload, err := json.Marshal(finishreq)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", pbs.BaseURL+"/dynamic_close", bytes.NewBuffer(jsonpayload))
	if err != nil {
		return err
	}
	req.Header.Add("Authorization", fmt.Sprintf("PBSAPIToken=%s:%s", pbs.AuthID, pbs.Secret))
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp2, err := pbs.Client.Do(req)
	if err != nil {
		fmt.Println("Error making request:", err)
		return err
	}
	defer resp2.Body.Close()
	if err := checkHTTPResponse(req, resp2); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp2.Body)

	f := &pbs.Manifest.Files[pbs.WritersManifest[writerid]]

	f.Csum = checksum
	f.Size = int64(totalsize)

	return nil
}

func (pbs *PBSClient) UploadBlob(name string, data []byte) error {
	out := make([]byte, 0)
	out = append(out, blobUncompressedMagic...)

	checksum := crc32.ChecksumIEEE(data)
	out = binary.LittleEndian.AppendUint32(out, checksum)
	out = append(out, data...)

	q := &url.Values{}
	q.Add("encoded-size", fmt.Sprintf("%d", len(out)))
	q.Add("file-name", name)

	req, err := http.NewRequest("POST", pbs.BaseURL+"/blob?"+q.Encode(), bytes.NewBuffer(out))
	if err != nil {
		return err
	}

	resp2, err := pbs.Client.Do(req)
	if err != nil {
		fmt.Println("Error making request:", err)
		return err
	}
	defer resp2.Body.Close()

	if err := checkHTTPResponse(req, resp2); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp2.Body)

	digest := sha256.Sum256(out)
	pbs.Manifest.Files = append(pbs.Manifest.Files, File{
		CryptMode: "none",
		Csum:      hex.EncodeToString(digest[:]),
		Filename:  name,
		Size:      int64(len(out)),
	})

	return nil
}

func (pbs *PBSClient) UploadManifest() error {
	manifestBin, err := json.Marshal(pbs.Manifest)
	if err != nil {
		return err
	}
	return pbs.UploadBlob("index.json.blob", manifestBin)
}

func (pbs *PBSClient) Finish() error {
	req, err := http.NewRequest("POST", pbs.BaseURL+"/finish", nil)
	if err != nil {
		return err
	}
	req.Header.Add("Authorization", fmt.Sprintf("PBSAPIToken=%s:%s", pbs.AuthID, pbs.Secret))
	resp2, err := pbs.Client.Do(req)
	if err != nil {
		fmt.Println("Error making request:", err)
		return err
	}
	defer resp2.Body.Close()
	if err := checkHTTPResponse(req, resp2); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp2.Body)
	return nil
}

func (pbs *PBSClient) dialPBSTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := tls.Dialer{Config: &pbs.TLSConfig}
	conn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if err := context.Cause(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (pbs *PBSClient) upgradePBSConnection(ctx context.Context, conn net.Conn, reader bool) (net.Conn, error) {
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	succeeded := false
	defer func() {
		stopCancel()
		if !succeeded {
			_ = conn.Close()
		}
	}()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}

	q := &url.Values{}
	q.Add("backup-time", fmt.Sprintf("%d", pbs.Manifest.BackupTime))
	q.Add("backup-type", pbs.Manifest.BackupType)
	q.Add("store", pbs.Datastore)
	if pbs.Namespace != "" {
		q.Add("ns", pbs.Namespace)
	}
	q.Add("backup-id", pbs.Manifest.BackupID)
	fmt.Println(q.Encode())

	endpoint := "/api2/json/backup"
	upgrade := "proxmox-backup-protocol-v1"
	if reader {
		endpoint = "/api2/json/reader"
		upgrade = "proxmox-backup-reader-protocol-v1"
	}
	request := "GET " + endpoint + "?" + q.Encode() + " HTTP/1.1\r\n" +
		"Authorization: " + fmt.Sprintf("PBSAPIToken=%s:%s", pbs.AuthID, pbs.Secret) + "\r\n" +
		"Upgrade: " + upgrade + "\r\n" +
		"Connection: Upgrade\r\n\r\n"
	written, err := io.WriteString(conn, request)
	if err != nil {
		if ctxErr := context.Cause(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	if written != len(request) {
		return nil, io.ErrShortWrite
	}

	fmt.Printf("Reading response to upgrade...\n")
	buf := make([]byte, 0)
	for !strings.HasSuffix(string(buf), "\r\n\r\n") && !strings.HasSuffix(string(buf), "\n\n") {
		byteBuffer := make([]byte, 1)
		bytesRead, err := conn.Read(byteBuffer)
		if err != nil || bytesRead == 0 {
			if ctxErr := context.Cause(ctx); ctxErr != nil {
				return nil, ctxErr
			}
			fmt.Println("Connection unexpectedly closed")
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		buf = append(buf, byteBuffer[:bytesRead]...)
		terminated := strings.HasSuffix(string(buf), "\r\n\r\n") || strings.HasSuffix(string(buf), "\n\n")
		if len(buf) > maxPBSUpgradeHeaderSize || (len(buf) == maxPBSUpgradeHeaderSize && !terminated) {
			return nil, fmt.Errorf("PBS upgrade response header exceeds %d bytes", maxPBSUpgradeHeaderSize)
		}
	}
	statusLine := strings.TrimSuffix(strings.SplitN(string(buf), "\n", 2)[0], "\r")
	tokens := strings.SplitN(statusLine, " ", 3)
	if len(tokens) < 2 {
		return nil, fmt.Errorf("malformed PBS upgrade HTTP status line")
	}
	if _, _, ok := http.ParseHTTPVersion(tokens[0]); !ok {
		return nil, fmt.Errorf("malformed PBS upgrade HTTP version")
	}
	if len(tokens[1]) != 3 {
		return nil, fmt.Errorf("malformed PBS upgrade HTTP status code")
	}
	statusCode, err := strconv.Atoi(tokens[1])
	if err != nil || statusCode < 100 || statusCode > 999 {
		return nil, fmt.Errorf("malformed PBS upgrade HTTP status code")
	}
	if statusCode != http.StatusSwitchingProtocols {
		fmt.Println("Unexpected response code: " + strings.Join(tokens[1:], " "))
		fmt.Println(string(buf))
		return nil, &AuthErr{}
	}
	if !stopCancel() {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		return nil, context.Canceled
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}

	fmt.Printf("Upgraderesp: %s\n", string(buf))
	fmt.Println("Successfully upgraded to HTTP/2.")
	succeeded = true
	return conn, nil
}

func (pbs *PBSClient) Connect(reader bool, backuptype string) {

	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))

	if err != nil {
		panic(err)
	}

	pbs.ZSTDDec = dec

	pbs.WritersManifest = make(map[uint64]int)
	pbs.configureTLS(&pbs.TLSConfig)
	if !reader {
		pbs.Manifest.BackupTime = time.Now().Unix()
	}
	pbs.Manifest.BackupType = backuptype
	if pbs.Manifest.BackupID == "" {
		hostname, _ := os.Hostname()
		pbs.Manifest.BackupID = hostname
	}
	pbs.Client = http.Client{
		Transport: &http2.Transport{

			DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
				// PBS authenticates and upgrades this owned TLS connection before
				// handing it to the HTTP/2 transport. The request context covers
				// both TLS dialing and the blocking upgrade exchange.
				conn, err := pbs.dialPBSTLS(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				return pbs.upgradePBSConnection(ctx, conn, reader)
			},
		},
	}

}

type FIDXHeader struct {
	Magic        [8]byte
	UUID         [16]byte
	CreationTime uint64
	IndexCsum    [32]byte
	Size         uint64
	ChunkSize    uint64
	Padding      [4016]byte
}

func (pbs *PBSClient) DownloadPreviousToBytes(archivename string) ([]byte, error) { //In the future also download to tmp if index is extremely big...
	return pbs.DownloadPreviousToBytesContext(context.Background(), archivename)
}

func (pbs *PBSClient) DownloadPreviousToBytesContext(ctx context.Context, archivename string) ([]byte, error) { //In the future also download to tmp if index is extremely big...
	q := &url.Values{}

	q.Add("archive-name", archivename)

	req, err := http.NewRequestWithContext(ctx, "GET", pbs.BaseURL+"/previous?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Authorization", fmt.Sprintf("PBSAPIToken=%s:%s", pbs.AuthID, pbs.Secret))
	resp2, err := pbs.Client.Do(req)
	if err != nil {
		fmt.Println("Error making request:", err)
		return nil, err
	}
	defer resp2.Body.Close()

	ret, err := io.ReadAll(resp2.Body)

	if err != nil {
		return nil, err
	}

	return ret, nil

}

func (pbs *PBSClient) DownloadToBytes(archivename string) ([]byte, error) { //In the future also download to tmp if index is extremely big...
	q := &url.Values{}

	q.Add("file-name", archivename)

	req, err := http.NewRequest("GET", pbs.BaseURL+"/download?"+q.Encode(), nil)
	req.Header.Add("Authorization", fmt.Sprintf("PBSAPIToken=%s:%s", pbs.AuthID, pbs.Secret))
	if err != nil {
		return nil, err
	}
	resp2, err := pbs.Client.Do(req)
	if err != nil {
		fmt.Println("Error making request:", err)
		return nil, err
	}
	defer resp2.Body.Close()

	ret, err := io.ReadAll(resp2.Body)

	if err != nil {
		return nil, err
	}

	return ret, nil

}

func (pbs *PBSClient) GetKnownSha265FromFIDX(archivename string) (*haxmap.Map[string, bool], error) {
	return pbs.GetKnownSha265FromFIDXContext(context.Background(), archivename)
}

func (pbs *PBSClient) GetKnownSha265FromFIDXContext(ctx context.Context, archivename string) (*haxmap.Map[string, bool], error) {
	data, err := pbs.DownloadPreviousToBytesContext(ctx, archivename)
	if err != nil {
		fmt.Println("Download of previous failed.")
		return nil, err
	}
	rdr := bytes.NewReader(data)
	var hdr FIDXHeader
	err = binary.Read(rdr, binary.LittleEndian, &hdr)
	if err != nil {
		fmt.Println("Failed to read FIDX Header")
		return nil, err
	}
	if !slices.Equal(hdr.Magic[:], []byte{47, 127, 65, 237, 145, 253, 15, 205}) {
		return nil, fmt.Errorf("FIDX: Invalid magic %+v", hdr.Magic)
	}
	ret := haxmap.New[string, bool]()
	log.Printf("Reading %d entries...", hdr.Size/hdr.ChunkSize)
	H := make([]byte, 32)
	for i := uint64(0); i < hdr.Size/hdr.ChunkSize; i++ {

		nbytes, err := rdr.Read(H)
		if err != nil {
			log.Printf("EOF at %d/%d", i, hdr.Size/hdr.ChunkSize)
			return nil, err
		}
		if nbytes != len(H) {
			return nil, fmt.Errorf("FIDX: Short read")
		}
		if i%4096 == 0 {
			log.Printf("%d/%d", i, hdr.Size/hdr.ChunkSize)
		}

		ret.Set(hex.EncodeToString(H), true)
	}
	log.Printf("Loaded %d known chunks from previous", ret.Len())
	return ret, nil

}

func (pbs *PBSClient) GetChunkData(digest string) ([]byte, error) {
	q := &url.Values{}

	q.Add("digest", digest)

	req, err := http.NewRequest("GET", pbs.BaseURL+"/chunk?"+q.Encode(), nil)
	req.Header.Add("Authorization", fmt.Sprintf("PBSAPIToken=%s:%s", pbs.AuthID, pbs.Secret))
	if err != nil {
		return nil, err
	}
	resp2, err := pbs.Client.Do(req)
	if err != nil {
		fmt.Println("Error making request:", err)
		return nil, err
	}
	defer resp2.Body.Close()

	ret, err := io.ReadAll(resp2.Body)

	if err != nil {
		return nil, err
	}

	if slices.Equal(ret[:8], blobUncompressedMagic) {
		return ret[12:], nil
	} else if slices.Equal(ret[:8], blobCompressedMagic) {
		ret2 := make([]byte, 0)
		ret2, err = pbs.ZSTDDec.DecodeAll(ret[12:], ret2)
		if err != nil {
			return nil, err
		}
		return ret2, nil
	} else {
		return nil, fmt.Errorf("Invalid chunk magic , or encrypted chunk!")
	}

}
