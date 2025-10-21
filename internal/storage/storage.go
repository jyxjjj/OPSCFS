package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/jyxjjj/OPSCFS/internal/blocks"
	"github.com/jyxjjj/OPSCFS/internal/index"
	"github.com/jyxjjj/OPSCFS/internal/pak"
)

const (
	ChunkSize        = 64 << 10 // 64KiB
	MaxVerifyWorkers = 64
)

type Store struct {
	root    string
	key     []byte
	keyHash []byte
	idx     *index.Index
	mu      sync.Mutex
}

func New(root string, key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, errors.New("key must be 32 bytes for AES-256")
	}
	keyHash := sha256.Sum256(key)
	idxPath := filepath.Join(root, "index.idx")
	ix, err := index.Open(idxPath)
	if err != nil {
		return nil, err
	}
	blocksPath := filepath.Join(root, "blocks.idx")
	blkIdx, err := blocks.Open(blocksPath)
	if err != nil {
		return nil, err
	}
	// Validate: ensure every block referenced in manifests exists in blocks.idx
	for fname, manifest := range ix.List() {
		for _, bid := range manifest {
			if _, ok := blkIdx.Get(bid); !ok {
				return nil, fmt.Errorf("startup integrity check failed: file %q references unknown block %s", fname, bid)
			}
		}
	}
	return &Store{root: root, key: key, keyHash: keyHash[:], idx: ix}, nil
}

func (s *Store) pakPathForHash(h []byte) string {
	// AA/FF/<hex>.pak where AA=first two chars, FF=3rd and 4th
	he := hex.EncodeToString(h)
	a := he[0:2]
	f := he[2:4]
	name := he + ".pak"
	return filepath.Join(s.root, a, f, name)
}

// Put writes data as a single file composed of chunks. Returns a reference key (filename) used in index.
func (s *Store) Put(name string, r io.Reader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	blkIdx, err := blocks.Open(filepath.Join(s.root, "blocks.idx"))
	if err != nil {
		return err
	}

	// load existing manifest (if any) so we can decrement refs after commit
	oldManifest, _ := s.idx.GetManifest(name)

	// first pass: determine blocks and collect data for new blocks
	buf := make([]byte, ChunkSize)
	manifest := []string{}
	toWrite := map[string][]byte{}
	for {
		n, err := io.ReadFull(r, buf)
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			if n == 0 {
				break
			}
		} else if err != nil && err != io.ErrUnexpectedEOF {
			return err
		}
		chunk := make([]byte, n)
		copy(chunk, buf[:n])
		h := sha256.Sum256(chunk)
		blockIDHex := hex.EncodeToString(h[:])
		manifest = append(manifest, blockIDHex)
		if _, ok := blkIdx.Get(blockIDHex); !ok {
			toWrite[blockIDHex] = chunk
		}
		if err == io.EOF || n < len(buf) {
			break
		}
	}

	// no txn: single-threaded writes are serialized by s.mu; index will be updated after blocks are written

	// write new blocks
	newRefs := map[string]blocks.Ref{}
	for id, chunk := range toWrite {
		h := sha256.Sum256(chunk)
		iv := make([]byte, 12)
		if _, err := rand.Read(iv); err != nil {
			return err
		}
		blockCipher, err := aes.NewCipher(s.key)
		if err != nil {
			return err
		}
		gcm, err := cipher.NewGCM(blockCipher)
		if err != nil {
			return err
		}
		ciphertext := gcm.Seal(nil, iv, chunk, nil)
		checksum := sha256.Sum256(chunk)
		pakPath := s.pakPathForHash(h[:])
		pk, err := pak.OpenOrCreate(pakPath, s.keyHash, ChunkSize)
		if err != nil {
			return err
		}
		off, l, err := pk.AppendRaw(h[:], checksum[:], iv, ciphertext)
		pk.Close()
		if err != nil {
			return err
		}
		newRefs[id] = blocks.Ref{PakPath: pakPath, Offset: off, Length: l}
	}

	// commit refs and increment counts for manifest (new and existing)
	for _, id := range manifest {
		if ref, ok := newRefs[id]; ok {
			if _, err := blkIdx.AddRef(id, ref); err != nil {
				return err
			}
		} else {
			// existing block: increment refcount
			ref, ok := blkIdx.Get(id)
			if !ok {
				return fmt.Errorf("missing block ref for %s", id)
			}
			if _, err := blkIdx.AddRef(id, ref); err != nil {
				return err
			}
		}
	}
	if err := blkIdx.Save(); err != nil {
		return err
	}

	// finally persist new manifest
	if err := s.idx.PutManifest(name, manifest); err != nil {
		return err
	}

	// decrement refs from old manifest that are not present in new manifest
	if len(oldManifest) > 0 {
		newSet := map[string]struct{}{}
		for _, id := range manifest {
			newSet[id] = struct{}{}
		}
		for _, id := range oldManifest {
			if _, keep := newSet[id]; !keep {
				if _, err := blkIdx.DecRef(id); err != nil {
					return err
				}
			}
		}
		if err := blkIdx.Save(); err != nil {
			return err
		}
	}

	// done

	return nil
}

