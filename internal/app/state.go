package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type StateStore struct {
	dir string
	mu  sync.Mutex
}

type offsetState struct {
	Offset    int64     `json:"offset"`
	UpdatedAt time.Time `json:"updated_at"`
}

type jobHistoryState struct {
	Jobs      []JobSnapshot `json:"jobs"`
	UpdatedAt time.Time     `json:"updated_at"`
}

func NewStateStore(dir string) (*StateStore, error) {
	dir, err := filepath.Abs(strings.TrimSpace(dir))
	if err != nil {
		return nil, fmt.Errorf("resolve state dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	return &StateStore{dir: dir}, nil
}

func (s *StateStore) LoadOffset(ctx context.Context) (int64, error) {
	if s == nil {
		return 0, nil
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var state offsetState
	if err := s.readJSON(s.offsetPath(), &state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("load update offset: %w", err)
	}
	if state.Offset < 0 {
		return 0, fmt.Errorf("load update offset: negative offset %d", state.Offset)
	}
	return state.Offset, nil
}

func (s *StateStore) SaveOffset(ctx context.Context, offset int64) error {
	if s == nil || offset <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return s.writeJSON(s.offsetPath(), offsetState{
		Offset:    offset,
		UpdatedAt: time.Now().UTC(),
	}, 0600)
}

func (s *StateStore) LoadJobHistory(ctx context.Context) ([]JobSnapshot, error) {
	if s == nil {
		return nil, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var state jobHistoryState
	if err := s.readJSON(s.jobsPath(), &state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("load job history: %w", err)
	}
	return state.Jobs, nil
}

func (s *StateStore) SaveJobHistory(jobs []JobSnapshot) error {
	if s == nil {
		return nil
	}
	return s.writeJSON(s.jobsPath(), jobHistoryState{
		Jobs:      jobs,
		UpdatedAt: time.Now().UTC(),
	}, 0600)
}

func (s *StateStore) offsetPath() string {
	return filepath.Join(s.dir, "telegram-offset.json")
}

func (s *StateStore) jobsPath() string {
	return filepath.Join(s.dir, "job-history.json")
}

func (s *StateStore) readJSON(path string, out any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	return nil
}

func (s *StateStore) writeJSON(path string, value any, perm os.FileMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tmp, err := os.CreateTemp(s.dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpPath := tmp.Name()
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod temp state file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}
