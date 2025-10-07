package session

import (
	"agent-squad/config"
	"agent-squad/log"
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// InstanceData represents the serializable data of an Instance
type InstanceData struct {
	Title     string    `json:"title"`
	Path      string    `json:"path"`
	Branch    string    `json:"branch"`
	Status    Status    `json:"status"`
	Height    int       `json:"height"`
	Width     int       `json:"width"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	AutoYes   bool      `json:"auto_yes"`

	Program   string          `json:"program"`
	Worktree  GitWorktreeData `json:"worktree"`
	DiffStats DiffStatsData   `json:"diff_stats"`
}

// GitWorktreeData represents the serializable data of a GitWorktree
type GitWorktreeData struct {
	RepoPath      string `json:"repo_path"`
	WorktreePath  string `json:"worktree_path"`
	SessionName   string `json:"session_name"`
	BranchName    string `json:"branch_name"`
	BaseCommitSHA string `json:"base_commit_sha"`
}

// DiffStatsData represents the serializable data of a DiffStats
type DiffStatsData struct {
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Content string `json:"content"`
}

// Storage handles saving and loading instances using the state interface
type Storage struct {
	state            config.InstanceStorage
	mu               sync.Mutex
	lastSavedData    []byte
	lastSaveTime     time.Time
	debounceInterval time.Duration
	pendingData      []byte
	debounceTimer    *time.Timer
}

// NewStorage creates a new storage instance
func NewStorage(state config.InstanceStorage) (*Storage, error) {
	return &Storage{
		state:            state,
		debounceInterval: 5 * time.Second,
	}, nil
}

// SaveInstances saves the list of instances to disk
func (s *Storage) SaveInstances(instances []*Instance) error {
	// Convert instances to InstanceData
	data := make([]InstanceData, 0)
	for _, instance := range instances {
		if instance.Started() {
			data = append(data, instance.ToInstanceData())
		}
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal instances: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if bytes.Equal(jsonData, s.lastSavedData) && s.pendingData == nil {
		return nil
	}

	now := time.Now()
	if s.lastSaveTime.IsZero() || now.Sub(s.lastSaveTime) >= s.debounceInterval {
		if err := s.writeLocked(jsonData); err != nil {
			return err
		}
		s.trackImmediateSave(jsonData, now)
		return nil
	}

	s.pendingData = cloneBytes(jsonData)
	if s.debounceTimer == nil {
		delay := s.debounceInterval - now.Sub(s.lastSaveTime)
		if delay < time.Second {
			delay = time.Second
		}
		s.debounceTimer = time.AfterFunc(delay, s.flushPending)
	}

	return nil
}

// LoadInstances loads the list of instances from disk
func (s *Storage) LoadInstances() ([]*Instance, error) {
	jsonData := s.state.GetInstances()

	var instancesData []InstanceData
	if err := json.Unmarshal(jsonData, &instancesData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal instances: %w", err)
	}

	instances := make([]*Instance, len(instancesData))
	for i, data := range instancesData {
		instance, err := FromInstanceData(data)
		if err != nil {
			return nil, fmt.Errorf("failed to create instance %s: %w", data.Title, err)
		}
		instances[i] = instance
	}

	return instances, nil
}

// DeleteInstance removes an instance from storage
func (s *Storage) DeleteInstance(title string) error {
	instances, err := s.LoadInstances()
	if err != nil {
		return fmt.Errorf("failed to load instances: %w", err)
	}

	found := false
	newInstances := make([]*Instance, 0)
	for _, instance := range instances {
		data := instance.ToInstanceData()
		if data.Title != title {
			newInstances = append(newInstances, instance)
		} else {
			found = true
		}
	}

	if !found {
		return fmt.Errorf("instance not found: %s", title)
	}

	return s.SaveInstances(newInstances)
}

// UpdateInstance updates an existing instance in storage
func (s *Storage) UpdateInstance(instance *Instance) error {
	instances, err := s.LoadInstances()
	if err != nil {
		return fmt.Errorf("failed to load instances: %w", err)
	}

	data := instance.ToInstanceData()
	found := false
	for i, existing := range instances {
		existingData := existing.ToInstanceData()
		if existingData.Title == data.Title {
			instances[i] = instance
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("instance not found: %s", data.Title)
	}

	return s.SaveInstances(instances)
}

// DeleteAllInstances removes all stored instances
func (s *Storage) DeleteAllInstances() error {
	return s.state.DeleteAllInstances()
}

// Flush synchronously writes any pending instance data to disk.
func (s *Storage) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.pendingData) == 0 {
		return nil
	}

	data := cloneBytes(s.pendingData)
	if err := s.writeLocked(data); err != nil {
		return err
	}
	s.lastSavedData = data
	s.lastSaveTime = time.Now()
	s.pendingData = nil
	if s.debounceTimer != nil {
		s.debounceTimer.Stop()
		s.debounceTimer = nil
	}
	return nil
}

func (s *Storage) trackImmediateSave(data []byte, now time.Time) {
	s.lastSavedData = cloneBytes(data)
	s.lastSaveTime = now
	s.pendingData = nil
	if s.debounceTimer != nil {
		s.debounceTimer.Stop()
		s.debounceTimer = nil
	}
}

func (s *Storage) flushPending() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.pendingData) == 0 {
		s.debounceTimer = nil
		return
	}

	data := cloneBytes(s.pendingData)
	if err := s.writeLocked(data); err != nil {
		if log.WarningLog != nil {
			log.WarningLog.Printf("failed to flush pending instances: %v", err)
		}
		s.debounceTimer = time.AfterFunc(s.debounceInterval, s.flushPending)
		return
	}

	s.lastSavedData = data
	s.lastSaveTime = time.Now()
	s.pendingData = nil
	s.debounceTimer = nil
}

func (s *Storage) writeLocked(data []byte) error {
	clone := cloneBytes(data)
	return s.state.SaveInstances(json.RawMessage(clone))
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}