// Get reads a stored file and writes plaintext to w. It reads sequentially from pak according to index entry.
func (s *Store) Get(name string, w io.Writer) error {
	manifest, ok := s.idx.GetManifest(name)
	if !ok {
		return fmt.Errorf("not found")
	}
	blkIdx, err := blocks.Open(filepath.Join(s.root, "blocks.idx"))
	if err != nil {
		return err
	}

	type job struct {
		seq        int
		bid        string
		checksum   []byte
		iv         []byte
		ciphertext []byte
	}
	type result struct {
		seq  int
		data []byte
		err  error
	}

	total := len(manifest)
	workers := MaxVerifyWorkers
	if total < workers {
		workers = total
		if workers < 1 {
			workers = 1
		}
	}

	jobs := make(chan job, workers)
	results := make(chan result, workers)

	// start workers
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for jb := range jobs {
				blockCipher, err := aes.NewCipher(s.key)
				if err != nil {
					results <- result{seq: jb.seq, data: nil, err: err}
					continue
				}
				gcm, err := cipher.NewGCM(blockCipher)
				if err != nil {
					results <- result{seq: jb.seq, data: nil, err: err}
					continue
				}
				plaintext, err := gcm.Open(nil, jb.iv, jb.ciphertext, nil)
				if err != nil {
					results <- result{seq: jb.seq, data: nil, err: err}
					continue
				}
				h := sha256.Sum256(plaintext)
				if !equal(h[:], jb.checksum) {
					results <- result{seq: jb.seq, data: nil, err: fmt.Errorf("checksum mismatch for block %s", jb.bid)}
					continue
				}
				results <- result{seq: jb.seq, data: plaintext, err: nil}
			}
		}()
	}

	// feed jobs by reading ciphertext sequentially (pak.ReadRaw is serialized internally)
	go func() {
		for i, bid := range manifest {
			ref, ok := blkIdx.Get(bid)
			if !ok {
				results <- result{seq: i, data: nil, err: fmt.Errorf("missing block %s", bid)}
				continue
			}
			pk, err := pak.OpenOrCreate(ref.PakPath, s.keyHash, ChunkSize)
			if err != nil {
				results <- result{seq: i, data: nil, err: err}
				continue
			}
			_, checksum, iv, ciphertext, err := pk.ReadRaw(ref.Offset)
			pk.Close()
			if err != nil {
				results <- result{seq: i, data: nil, err: err}
				continue
			}
			jobs <- job{seq: i, bid: bid, checksum: checksum, iv: iv, ciphertext: ciphertext}
		}
		close(jobs)
	}()

	// collect results and write in-order
	go func() {
		wg.Wait()
		close(results)
	}()

	buffer := make(map[int][]byte)
	next := 0
	for res := range results {
		if res.err != nil {
			return res.err
		}
		buffer[res.seq] = res.data
		for {
			if d, ok := buffer[next]; ok {
				if _, err := w.Write(d); err != nil {
					return err
				}
				delete(buffer, next)
				next++
			} else {
				break
			}
		}
	}
	return nil
}

