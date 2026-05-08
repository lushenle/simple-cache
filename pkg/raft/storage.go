package raft

import (
	"bufio"
	"bytes"
	"encoding/binary"
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

// binaryWALVersion marks the binary format version.
const binaryWALVersion byte = 1

// encodeEntryBinary serializes a single log entry to binary.
func encodeEntryBinary(e LogEntry) []byte {
	buf := make([]byte, 0, 32+len(e.Type)+len(e.Data))
	buf = binary.BigEndian.AppendUint64(buf, e.Index)
	buf = binary.BigEndian.AppendUint64(buf, e.Term)
	typeStr := string(e.Type)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(typeStr)))
	buf = append(buf, typeStr...)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(e.Data)))
	buf = append(buf, e.Data...)
	return buf
}

// decodeEntryBinary decodes a single log entry from binary data.
func decodeEntryBinary(data []byte) (LogEntry, []byte, error) {
	if len(data) < 20 {
		return LogEntry{}, data, fmt.Errorf("entry too short: %d bytes", len(data))
	}
	index := binary.BigEndian.Uint64(data[0:8])
	term := binary.BigEndian.Uint64(data[8:16])
	typeLen := binary.BigEndian.Uint32(data[16:20])
	offset := 20
	if uint64(len(data)) < uint64(offset)+uint64(typeLen)+4 {
		return LogEntry{}, data, fmt.Errorf("truncated entry type")
	}
	typeStr := string(data[offset : offset+int(typeLen)])
	offset += int(typeLen)
	if len(data) < offset+4 {
		return LogEntry{}, data, fmt.Errorf("truncated entry data length")
	}
	dataLen := binary.BigEndian.Uint32(data[offset : offset+4])
	offset += 4
	if uint64(len(data)) < uint64(offset)+uint64(dataLen) {
		return LogEntry{}, data, fmt.Errorf("truncated entry data")
	}
	entry := LogEntry{
		Index: index,
		Term:  term,
		Type:  EntryType(typeStr),
		Data:  append([]byte(nil), data[offset:offset+int(dataLen)]...),
	}
	offset += int(dataLen)
	return entry, data[offset:], nil
}

// appendEntriesBinary writes entries in binary format to the WAL file.
func appendEntriesBinary(f *os.File, entries []LogEntry) error {
	for _, entry := range entries {
		b := encodeEntryBinary(entry)
		if _, err := f.Write(b); err != nil {
			return err
		}
	}
	return nil
}

// isBinaryWAL checks if the given data starts with the binary format marker.
// JSON format always starts with '{'; binary starts with a non-ASCII version byte.
func isBinaryWAL(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	return data[0] != '{'
}

// loadEntriesBinary reads all entries from binary WAL data.
func loadEntriesBinary(data []byte) ([]LogEntry, error) {
	var entries []LogEntry
	remaining := data
	for len(remaining) > 0 {
		entry, rest, err := decodeEntryBinary(remaining)
		if err != nil {
			return entries, err
		}
		entries = append(entries, entry)
		remaining = rest
	}
	return entries, nil
}

// loadEntriesJSON reads all entries from JSONL WAL data.
func loadEntriesJSON(data []byte) ([]LogEntry, error) {
	var entries []LogEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var entry LogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("decode wal entry: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
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

	if err := appendEntriesBinary(f, entries); err != nil {
		return err
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

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}

	if isBinaryWAL(data) {
		return loadEntriesBinary(data)
	}
	return loadEntriesJSON(data)
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

	if err := appendEntriesBinary(f, entries); err != nil {
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
