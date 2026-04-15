package cache

import (
	"container/heap"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/armon/go-radix"
	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

const (
	dumpMagic   = "SCDF"
	dumpVersion = 1
)

// DumpEntry represents a single cache entry in the dump file.
type DumpEntry struct {
	Key           string `json:"key"`
	Value         string `json:"value"`
	ValueType     string `json:"value_type"`
	Expiration    string `json:"expiration,omitempty"`
	HasExpiration bool   `json:"has_expiration"`
}

// DumpJSON is the top-level JSON dump structure.
type DumpJSON struct {
	Version      int          `json:"version"`
	NodeID       string       `json:"node_id"`
	DumpedAt     string       `json:"dumped_at"`
	TotalKeys    int          `json:"total_keys"`
	ExpiredKeys  int          `json:"expired_keys"`
	Entries      []DumpEntry  `json:"entries"`
}

// DumpResult contains the result of a dump operation.
type DumpResult struct {
	Success    bool
	TotalKeys  int32
	FileSize   int64
	Path       string
	Format     string
	DurationMs float64
}

// LoadResult contains the result of a load operation.
type LoadResult struct {
	Success      bool
	TotalKeys    int32
	LoadedKeys   int32
	SkippedKeys  int32
	Path         string
	DurationMs   float64
}

// Dump exports all cache data to a file.
// format: "binary" or "json"
// path: file path, empty means default (data/cache-{nodeID}.dump)
func (c *Cache) Dump(nodeID, format, path string) (*DumpResult, error) {
	switch format {
	case "":
		format = "binary"
	case "binary", "json":
	default:
		metrics.IncPersistenceOp("dump", "error")
		return nil, fmt.Errorf("unsupported dump format: %s", format)
	}

	c.logger.Info("starting cache dump", zap.String("format", format), zap.String("path", path))

	start := time.Now()
	if path == "" {
		path = fmt.Sprintf("data/cache-%s.dump", nodeID)
		if format == "json" {
			path += ".json"
		}
	}

	c.mu.Lock(metrics.LockWrite)
	defer c.mu.Unlock()

	// Collect all entries
	entries := make([]DumpEntry, 0, len(c.items))
	now := time.Now()
	expiredCount := 0
	for key, item := range c.items {
		// Skip already-expired entries
		if !item.expiration.IsZero() && now.After(item.expiration) {
			expiredCount++
			continue
		}

		val, valType := serializeValue(item.value)
		entry := DumpEntry{
			Key:           key,
			Value:         val,
			ValueType:     valType,
			HasExpiration: !item.expiration.IsZero(),
		}
		if entry.HasExpiration {
			entry.Expiration = item.expiration.UTC().Format(time.RFC3339Nano)
		}
		entries = append(entries, entry)
	}

	// Sort entries by key for deterministic output
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})

	var data []byte
	var err error

	switch format {
	case "json":
		dump := DumpJSON{
			Version:     dumpVersion,
			NodeID:      nodeID,
			DumpedAt:    time.Now().UTC().Format(time.RFC3339Nano),
			TotalKeys:   len(entries),
			ExpiredKeys: expiredCount,
			Entries:     entries,
		}
		data, err = json.MarshalIndent(dump, "", "  ")
	default:
		data, err = encodeBinaryDump(entries)
	}

	if err != nil {
		metrics.IncPersistenceOp("dump", "error")
		return nil, fmt.Errorf("serialize dump data: %w", err)
	}

	// Atomic write: write to temp file, then rename
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		metrics.IncPersistenceOp("dump", "error")
		return nil, fmt.Errorf("create directory %s: %w", dir, err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		metrics.IncPersistenceOp("dump", "error")
		return nil, fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		metrics.IncPersistenceOp("dump", "error")
		return nil, fmt.Errorf("rename temp file: %w", err)
	}

	durationMs := float64(time.Since(start).Microseconds()) / 1000.0
	metrics.IncPersistenceOp("dump", "success")
	metrics.ObservePersistenceDuration("dump", time.Since(start).Seconds())
	metrics.SetDumpKeys(int64(len(entries)))

	c.logger.Info("cache dump completed",
		zap.String("path", path),
		zap.String("format", format),
		zap.Int("total_keys", len(entries)),
		zap.Int("expired_skipped", expiredCount),
		zap.Duration("duration", time.Since(start)),
	)

	return &DumpResult{
		Success:    true,
		TotalKeys:  int32(len(entries)),
		FileSize:   int64(len(data)),
		Path:       path,
		Format:     format,
		DurationMs: durationMs,
	}, nil
}