// Verify performs a full manual verification of a stored file by decrypting
// each block and checking its checksum. progress may be nil; if provided it
// will be called with (done, total) after each block is verified.
func (s *Store) Verify(name string, progress func(done, total int)) error {
	manifest, ok := s.idx.GetManifest(name)
	if !ok {
		return fmt.Errorf("not found")
	}
	blkIdx, err := blocks.Open(filepath.Join(s.root, "blocks.idx"))
	if err != nil {
		return err
	}

	type job struct {
		seq        int
		bid        string
		checksum   []byte
		iv         []byte
		ciphertext []byte
	}
	type result struct {
		seq int
		err error
	}

	total := len(manifest)
	workers := MaxVerifyWorkers
	if total < workers {
		workers = total
		if workers < 1 {
			workers = 1
		}
	}

	jobs := make(chan job, workers)
	results := make(chan result, workers)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for jb := range jobs {
				blockCipher, err := aes.NewCipher(s.key)
				if err != nil {
					results <- result{seq: jb.seq, err: err}
					continue
				}
				gcm, err := cipher.NewGCM(blockCipher)
				if err != nil {
					results <- result{seq: jb.seq, err: err}
					continue
				}
				plaintext, err := gcm.Open(nil, jb.iv, jb.ciphertext, nil)
				if err != nil {
					results <- result{seq: jb.seq, err: fmt.Errorf("decryption failed for block %s: %w", jb.bid, err)}
					continue
				}
				h := sha256.Sum256(plaintext)
				if !equal(h[:], jb.checksum) {
					results <- result{seq: jb.seq, err: fmt.Errorf("checksum mismatch for block %s", jb.bid)}
					continue
				}
				results <- result{seq: jb.seq, err: nil}
			}
		}()
	}

	// feed jobs by reading ciphertext sequentially
	go func() {
		for i, bid := range manifest {
			ref, ok := blkIdx.Get(bid)
			if !ok {
				results <- result{seq: i, err: fmt.Errorf("missing block %s", bid)}
				continue
			}
			pk, err := pak.OpenOrCreate(ref.PakPath, s.keyHash, ChunkSize)
			if err != nil {
				results <- result{seq: i, err: err}
				continue
			}
			_, checksum, iv, ciphertext, err := pk.ReadRaw(ref.Offset)
			pk.Close()
			if err != nil {
				results <- result{seq: i, err: err}
				continue
			}
			jobs <- job{seq: i, bid: bid, checksum: checksum, iv: iv, ciphertext: ciphertext}
		}
		close(jobs)
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	done := 0
	for res := range results {
		if res.err != nil {
			return res.err
		}
		done++
		if progress != nil {
			progress(done, total)
		}
	}
	return nil
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Delete removes the manifest for a file (does not immediately free blocks)
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// decrement refs for each block in manifest
	manifest, ok := s.idx.GetManifest(name)
	if !ok {
		return nil
	}
	blkIdx, err := blocks.Open(filepath.Join(s.root, "blocks.idx"))
	if err != nil {
		return err
	}
	for _, id := range manifest {
		if _, err := blkIdx.DecRef(id); err != nil {
			return err
		}
	}
	if err := blkIdx.Save(); err != nil {
		return err
	}
	return s.idx.Delete(name)
}

// List returns all file manifests
func (s *Store) List() map[string][]string {
	return s.idx.List()
}

// Clean removes orphan block entries and deletes pak files that no longer have any referenced blocks.
func (s *Store) Clean() error {
	blkIdx, err := blocks.Open(filepath.Join(s.root, "blocks.idx"))
	if err != nil {
		return err
	}
	counts := blkIdx.Counts()
	// collect blocks with zero count
	zeroBlocks := []string{}
	for id, cnt := range counts {
		if cnt == 0 {
			zeroBlocks = append(zeroBlocks, id)
		}
	}
	// remove entries and delete pak files
	for _, id := range zeroBlocks {
		ref, ok := blkIdx.Get(id)
		if ok {
			// attempt delete pak file if exists and no other block references it
			// delete block entry
			if err := blkIdx.Delete(id); err != nil {
				return err
			}
			// remove pak file if no remaining refs point to it
			still := false
			for _, r := range blkIdx.List() {
				if r.PakPath == ref.PakPath {
					still = true
					break
				}
			}
			if !still {
				_ = os.Remove(ref.PakPath)
				// clean up empty parent directories
				dir := filepath.Dir(ref.PakPath)
				for dir != s.root && dir != "." {
					entries, _ := os.ReadDir(dir)
					if len(entries) == 0 {
						_ = os.Remove(dir)
						dir = filepath.Dir(dir)
						continue
					}
					break
				}
			}
		}
	}
	return blkIdx.Save()
}
