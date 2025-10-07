package session

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type fakeInstanceStorage struct {
	mu     sync.Mutex
	writes [][]byte
}

func (f *fakeInstanceStorage) SaveInstances(data json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	copyData := make([]byte, len(data))
	copy(copyData, data)
	f.writes = append(f.writes, copyData)
	return nil
}

func (f *fakeInstanceStorage) GetInstances() json.RawMessage {
	return json.RawMessage("[]")
}

func (f *fakeInstanceStorage) DeleteAllInstances() error {
	return nil
}

func (f *fakeInstanceStorage) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

func TestStorageDebounceAndFlush(t *testing.T) {
	store := &fakeInstanceStorage{}
	s, err := NewStorage(store)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	s.debounceInterval = 50 * time.Millisecond

	if err := s.SaveInstances(nil); err != nil {
		t.Fatalf("SaveInstances initial: %v", err)
	}
	if got := store.writeCount(); got != 1 {
		t.Fatalf("expected initial write count 1, got %d", got)
	}

	if err := s.SaveInstances(nil); err != nil {
		t.Fatalf("SaveInstances same payload: %v", err)
	}
	if got := store.writeCount(); got != 1 {
		t.Fatalf("expected no additional writes for identical payload, got %d", got)
	}

	instance := &Instance{Title: "example"}
	instance.started = true
	if err := s.SaveInstances([]*Instance{instance}); err != nil {
		t.Fatalf("SaveInstances pending write: %v", err)
	}
	if got := store.writeCount(); got != 1 {
		t.Fatalf("expected pending write to be deferred, got count %d", got)
	}
	if s.pendingData == nil {
		t.Fatal("expected pendingData to be queued")
	}

	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := store.writeCount(); got != 2 {
		t.Fatalf("expected deferred write to flush, got %d", got)
	}
	if s.pendingData != nil {
		t.Fatal("expected pendingData to be cleared after flush")
	}
	if s.debounceTimer != nil {
		t.Fatal("expected debounce timer to be cleared after flush")
	}
}
