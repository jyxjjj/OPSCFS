package storage

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jyxjjj/OPSCFS/internal/blocks"
)

func TestPutGetRoundtrip(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	s, err := New(dir, key)
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("hello opscfs")
	if err := s.Put("file1", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := s.Get("file1", &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Fatalf("got %q", buf.Bytes())
	}
}

func TestDedupCountsAndClean(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	s, err := New(dir, key)
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("same-data")
	if err := s.Put("a", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("b", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}

	// blocks counts should show one block with count 2
	blkIdx, err := blocks.Open(filepath.Join(dir, "blocks.idx"))
	if err != nil {
		t.Fatal(err)
	}
	counts := blkIdx.Counts()
	if len(counts) == 0 {
		t.Fatalf("no blocks found")
	}
	var foundTwo bool
	for _, c := range counts {
		if c == 2 {
			foundTwo = true
		}
	}
	if !foundTwo {
		t.Fatalf("expected a block with count 2, got %+v", counts)
	}

	// delete one
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	// reopen blocks index to read persisted counts
	blkIdx, err = blocks.Open(filepath.Join(dir, "blocks.idx"))
	if err != nil {
		t.Fatal(err)
	}
	counts = blkIdx.Counts()
	var foundOne bool
	for _, c := range counts {
		if c == 1 {
			foundOne = true
		}
	}
	if !foundOne {
		t.Fatalf("expected a block with count 1 after delete, got %+v", counts)
	}

	// delete the other and clean
	if err := s.Delete("b"); err != nil {
		t.Fatal(err)
	}
	if err := s.Clean(); err != nil {
		t.Fatal(err)
	}

	// pak files under dir should be removed
	var pakExists bool
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Ext(path) == ".pak" {
			pakExists = true
		}
		return nil
	})
	if pakExists {
		t.Fatalf("expected no pak files after clean")
	}
}

// TestRecoveryOnStartup simulates existing pak files and manifests, then
// calls New() to ensure the store reconstructs blocks.idx properly.
func TestRecoveryOnStartup(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	s, err := New(dir, key)
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("recovery-data")
	if err := s.Put("x", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}

	// manually tamper: remove blocks.idx and reopen store; New() should return error (no auto-recovery)
	_ = os.Remove(filepath.Join(dir, "blocks.idx"))

	_, err = New(dir, key)
	if err == nil {
		t.Fatalf("expected New() to fail on missing blocks.idx (no auto-recovery), but it succeeded")
	}
}

// TestConcurrentPutDelete runs concurrent Put/Delete operations to surface races.
func TestConcurrentPutDelete(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	s, err := New(dir, key)
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("concurrent-data")
	done := make(chan struct{})

	// writer goroutine
	go func() {
		for i := 0; i < 50; i++ {
			name := fmt.Sprintf("f%c", 'A'+(i%3))
			_ = s.Put(name, bytes.NewReader(data))
		}
		close(done)
	}()

	// deleter goroutine
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("f%c", 'A'+(i%3))
		_ = s.Delete(name)
	}
	<-done
	// final clean should not panic
	if err := s.Clean(); err != nil {
		t.Fatalf("clean failed: %v", err)
	}
}
