package cache

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"hash/crc32"
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
	require.NoError(t, c.Set("key2", "value2", ""))    // no expiration
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
	assert.Contains(t, string(data), `"version": 2`)
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

	require.NoError(t, os.WriteFile(path, []byte(dumpData), 0o644))

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
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

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

func TestDecodeBinaryDumpRejectsCRC32Mismatch(t *testing.T) {
	data, err := encodeBinaryDump([]DumpEntry{
		{Key: "key1", Value: "value1", ValueType: "string"},
	})
	require.NoError(t, err)

	// Corrupt payload while keeping header/footer length intact.
	data[20] ^= 0xFF

	_, err = decodeBinaryDump(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "crc32 mismatch")
}

func TestDecodeBinaryDumpRejectsTruncatedEntryByCount(t *testing.T) {
	data, err := encodeBinaryDump([]DumpEntry{
		{Key: "key1", Value: "value1", ValueType: "string"},
	})
	require.NoError(t, err)

	// Tamper count to expect one more entry and update checksum so parse validation is exercised.
	binary.BigEndian.PutUint32(data[8:12], 2)
	footerStart := len(data) - 8
	binary.BigEndian.PutUint32(data[footerStart:footerStart+4], crc32.ChecksumIEEE(data[:footerStart]))

	_, err = decodeBinaryDump(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated dump")
}

func TestSerializeDeserializeTypedValues(t *testing.T) {
	// nil
	s, vt := serializeValue(nil)
	assert.Equal(t, "", s)
	assert.Equal(t, "nil", vt)
	assert.Nil(t, deserializeValue(s, vt))

	// string
	s, vt = serializeValue("hello")
	assert.Equal(t, "hello", s)
	assert.Equal(t, "string", vt)
	assert.Equal(t, "hello", deserializeValue(s, vt))

	// []byte
	origBytes := []byte{0xFF, 0x00, 0xAB}
	s, vt = serializeValue(origBytes)
	assert.Equal(t, base64.StdEncoding.EncodeToString(origBytes), s)
	assert.Equal(t, "bytes", vt)
	restored := deserializeValue(s, vt)
	assert.Equal(t, origBytes, restored)

	// map (json)
	m := map[string]int{"a": 1}
	s, vt = serializeValue(m)
	assert.Equal(t, "json", vt)
	restoredVal := deserializeValue(s, vt)
	restoredMap, ok := restoredVal.(map[string]interface{})
	require.True(t, ok, "expected map[string]interface{}, got %T", restoredVal)
	assert.Equal(t, float64(1), restoredMap["a"])
}

func TestDumpAndLoadBinaryWithValueType(t *testing.T) {
	entries := []DumpEntry{
		{Key: "str", Value: "hello", ValueType: "string"},
		{Key: "bin", Value: base64.StdEncoding.EncodeToString([]byte{0xDE, 0xAD}), ValueType: "bytes"},
		{Key: "obj", Value: `{"x":1}`, ValueType: "json"},
		{Key: "nul", Value: "", ValueType: "nil"},
	}

	data, err := encodeBinaryDump(entries)
	require.NoError(t, err)

	decoded, err := decodeBinaryDump(data)
	require.NoError(t, err)
	require.Len(t, decoded, len(entries))

	for i, e := range entries {
		assert.Equal(t, e.Key, decoded[i].Key, "key mismatch at %d", i)
		assert.Equal(t, e.Value, decoded[i].Value, "value mismatch at %d", i)
		assert.Equal(t, e.ValueType, decoded[i].ValueType, "value_type mismatch at %d", i)
	}
}

func TestDecodeBinaryDumpV1Compatibility(t *testing.T) {
	// Manually construct a v1 binary dump (without ValueType field per entry).
	// Layout: magic(4) + version(4) + count(4) + flags(4)
	//         per entry: keyLen(4) + key + valLen(4) + val + expUnix(8) + hasExp(1)
	//         footer: crc32(4) + padding(4)
	type v1Entry struct {
		key   string
		value string
	}
	v1Entries := []v1Entry{
		{"k1", "v1"},
		{"k2", "v2"},
	}

	buf := make([]byte, 0, 256)
	buf = append(buf, []byte("SCDF")...)                             // magic
	buf = binary.BigEndian.AppendUint32(buf, 1)                      // version = 1
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(v1Entries))) // count
	buf = binary.BigEndian.AppendUint32(buf, 0)                      // flags

	for _, e := range v1Entries {
		kb := []byte(e.key)
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(kb)))
		buf = append(buf, kb...)
		vb := []byte(e.value)
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(vb)))
		buf = append(buf, vb...)
		buf = binary.BigEndian.AppendUint64(buf, 0) // no expiration
		buf = append(buf, 0)                        // hasExp = false
	}

	// Footer
	checksum := crc32.ChecksumIEEE(buf)
	buf = binary.BigEndian.AppendUint32(buf, checksum)
	buf = binary.BigEndian.AppendUint32(buf, 0) // padding

	decoded, err := decodeBinaryDump(buf)
	require.NoError(t, err)
	require.Len(t, decoded, 2)

	for i, e := range v1Entries {
		assert.Equal(t, e.key, decoded[i].Key)
		assert.Equal(t, e.value, decoded[i].Value)
		assert.Equal(t, "string", decoded[i].ValueType, "v1 entries should default to string type")
		assert.False(t, decoded[i].HasExpiration)
	}
}