// Load imports cache data from a file.
// path: file path, empty means auto-detect default (tries binary first, then json)
func (c *Cache) Load(nodeID, path string) (*LoadResult, error) {
	c.logger.Info("starting cache load", zap.String("path", path))

	start := time.Now()

	// Auto-detect path if not specified
	if path == "" {
		binPath := fmt.Sprintf("data/cache-%s.dump", nodeID)
		jsonPath := fmt.Sprintf("data/cache-%s.dump.json", nodeID)

		// Prefer binary, fall back to json
		if _, err := os.Stat(binPath); err == nil {
			path = binPath
		} else if _, err := os.Stat(jsonPath); err == nil {
			path = jsonPath
		} else {
			c.logger.Info("no dump file found, skipping load")
			return &LoadResult{Success: true, Path: "none"}, nil
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		metrics.IncPersistenceOp("load", "error")
		return nil, fmt.Errorf("read dump file: %w", err)
	}

	var entries []DumpEntry
	format := "unknown"

	// Detect format
	if len(data) >= 4 && string(data[:4]) == dumpMagic {
		entries, err = decodeBinaryDump(data)
		format = "binary"
	} else {
		var dump DumpJSON
		if err := json.Unmarshal(data, &dump); err != nil {
			metrics.IncPersistenceOp("load", "error")
			return nil, fmt.Errorf("parse dump file: %w", err)
		}
		entries = dump.Entries
		format = "json"
	}

	if err != nil {
		metrics.IncPersistenceOp("load", "error")
		return nil, fmt.Errorf("parse dump file: %w", err)
	}

	c.mu.Lock(metrics.LockWrite)
	defer c.mu.Unlock()

	// Clear existing data before loading (inline reset to avoid deadlock with separate Reset() call)
	c.items = make(map[string]*Item)
	c.prefixTree = radix.New()
	c.expirationHeap = &ExpirationHeap{onSwap: c.expirationHeap.onSwap}
	heap.Init(c.expirationHeap)
	c.expirationIndex = make(map[string]int)

	now := time.Now()
	loaded := 0
	skipped := 0

	for _, entry := range entries {
		var expiration time.Time
		if entry.HasExpiration && entry.Expiration != "" {
			exp, err := time.Parse(time.RFC3339Nano, entry.Expiration)
			if err != nil {
				c.logger.Warn("skip entry with invalid expiration", zap.String("key", entry.Key), zap.Error(err))
				skipped++
				continue
			}
			expiration = exp

			// Skip already-expired entries
			if now.After(expiration) {
				skipped++
				continue
			}
		}

		value := deserializeValue(entry.Value, entry.ValueType)

		// Use setInternal to add to all data structures
		c.setInternal(entry.Key, &Item{
			value:      value,
			expiration: expiration,
		})

		if !expiration.IsZero() {
			heap.Push(c.expirationHeap, &expirationEntry{
				key:        entry.Key,
				expiration: expiration,
			})
		}

		loaded++
	}

	metrics.UpdateKeysTotal(len(c.items))
	metrics.UpdateExpirationHeapSize(c.expirationHeap.Len())
	metrics.IncPersistenceOp("load", "success")
	metrics.ObservePersistenceDuration("load", time.Since(start).Seconds())
	metrics.SetLoadKeys(int64(loaded), int64(skipped))

	durationMs := float64(time.Since(start).Microseconds()) / 1000.0
	c.logger.Info("cache load completed",
		zap.String("path", path),
		zap.String("format", format),
		zap.Int("loaded", loaded),
		zap.Int("skipped", skipped),
		zap.Duration("duration", time.Since(start)),
	)

	return &LoadResult{
		Success:      true,
		TotalKeys:    int32(len(entries)),
		LoadedKeys:   int32(loaded),
		SkippedKeys:  int32(skipped),
		Path:         path,
		DurationMs:   durationMs,
	}, nil
}

// DefaultDumpPath returns the default dump file path for the given nodeID and format.
func DefaultDumpPath(nodeID, format, dataDir string) string {
	if dataDir == "" {
		dataDir = "data"
	}
	path := filepath.Join(dataDir, fmt.Sprintf("cache-%s.dump", nodeID))
	if format == "json" {
		path += ".json"
	}
	return path
}

// --- Binary format encoding/decoding ---

func encodeBinaryDump(entries []DumpEntry) ([]byte, error) {
	buf := make([]byte, 0, 16+len(entries)*100)

	// Header
	buf = append(buf, dumpMagic...)
	buf = binary.BigEndian.AppendUint32(buf, dumpVersion)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(entries)))
	buf = binary.BigEndian.AppendUint32(buf, 0) // flags

	// Entries
	for _, e := range entries {
		// Key
		keyBytes := []byte(e.Key)
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(keyBytes)))
		buf = append(buf, keyBytes...)

		// Value
		valBytes := []byte(e.Value)
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(valBytes)))
		buf = append(buf, valBytes...)

		// Expiration
		var expUnix int64
		if e.HasExpiration && e.Expiration != "" {
			t, err := time.Parse(time.RFC3339Nano, e.Expiration)
			if err == nil {
				expUnix = t.UnixNano()
			}
		}
		buf = binary.BigEndian.AppendUint64(buf, uint64(expUnix))

		// HasExpiration
		if e.HasExpiration {
			buf = append(buf, 1)
		} else {
			buf = append(buf, 0)
		}
	}

	// Footer: CRC32
	checksum := crc32.ChecksumIEEE(buf)
	buf = binary.BigEndian.AppendUint32(buf, checksum)
	buf = binary.BigEndian.AppendUint32(buf, 0) // padding

	return buf, nil
}

