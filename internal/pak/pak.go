package pak

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const (
	Magic   = "OPSCFSPAK"
	Version = 1
)

// FileHeader layout:
// Magic (8 bytes) | Version (1 byte) | keyHash (32 bytes) | blockSize (4 bytes little)

var mu sync.Mutex

type Pak struct {
	Path string
	f    *os.File
}

func OpenOrCreate(path string, keyHash []byte, blockSize uint32) (*Pak, error) {
	mu.Lock()
	defer mu.Unlock()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	// if file is new (size 0) write header
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if fi.Size() == 0 {
		if len(keyHash) != sha256.Size {
			f.Close()
			return nil, errors.New("invalid key hash length")
		}
		// write header
		if _, err := f.Write([]byte(Magic)); err != nil {
			f.Close()
			return nil, err
		}
		if _, err := f.Write([]byte{byte(Version)}); err != nil {
			f.Close()
			return nil, err
		}
		if _, err := f.Write(keyHash); err != nil {
			f.Close()
			return nil, err
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], blockSize)
		if _, err := f.Write(b[:]); err != nil {
			f.Close()
			return nil, err
		}
	}
	// if file existed, verify header
	if fi.Size() > 0 {
		// read and validate header
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
		magicLen := len(Magic)
		hdr := make([]byte, magicLen+1+sha256.Size+4)
		if _, err := io.ReadFull(f, hdr); err != nil {
			f.Close()
			return nil, err
		}
		if string(hdr[0:magicLen]) != Magic {
			f.Close()
			return nil, errors.New("invalid pak magic")
		}
		if hdr[magicLen] != byte(Version) {
			f.Close()
			return nil, errors.New("pak version mismatch")
		}
		if !equalBytes(hdr[magicLen+1:magicLen+1+sha256.Size], keyHash) {
			f.Close()
			return nil, errors.New("pak key hash mismatch")
		}
		bs := binary.LittleEndian.Uint32(hdr[magicLen+1+sha256.Size:])
		if bs != blockSize {
			f.Close()
			return nil, errors.New("pak block size mismatch")
		}
	}
	return &Pak{Path: path, f: f}, nil
}

func (p *Pak) Close() error {
	if p.f == nil {
		return nil
	}
	return p.f.Close()
}

// AppendRaw appends raw bytes (already encrypted) with metadata: blockID (32 bytes), checksum (32 bytes), iv (12 bytes), ciphertextLen (4), ciphertext
// returns the offset at which block header starts and total bytes written
func (p *Pak) AppendRaw(blockID, checksum, iv []byte, ciphertext []byte) (int64, int64, error) {
	if len(blockID) != sha256.Size || len(checksum) != sha256.Size {
		return 0, 0, fmt.Errorf("invalid id/checksum length")
	}
	if len(iv) != 12 {
		return 0, 0, fmt.Errorf("invalid iv length")
	}
	mu.Lock()
	defer mu.Unlock()
	off, err := p.f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, 0, err
	}
	// write block header
	if _, err := p.f.Write(blockID); err != nil {
		return 0, 0, err
	}
	if _, err := p.f.Write(checksum); err != nil {
		return 0, 0, err
	}
	if _, err := p.f.Write(iv); err != nil {
		return 0, 0, err
	}
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(len(ciphertext)))
	if _, err := p.f.Write(b[:]); err != nil {
		return 0, 0, err
	}
	n, err := p.f.Write(ciphertext)
	if err != nil {
		return 0, 0, err
	}
	return off, int64(32 + 32 + 12 + 4 + n), nil
}

// ReadRaw reads a block at offset; returns blockID, checksum, iv, ciphertext
func (p *Pak) ReadRaw(offset int64) (blockID, checksum, iv, ciphertext []byte, err error) {
	mu.Lock()
	defer mu.Unlock()
	if _, err = p.f.Seek(offset, io.SeekStart); err != nil {
		return
	}
	blockID = make([]byte, sha256.Size)
	if _, err = io.ReadFull(p.f, blockID); err != nil {
		return
	}
	checksum = make([]byte, sha256.Size)
	if _, err = io.ReadFull(p.f, checksum); err != nil {
		return
	}
	iv = make([]byte, 12)
	if _, err = io.ReadFull(p.f, iv); err != nil {
		return
	}
	var b [4]byte
	if _, err = io.ReadFull(p.f, b[:]); err != nil {
		return
	}
	l := int(binary.LittleEndian.Uint32(b[:]))
	ciphertext = make([]byte, l)
	if _, err = io.ReadFull(p.f, ciphertext); err != nil {
		return
	}
	return
}

func equalBytes(a, b []byte) bool {
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
