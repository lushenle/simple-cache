package cache

import "github.com/lushenle/simple-cache/pkg/metrics"

func (c *Cache) Reset() int {
	c.logger.Info("resetting cache")

	c.mu.Lock("write")
	defer c.mu.Unlock()
	count := len(c.items)
	c.items = make(map[string]*Item)
	metrics.UpdateKeysTotal(0)
	metrics.UpdateExpirationHeapSize(c.expirationHeap.Len())
	return count
}
