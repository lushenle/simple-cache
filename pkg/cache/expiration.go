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

	duration, err := time.ParseDuration(expire)
	if err != nil {
		return false
	}

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

	// Set new expiration
	item.expiration = time.Now().Add(duration)
	heap.Push(c.expirationHeap, &expirationEntry{
		key:        key,
		expiration: item.expiration,
	})
	// expirationIndex is updated by the onSwap callback during heap.Push
	metrics.UpdateExpirationHeapSize(c.expirationHeap.Len())

	return true
}
