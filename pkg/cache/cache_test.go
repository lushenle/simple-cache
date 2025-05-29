package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBasicOperations(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	t.Run("SetAndGet", func(t *testing.T) {
		err := c.Set("key1", "value1", "")
		assert.Nil(t, err)
		val, found := c.Get("key1")
		assert.True(t, found)
		assert.Equal(t, "value1", val)
	})

	t.Run("Expiration", func(t *testing.T) {
		err := c.Set("key2", "value2", "100ms")
		assert.Nil(t, err)
		time.Sleep(150 * time.Millisecond)
		val, found := c.Get("key2")
		assert.False(t, found)
		assert.Empty(t, val)
	})

	t.Run("Delete", func(t *testing.T) {
		c.Set("key3", "value3", "")
		existed := c.Del("key3")
		assert.True(t, existed)
		assert.False(t, c.Del("nonexistent"))
	})
}

func TestSearch(t *testing.T) {
	c := newTestCache()
	defer c.Close()
	keys := []string{
		"user:1001",
		"user:1002",
		"order:2001",
		"temp:123",
	}
	for _, k := range keys {
		err := c.Set(k, "data", "")
		assert.Nil(t, err)
	}

	// Check if all keys are present in the cache
	assert.Len(t, c.items, 4)

	t.Run("Wildcard", func(t *testing.T) {
		results, err := c.Search("user:*", false)
		assert.Nil(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("Regex", func(t *testing.T) {
		results, err := c.Search(`^order:\d+$`, true)
		assert.Nil(t, err)
		assert.Len(t, results, 1)

		// Validate uniqueness of results
		unique := make(map[string]struct{})
		for _, k := range results {
			unique[k] = struct{}{}
		}
		assert.Len(t, unique, 1)
	})
}

func BenchmarkConcurrentAccess(b *testing.B) {
	c := newTestCache()
	defer c.Close()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("key%d", i%1000)
			err := c.Set(key, "value", "")
			assert.Nil(b, err)
			c.Get(key)
			i++
		}
	})
}

func TestSearchConcurrency(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	// Add 1000 keys
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("key-%04d", i)
		c.Set(key, "value", "")
	}

	// Concurrent search requests
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, _ := c.Search("key-*", false)
			assert.Len(t, results, 1000)
		}()
	}

	wg.Wait()
}

func TestExpirationCleanup(t *testing.T) {
	// Use a short cleanup interval for this test
	c := newTestCache(50 * time.Millisecond)
	defer c.Close()

	// 设置立即过期的键
	c.Set("temp1", "value", "1ms")
	c.Set("temp2", "value", "1ms")
	c.Set("valid", "value", "1h")

	// 等待清理周期 (must be longer than cleanupInterval and allow for processing)
	time.Sleep(150 * time.Millisecond)

	c.mu.RLock("read")
	defer c.mu.RUnlock()

	assert.Equal(t, 1, len(c.items))
	_, exists := c.items["valid"]
	assert.True(t, exists)
}

func TestHeapMaintenance(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	c.Set("key1", "v", "1m")
	c.Set("key2", "v", "2m")
	c.Set("key3", "v", "30s")

	c.mu.Lock("write")
	entry := (*c.expirationHeap)[0]
	assert.Equal(t, "key3", entry.key)
	c.mu.Unlock()

	c.Del("key2")
	c.mu.Lock("write")
	for _, e := range *c.expirationHeap {
		assert.NotEqual(t, "key2", e.key)
	}
	c.mu.Unlock()
}
