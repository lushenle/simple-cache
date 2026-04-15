package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestDumpAndLoadBinary(t *testing.T) {
	logger := zap.NewNop()
	c := New(time.Minute, logger)
	defer c.Close()

	// Set some data
	require.NoError(t, c.Set("key1", "value1", "1h"))
	require.NoError(t, c.Set("key2", "value2", "")) // no expiration
	require.NoError(t, c.Set("key3", "value3", "1ns")) // already expired

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cache-node1.dump")

	// Dump
	result, err := c.Dump("node1", "binary", path)
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, int32(2), result.TotalKeys) // key3 expired, skipped
	assert.Greater(t, result.FileSize, int64(0))
	assert.Equal(t, "binary", result.Format)
	assert.FileExists(t, path)

	// Load into a new cache
	c2 := New(time.Minute, logger)
	defer c2.Close()

	loadResult, err := c2.Load("node1", path)
	require.NoError(t, err)
	assert.Equal(t, int32(2), loadResult.TotalKeys)
	assert.Equal(t, int32(2), loadResult.LoadedKeys)
	assert.Equal(t, int32(0), loadResult.SkippedKeys)

	// Verify data
	val, found := c2.Get("key1")
	assert.True(t, found)
	assert.Equal(t, "value1", val)

	val, found = c2.Get("key2")
	assert.True(t, found)
	assert.Equal(t, "value2", val)

	_, found = c2.Get("key3")
	assert.False(t, found) // expired, not loaded
}

func TestDumpAndLoadJSON(t *testing.T) {
	logger := zap.NewNop()
	c := New(time.Minute, logger)
	defer c.Close()

	require.NoError(t, c.Set("key1", "value1", "1h"))
	require.NoError(t, c.Set("key2", "value2", ""))

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cache-node1.dump.json")

	// Dump
	result, err := c.Dump("node1", "json", path)
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, int32(2), result.TotalKeys)
	assert.Equal(t, "json", result.Format)

	// Verify JSON is valid and readable
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"version": 1`)
	assert.Contains(t, string(data), `"node_id": "node1"`)
	assert.Contains(t, string(data), `"key1"`)
	assert.Contains(t, string(data), `"key2"`)

	// Load
	c2 := New(time.Minute, logger)
	defer c2.Close()

	loadResult, err := c2.Load("node1", path)
	require.NoError(t, err)
	assert.Equal(t, int32(2), loadResult.LoadedKeys)
}

func TestLoadExpiredKeys(t *testing.T) {
	logger := zap.NewNop()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cache-node1.dump.json")

	// Construct a JSON dump file with one valid and one already-expired entry
	// This avoids timing issues with very short TTLs
	pastTime := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
	futureTime := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339Nano)
	dumpData := fmt.Sprintf(`{
  "version": 1,
  "node_id": "node1",
  "dumped_at": "%s",
  "total_keys": 2,
  "expired_keys": 0,
  "entries": [
    {"key": "key1", "value": "value1", "value_type": "string", "expiration": "%s", "has_expiration": true},
    {"key": "key2", "value": "value2", "value_type": "string", "expiration": "%s", "has_expiration": true}
  ]
}`, time.Now().UTC().Format(time.RFC3339Nano), futureTime, pastTime)

	require.NoError(t, os.WriteFile(path, []byte(dumpData), 0644))

	// Load - key2 should be skipped (expired), key1 should be loaded
	c := New(time.Minute, logger)
	defer c.Close()

	loadResult, err := c.Load("node1", path)
	require.NoError(t, err)
	assert.Equal(t, int32(2), loadResult.TotalKeys)
	assert.Equal(t, int32(1), loadResult.LoadedKeys)
	assert.Equal(t, int32(1), loadResult.SkippedKeys)

	val, found := c.Get("key1")
	assert.True(t, found)
	assert.Equal(t, "value1", val)

	_, found = c.Get("key2")
	assert.False(t, found)
}

func TestLoadNonExistentFile(t *testing.T) {
	logger := zap.NewNop()
	c := New(time.Minute, logger)
	defer c.Close()

	// Pass a non-existent explicit path — should return error
	_, err := c.Load("node1", "/non/existent/path.dump")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read dump file")

	// Pass empty path with non-existent default — should return success with Path "none"
	result, err := c.Load("node1", "")
	require.NoError(t, err)
	assert.Equal(t, "none", result.Path)
}

func TestLoadAutoDetect(t *testing.T) {
	logger := zap.NewNop()
	c := New(time.Minute, logger)
	defer c.Close()

	require.NoError(t, c.Set("key1", "value1", "1h"))

	tmpDir := t.TempDir()

	// Dump binary to the auto-detect location: {tmpDir}/data/cache-node1.dump
	dataDir := filepath.Join(tmpDir, "data")
	binPath := filepath.Join(dataDir, "cache-node1.dump")
	require.NoError(t, os.MkdirAll(dataDir, 0755))

	_, err := c.Dump("node1", "binary", binPath)
	require.NoError(t, err)

	// Load with auto-detect (empty path) — Load() uses relative path "data/cache-node1.dump"
	// so we need to change working directory to tmpDir
	originalDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	defer os.Chdir(originalDir)

	c2 := New(time.Minute, logger)
	defer c2.Close()

	loadResult, err := c2.Load("node1", "")
	require.NoError(t, err)
	assert.Equal(t, int32(1), loadResult.LoadedKeys)
}

func TestDumpAtomicWrite(t *testing.T) {
	logger := zap.NewNop()
	c := New(time.Minute, logger)
	defer c.Close()

	require.NoError(t, c.Set("key1", "value1", ""))

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "subdir", "cache-node1.dump")

	// Dump to a non-existent subdirectory
	result, err := c.Dump("node1", "binary", path)
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.FileExists(t, path)

	// Verify no temp file remains
	_, err = os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(err))
}

func TestDumpDefaultPath(t *testing.T) {
	assert.Equal(t, "data/cache-node1.dump", DefaultDumpPath("node1", "binary", "data"))
	assert.Equal(t, "data/cache-node1.dump.json", DefaultDumpPath("node1", "json", "data"))
	assert.Equal(t, "custom/cache-node1.dump", DefaultDumpPath("node1", "binary", "custom"))
}

func TestDumpInvalidFormat(t *testing.T) {
	logger := zap.NewNop()
	c := New(time.Minute, logger)
	defer c.Close()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cache-node1.dump")

	_, err := c.Dump("node1", "yaml", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported dump format")
	assert.NoFileExists(t, path)
}
