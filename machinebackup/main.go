package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"io"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"clientcommon"
	"fmt"
	"os"
	"pbscommon"
	"runtime"
	"sync/atomic"

	"github.com/alphadose/haxmap"
	"github.com/google/uuid"
	"github.com/tawesoft/golib/v2/dialog"
)

var defaultMailSubjectTemplate = "Backup {{.Status}}"
var defaultMailBodyTemplate = `{{if .Success}}Backup complete ({{.FromattedDuration}})
Chunks New {{.NewChunks}}, Reused {{.ReusedChunks}}.{{else}}Error occurred while working, backup may be not completed.
Last error is: {{.ErrorStr}}{{end}}`

var didxMagic = []byte{28, 145, 78, 165, 25, 186, 179, 205}

type ChunkState struct {
	assignments        []string
	index_hash_data    map[uint64][]byte
	assignments_offset []uint64
	processed_size     uint64
	wrid               uint64
	chunkcount         uint64
	current_chunk      []byte
	C                  pbscommon.Chunker
	newchunk           *atomic.Uint64
	reusechunk         *atomic.Uint64
	knownChunks        *haxmap.Map[string, bool]
}

type Partition struct {
	StartByte   uint64
	EndByte     uint64
	RequiresVSS bool
	Skip        bool
	VSSSource   string
}

// fixedIndexClient is the narrow PBS protocol surface used by the FIDX writer.
// Keeping this boundary small permits an in-memory correctness harness without
// changing the production protocol implementation.
type fixedIndexClient interface {
	GetKnownSha265FromFIDXContext(context.Context, string) (*haxmap.Map[string, bool], error)
	CreateFixedIndexContext(context.Context, pbscommon.FixedIndexCreateReq) (uint64, error)
	UploadFixedCompressedChunkContext(context.Context, uint64, string, []byte) error
	AssignFixedChunksContext(context.Context, uint64, []string, []uint64) error
	CloseFixedIndexContext(context.Context, uint64, string, uint64, uint64) error
}

func (c *ChunkState) Init(newchunk *atomic.Uint64, reusechunk *atomic.Uint64, knownChunks *haxmap.Map[string, bool]) {
	c.assignments = make([]string, 0)
	c.assignments_offset = make([]uint64, 0)
	c.processed_size = 0
	c.chunkcount = 0
	c.index_hash_data = make(map[uint64][]byte)
	c.current_chunk = make([]byte, 0)
	c.C = pbscommon.Chunker{}
	c.C.New(1024 * 1024 * 4)
	c.reusechunk = reusechunk
	c.newchunk = newchunk
	c.knownChunks = knownChunks
}

func BytesToString(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%dB", b)
	}
	if b < 1024*1024 {
		return fmt.Sprintf("%dKB", b/1024)
	}
	if b < 1024*1024*1024 {
		return fmt.Sprintf("%dMB", b/(1024*1024))
	}

	return fmt.Sprintf("%dGB", b/(1024*1024*1024))

}

type blockProducer func(context.Context, func([]byte) error) error

func closeOnCancellation(ctx context.Context, closer io.Closer) func() {
	stop := context.AfterFunc(ctx, func() { _ = closer.Close() })
	return func() { stop() }
}

func uploadWorker(client fixedIndexClient, filename string, totalSize uint64, producer blockProducer) error {
	return uploadWorkerWithProcessedHook(context.Background(), client, filename, totalSize, producer, nil)
}

