package cache

import (
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

func (c *Cache) Get(key string) (string, bool) {
	c.logger.Info("get", zap.String("key", key))

	start := time.Now()
	var success bool
	defer func() {
		metrics.ObserveOperation(time.Since(start), "get")
		metrics.IncOperation("get", success)
	}()

	c.mu.RLock("read")
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

func (c *Cache) handleExpiredKey(key string) (string, bool) {
	c.mu.Lock("write")
	defer c.mu.Unlock()

	// Double check after acquiring write lock
	item, found := c.items[key]
	if !found {
		return "", false
	}

	if !item.expiration.IsZero() && time.Now().After(item.expiration) {
		delete(c.items, key)
		return "", false
	}

	return item.value, true
}
