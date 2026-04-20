package cache

import (
	"container/heap"
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

// SetExpiration updates the expiration time for an existing key.
// Returns true if the key existed and was updated.
func (c *Cache) SetExpiration(key string, expire string) bool {
	c.logger.Debug("set expiration", zap.String("key", key))

	c.mu.Lock(metrics.LockWrite)
	defer c.mu.Unlock()

	item, exists := c.items[key]
	if !exists {
		return false
	}

	// Remove old expiration from heap if present
	if !item.expiration.IsZero() {
		if idx, ok := c.expirationIndex[key]; ok {
			heap.Remove(c.expirationHeap, idx)
			delete(c.expirationIndex, key)
			metrics.UpdateExpirationHeapSize(c.expirationHeap.Len())
		}
	}

	// Empty expire means removing expiration, keeping the key persistent.
	if expire == "" {
		item.expiration = time.Time{}
		return true
	}

	duration, err := time.ParseDuration(expire)
	if err != nil {
		return false
	}

	item.expiration = time.Now().Add(duration)
	heap.Push(c.expirationHeap, &expirationEntry{
		key:        key,
		expiration: item.expiration,
	})
	metrics.UpdateExpirationHeapSize(c.expirationHeap.Len())

	return true
}