// uploadWorkerWithProcessedHook exposes a completion notification solely for
// deterministic correctness tests. Production callers use uploadWorker above.
func uploadWorkerWithProcessedHook(parent context.Context, client fixedIndexClient, filename string, totalSize uint64, producer blockProducer, processed func(uint64)) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	newchunk := new(atomic.Uint64)
	reusechunk := new(atomic.Uint64)
	knownChunks := haxmap.New[string, bool]()

	knownChunks2, err := client.GetKnownSha265FromFIDXContext(ctx, filename)
	if err == nil {
		knownChunks = knownChunks2
	} else {
		fmt.Printf("Cannot get previous: %s\n", err.Error())
	}

	CS := ChunkState{}
	CS.Init(newchunk, reusechunk, knownChunks)
	wrid, err := client.CreateFixedIndexContext(ctx, pbscommon.FixedIndexCreateReq{
		ArchiveName: filename,
		Size:        int64(totalSize),
	})
	if err != nil {
		return err
	}

	type posSeg struct {
		pos  uint64
		data []byte
	}

	jobs := make(chan posSeg)
	producerDone := make(chan error, 1)
	var workers sync.WaitGroup
	var stateMu sync.Mutex
	var errorMu sync.Mutex
	var firstError error

	fail := func(err error) {
		if err == nil {
			return
		}
		errorMu.Lock()
		if firstError == nil {
			firstError = err
			cancel()
		}
		errorMu.Unlock()
	}
	getError := func() error {
		errorMu.Lock()
		defer errorMu.Unlock()
		return firstError
	}

	go func() {
		var producerErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				producerErr = fmt.Errorf("block producer panic: %v", recovered)
			}
			fail(producerErr)
			close(jobs)
			producerDone <- producerErr
		}()

		var position uint64
		producerErr = producer(ctx, func(block []byte) error {
			segment := posSeg{pos: position, data: block}
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case jobs <- segment:
				position += uint64(len(block))
				return nil
			}
		})
	}()

	worker := func() {
		defer workers.Done()
		zeroBlock := make([]byte, pbscommon.PBS_FIXED_CHUNK_SIZE)
		zeroSHA256 := sha256.Sum256(zeroBlock)
		for {
			select {
			case <-ctx.Done():
				return
			case segment, ok := <-jobs:
				if !ok {
					return
				}
				if ctx.Err() != nil {
					return
				}

				segmentDigest := zeroSHA256[:]
				if !bytes.Equal(segment.data, zeroBlock) {
					digest := sha256.Sum256(segment.data)
					segmentDigest = digest[:]
				}
				shaHash := hex.EncodeToString(segmentDigest)

				stateMu.Lock()
				_, exists := knownChunks.GetOrSet(shaHash, true)
				stateMu.Unlock()

				if exists {
					reusechunk.Add(1)
				} else {
					select {
					case <-ctx.Done():
						return
					default:
					}
					if err := client.UploadFixedCompressedChunkContext(ctx, wrid, shaHash, segment.data); err != nil {
						fail(err)
						return
					}
				}
				if ctx.Err() != nil {
					return
				}

				stateMu.Lock()
				CS.index_hash_data[segment.pos] = segmentDigest
				CS.assignments = append(CS.assignments, shaHash)
				CS.assignments_offset = append(CS.assignments_offset, segment.pos)
				CS.processed_size += uint64(len(segment.data))
				CS.chunkcount++
				if CS.processed_size > totalSize {
					stateMu.Unlock()
					fail(fmt.Errorf("fatal: tried to backup more data than specified size"))
					return
				}
				fmt.Printf("Chunk %d/%d/%d\n", CS.chunkcount, int(math.Ceil(float64(totalSize)/float64(pbscommon.PBS_FIXED_CHUNK_SIZE))), reusechunk.Load())
				stateMu.Unlock()
				if processed != nil {
					processed(segment.pos)
				}
			}
		}
	}

	workers.Add(8)
	for range 8 {
		go worker()
	}
	workers.Wait()
	producerErr := <-producerDone
	if err := getError(); err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if producerErr != nil {
		return producerErr
	}

	// Avoid request-entity-too-large responses by assigning at most 128 chunks.
	for offset := 0; offset < len(CS.assignments); offset += 128 {
		end := min(offset+128, len(CS.assignments))
		if err := client.AssignFixedChunksContext(ctx, wrid, CS.assignments[offset:end], CS.assignments_offset[offset:end]); err != nil {
			return err
		}
	}

	chunkDigests := sha256.New()
	positions := slices.Collect(maps.Keys(CS.index_hash_data))
	slices.Sort(positions)
	for _, position := range positions {
		_, _ = chunkDigests.Write(CS.index_hash_data[position])
	}

	return client.CloseFixedIndexContext(ctx, wrid, hex.EncodeToString(chunkDigests.Sum(nil)), CS.processed_size, CS.chunkcount)
}

func Slugify(input string) string {
	// Convert to lowercase
	s := strings.ToLower(input)
	s = strings.ReplaceAll(s, "/", "")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "_", "")
	reg := regexp.MustCompile(`[^a-z0-9-]+`)
	s = reg.ReplaceAllString(s, "")
	regDash := regexp.MustCompile(`-+`)
	s = regDash.ReplaceAllString(s, "")
	s = strings.Trim(s, "-")

	return s
}

var physicalDrivePattern = regexp.MustCompile(`(?i)^\\\\\.\\physicaldrive(\d+)$`)

func physicalDriveIndex(path string) (int, bool) {
	matches := physicalDrivePattern.FindStringSubmatch(path)
	if matches == nil {
		return 0, false
	}
	idx, err := strconv.ParseInt(matches[1], 10, 32)
	return int(idx), err == nil
}

//TODO: Perhaps on linux we could use that https://github.com/datto/dattobd for block devices

func backupFileDevice(client *pbscommon.PBSClient, filename string) error {
	slug := Slugify(filename)
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	producer := func(ctx context.Context, emit func([]byte) error) error {
		stopClose := closeOnCancellation(ctx, file)
		defer stopClose()
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		for {
			block := make([]byte, pbscommon.PBS_FIXED_CHUNK_SIZE)
			bytesRead, err := file.Read(block)
			if bytesRead > 0 {
				if emitErr := emit(block[:bytesRead]); emitErr != nil {
					return emitErr
				}
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			default:
			}
		}
	}
	return uploadWorker(client, slug+".fidx", uint64(size), producer)
}

type BackupDisk struct {
	Index int
	Size  int64
}

