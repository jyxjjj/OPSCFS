package blocks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

type Ref struct {
	PakPath string `json:"pak_path"`
	Offset  int64  `json:"offset"`
	Length  int64  `json:"length"`
}

type Blocks struct {
	path string
	mu   sync.RWMutex
	refs map[string]Ref // blockID(hex) -> ref
	cnts map[string]int // blockID(hex) -> refcount
}

func Open(path string) (*Blocks, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	b := &Blocks{path: path, refs: map[string]Ref{}, cnts: map[string]int{}}
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		data, err := io.ReadAll(f)
		if err == nil && len(data) > 0 {
			var doc struct {
				Refs map[string]Ref `json:"refs"`
				Cnts map[string]int `json:"counts"`
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				return nil, fmt.Errorf("blocks index parse: %w", err)
			}
			if doc.Refs != nil {
				b.refs = doc.Refs
			}
			if doc.Cnts != nil {
				b.cnts = doc.Cnts
			}
		}
	}
	return b, nil
}

func (b *Blocks) Save() error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	tmp := b.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	doc := struct {
		Refs map[string]Ref `json:"refs"`
		Cnts map[string]int `json:"counts"`
	}{Refs: b.refs, Cnts: b.cnts}
	if err := enc.Encode(&doc); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(tmp, b.path)
}

func (b *Blocks) Get(id string) (Ref, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	r, ok := b.refs[id]
	return r, ok
}

// AddRef sets ref if missing and increments reference count. Returns the current count after increment.
func (b *Blocks) AddRef(id string, r Ref) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.refs[id]; !ok {
		b.refs[id] = r
	}
	b.cnts[id] = b.cnts[id] + 1
	if err := b.saveLocked(); err != nil {
		return 0, err
	}
	return b.cnts[id], nil
}

// DecRef decrements reference count and removes ref when it reaches zero. Returns remaining count.
func (b *Blocks) DecRef(id string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cnt := b.cnts[id]
	if cnt <= 1 {
		// Instead of removing the ref immediately, set count to 0 and keep the ref
		// so that a subsequent Clean() can find the pak path and remove the file.
		b.cnts[id] = 0
		if err := b.saveLocked(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	b.cnts[id] = cnt - 1
	if err := b.saveLocked(); err != nil {
		return 0, err
	}
	return b.cnts[id], nil
}

// saveLocked writes file; caller must hold b.mu write lock
func (b *Blocks) saveLocked() error {
	tmp := b.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	doc := struct {
		Refs map[string]Ref `json:"refs"`
		Cnts map[string]int `json:"counts"`
	}{Refs: b.refs, Cnts: b.cnts}
	if err := enc.Encode(&doc); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(tmp, b.path)
}

func (b *Blocks) Delete(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.cnts, id)
	delete(b.refs, id)
	return b.saveLocked()
}

func (b *Blocks) List() map[string]Ref {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]Ref, len(b.refs))
	for k, v := range b.refs {
		out[k] = v
	}
	return out
}

// Counts returns a copy of counts map
func (b *Blocks) Counts() map[string]int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]int, len(b.cnts))
	for k, v := range b.cnts {
		out[k] = v
	}
	return out
}

// Reconcile replaces refs and counts with the provided maps and persists them.
// This is intended for recovery: the caller should have scanned pak files
// and computed counts from file manifests, then call Reconcile to atomically
// update the on-disk blocks index.
func (b *Blocks) Reconcile(refs map[string]Ref, counts map[string]int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	// copy maps to internal state
	b.refs = make(map[string]Ref, len(refs))
	for k, v := range refs {
		b.refs[k] = v
	}
	b.cnts = make(map[string]int, len(counts))
	for k, v := range counts {
		b.cnts[k] = v
	}
	return b.saveLocked()
}
