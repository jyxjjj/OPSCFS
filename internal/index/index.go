package index

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Index holds mapping from filename -> manifest (list of blockIDs)
type Index struct {
	path  string
	mu    sync.RWMutex
	files map[string][]string
}

func Open(path string) (*Index, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	idx := &Index{path: path, files: map[string][]string{}}
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		data, err := io.ReadAll(f)
		if err == nil && len(data) > 0 {
			if err := json.Unmarshal(data, &idx.files); err != nil {
				return nil, fmt.Errorf("failed to parse index: %w", err)
			}
		}
	}
	return idx, nil
}

func (ix *Index) Save() error {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	tmp := ix.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(ix.files); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(tmp, ix.path)
}

func (ix *Index) PutManifest(name string, manifest []string) error {
	ix.mu.Lock()
	ix.files[name] = manifest
	ix.mu.Unlock()
	return ix.Save()
}

func (ix *Index) GetManifest(name string) ([]string, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	m, ok := ix.files[name]
	return m, ok
}

func (ix *Index) Delete(name string) error {
	ix.mu.Lock()
	delete(ix.files, name)
	ix.mu.Unlock()
	return ix.Save()
}

func (ix *Index) List() map[string][]string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make(map[string][]string, len(ix.files))
	for k, v := range ix.files {
		out[k] = append([]string(nil), v...)
	}
	return out
}