func main() {

	cfg := loadConfig()

	if ok := cfg.valid(); !ok {
		if runtime.GOOS == "windows" {
			usage := "All options are mandatory:\n"
			flag.VisitAll(func(f *flag.Flag) {
				usage += "-" + f.Name + " " + f.Usage + "\n"
			})
			dialog.Error(usage)
		} else {
			fmt.Println("All options are mandatory")

			flag.PrintDefaults()
		}
		os.Exit(1)
	}
	L := clientcommon.Locking{}

	lock_ok := L.AcquireProcessLock()
	if !lock_ok {

		dialog.Error("Backup jobs need to run exclusively, please wait until the previous job has finished")
		os.Exit(2)
	}
	defer L.ReleaseProcessLock()

	if cfg.SysTray {
		sysTraySetup()
	}

	insecure := cfg.CertFingerprint != ""

	client := &pbscommon.PBSClient{
		BaseURL:         cfg.BaseURL,
		CertFingerPrint: cfg.CertFingerprint, //"ea:7d:06:f9:87:73:a4:72:d0:e8:05:a4:b3:3d:95:d7:0a:26:dd:6d:5c:ca:e6:99:83:e4:11:3b:5f:10:f4:4b",
		AuthID:          cfg.AuthID,
		Secret:          cfg.Secret,
		Datastore:       cfg.Datastore,
		Namespace:       cfg.Namespace,
		Insecure:        insecure,
		Manifest: pbscommon.BackupManifest{
			BackupID: cfg.BackupID,
		},
	}

	//Physical drive paths will be like  "\\\\.\\PhysicalDrive0"
	client.Connect(false, cfg.BackupType)
	disks := make([]BackupDisk, 0)

	for _, dev := range cfg.BackupDevices {
		if idx, ok := physicalDriveIndex(dev); ok {
			size, err := backupWindowsDisk(client, idx)
			if err != nil {
				panic(err)
			}
			disks = append(disks, BackupDisk{
				Index: idx,
				Size:  size,
			})
		} else {
			err := backupFileDevice(client, dev)
			if err != nil {
				panic(err)
			}
		}
	}

	if cfg.BackupType == "vm" {
		vmid, err := strconv.ParseInt(cfg.BackupID, 10, 32)
		if err != nil {
			panic(err)
		}
		hostname, err := os.Hostname()
		if err != nil {
			panic(err)
		}
		cfgt := QEMUConfigTemplate{
			VMGenId: uuid.New().String(),
			VMID:    vmid,
			Disks:   disks,
			VMName:  hostname,
			SMBIOS:  uuid.New().String(), //TODO extract from real machine
		}
		if runtime.GOOS == "windows" { // TODO Improve
			cfgt.OS = "win11"
		} else {
			cfgt.OS = "l26"
		}
		qemuConfig, err := renderQEMUConfig(cfgt)
		if err != nil {
			panic(err)
		}
		if err := client.UploadBlob("qemu-server.conf.blob", qemuConfig); err != nil {
			panic(err)
		}
	}

	err := client.UploadManifest()
	if err != nil {
		panic(err)
	}
	client.Finish()

	/*partitions, err := disk.Partitions(false) // false means don't include virtual partitions
	if err != nil {
		log.Fatalf("Error fetching partitions: %v", err)
	}

	// Iterate over partitions and print them
	for _, partition := range partitions {
		// Print partition information
		fmt.Printf("Device: %s\n", partition.Device)
		fmt.Printf("Mountpoint: %s\n", partition.Mountpoint)
		fmt.Printf("Filesystem type: %s\n", partition.Fstype)

		// List the corresponding drive letter for each partition
		// This is platform dependent, but it should map to the drive letter on Windows.
		// Windows typically assigns a drive letter (like C:, D:) to each partition.
		// We use partition.Mountpoint to get it, which should include the letter (e.g. "C:\").
		if partition.Mountpoint != "" {
			fmt.Printf("Drive Letter: %s\n", partition.Mountpoint)
		}
	}

	return

	SNAP := snapshot.CreateVSSSnapshot("C:\\")
	defer snapshot.VSSCleanup()
	fmt.Println("ObjectPath: " + SNAP.ObjectPath)
	file, err := os.Open(strings.TrimRight(SNAP.ObjectPath, "\\"))
	if err != nil {
		panic(err)
	}

	x := make([]byte, 1024)
	n, err := file.Read(x)
	if err != nil {
		panic(err)
	} else {
		fmt.Print(n)
	}*/

	//Windows backup logic will be as follows

	//1. Enumerate fixed non-usb disks ( SATA + NVME )
	//2. Enumerate partitions with offset and length
	//3. Start reading using PhysicalDriveX special file
	//4. If we go into a region that contains a mounted partition, if filesystem is NTFS or ReFS , take VSS snapshot and switch to the associated shadow volume file
	//4. If the partition is not mounted just keep reading, if the partition is mounted and not NTFS or ReFS for now throw a warning and write zeros
	//5. For each disk create a fixed index ( Do it in parallel maybe)

}
