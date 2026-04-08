package cache

import (
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

func (c *Cache) Get(key string) (any, bool) {
	c.logger.Debug("get", zap.String("key", key))

	start := time.Now()
	var success bool
	defer func() {
		metrics.ObserveOperation(time.Since(start), metrics.OpGet)
		metrics.IncOperation(metrics.OpGet, success)
	}()

	c.mu.RLock(metrics.LockRead)
	item, found := c.items[key]
	if !found {
		c.mu.RUnlock()
		return "", success
	}

	if !item.expiration.IsZero() && time.Now().After(item.expiration) {
		c.mu.RUnlock()
		return c.handleExpiredKey(key)
	}
	defer c.mu.RUnlock()

	success = true
	return item.value, success
}

func (c *Cache) handleExpiredKey(key string) (any, bool) {
	c.mu.Lock(metrics.LockWrite)
	defer c.mu.Unlock()

	// Double check after acquiring write lock
	item, found := c.items[key]
	if !found {
		return "", false
	}

	if !item.expiration.IsZero() && time.Now().After(item.expiration) {
		// Use delInternal for complete cleanup (heap + prefixTree + items)
		c.delInternal(key)
		return "", false
	}

	return item.value, true
}