func decodeBinaryDump(data []byte) ([]DumpEntry, error) {
	if len(data) < 16 {
		return nil, fmt.Errorf("file too small: %d bytes", len(data))
	}

	// Verify magic
	if string(data[:4]) != dumpMagic {
		return nil, fmt.Errorf("invalid magic: %s", string(data[:4]))
	}

	version := binary.BigEndian.Uint32(data[4:8])
	if version != dumpVersion {
		return nil, fmt.Errorf("unsupported version: %d", version)
	}

	count := binary.BigEndian.Uint32(data[8:12])
	// data[12:16] = flags

	entries := make([]DumpEntry, 0, count)
	offset := 16

	// Footer is last 8 bytes
	footerStart := len(data) - 8

	for i := uint32(0); i < count && offset < footerStart; i++ {
		// Key
		if offset+4 > footerStart {
			break
		}
		keyLen := binary.BigEndian.Uint32(data[offset : offset+4])
		offset += 4
		if offset+int(keyLen) > footerStart {
			break
		}
		key := string(data[offset : offset+int(keyLen)])
		offset += int(keyLen)

		// Value
		if offset+4 > footerStart {
			break
		}
		valLen := binary.BigEndian.Uint32(data[offset : offset+4])
		offset += 4
		if offset+int(valLen) > footerStart {
			break
		}
		value := string(data[offset : offset+int(valLen)])
		offset += int(valLen)

		// Expiration
		if offset+8 > footerStart {
			break
		}
		expUnix := int64(binary.BigEndian.Uint64(data[offset : offset+8]))
		offset += 8

		// HasExpiration
		if offset+1 > footerStart {
			break
		}
		hasExp := data[offset] == 1
		offset += 1

		entry := DumpEntry{
			Key:           key,
			Value:         value,
			ValueType:     "string", // binary format doesn't preserve type info
			HasExpiration: hasExp,
		}
		if hasExp && expUnix != 0 {
			entry.Expiration = time.Unix(0, expUnix).UTC().Format(time.RFC3339Nano)
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// --- Value serialization helpers ---

func serializeValue(v any) (string, string) {
	if v == nil {
		return "", "nil"
	}
	switch val := v.(type) {
	case string:
		return val, "string"
	case []byte:
		return string(val), "bytes" // Note: lossy for non-UTF-8 bytes
	default:
		// Try JSON marshal for complex types
		b, err := json.Marshal(val)
		if err == nil {
			return string(b), "json"
		}
		return fmt.Sprintf("%v", val), "other"
	}
}

func deserializeValue(data, valueType string) any {
	switch valueType {
	case "bytes":
		return []byte(data)
	case "json", "other":
		return data // Return as string
	default:
		return data
	}
}
