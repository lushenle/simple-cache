package raft

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

type Storage struct {
	path string
	mu   sync.Mutex
}

func NewStorage(path string) *Storage { return &Storage{path: path} }

type Meta struct {
	CurrentTerm   uint64   `json:"current_term"`
	VotedFor      string   `json:"voted_for"`
	CommitIndex   uint64   `json:"commit_index"`
	Peers         []string `json:"peers,omitempty"`
	SnapshotIndex uint64   `json:"snapshot_index"`
	SnapshotTerm  uint64   `json:"snapshot_term"`
}

type snapshotFile struct {
	Meta SnapshotMeta `json:"meta"`
	Data []byte       `json:"data"`
}

func (s *Storage) AppendEntry(entry LogEntry) error {
	return s.AppendEntries([]LogEntry{entry})
}

func (s *Storage) AppendEntries(entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			return err
		}
	}

	return f.Sync()
}

func (s *Storage) LoadEntries() ([]LogEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []LogEntry
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSpace(line)
			if len(line) > 0 {
				var entry LogEntry
				if err := json.Unmarshal(line, &entry); err != nil {
					return nil, fmt.Errorf("decode wal entry: %w", err)
				}
				entries = append(entries, entry)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}

	return entries, nil
}

func (s *Storage) RewriteEntries(entries []LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}

	tmpPath := s.path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(f)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return err
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return syncDir(filepath.Dir(s.path))
}

func (s *Storage) TruncateFrom(index uint64) error {
	entries, err := s.LoadEntries()
	if err != nil {
		return err
	}
	if index == 0 {
		return s.RewriteEntries(nil)
	}
	kept := make([]LogEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Index < index {
			kept = append(kept, entry)
		}
	}
	return s.RewriteEntries(kept)
}

func (s *Storage) CompactLog(lastIncludedIndex uint64) error {
	entries, err := s.LoadEntries()
	if err != nil {
		return err
	}
	kept := make([]LogEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Index > lastIncludedIndex {
			kept = append(kept, entry)
		}
	}
	return s.RewriteEntries(kept)
}

func (s *Storage) metaPath() string     { return s.path + ".meta" }
func (s *Storage) snapshotPath() string { return s.path + ".snapshot" }

func (s *Storage) LoadMeta() (*Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mp := s.metaPath()
	f, err := os.Open(mp)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Meta{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var m Meta
	dec := json.NewDecoder(f)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("corrupt raft meta file %s: %w", mp, err)
	}
	return &m, nil
}

func (s *Storage) SaveMeta(m *Meta) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	mp := s.metaPath()
	if err := os.MkdirAll(filepath.Dir(mp), 0o755); err != nil {
		return err
	}

	tmpPath := mp + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(f)
	if err := enc.Encode(m); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, mp); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return syncDir(filepath.Dir(mp))
}

func (s *Storage) SaveSnapshot(meta SnapshotMeta, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.snapshotPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tmpPath := path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(snapshotFile{
		Meta: meta,
		Data: data,
	})
	if err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return syncDir(filepath.Dir(path))
}

func (s *Storage) LoadSnapshot() (*SnapshotMeta, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.snapshotPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer f.Close()

	var meta SnapshotMeta
	var payload snapshotFile
	dec := json.NewDecoder(f)
	if err := dec.Decode(&payload); err != nil {
		return nil, nil, err
	}
	meta = payload.Meta
	data := payload.Data
	if len(data) == 0 {
		return &meta, nil, nil
	}
	return &meta, data, nil
}

func (s *Storage) HasSnapshot() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := os.Stat(s.snapshotPath())
	return err == nil
}

func syncDir(dir string) error {
	df, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer df.Close()
	return df.Sync()
}
