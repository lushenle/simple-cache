package cache

import (
	"container/heap"
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

func (c *Cache) Del(key string) bool {
	c.logger.Info("del", zap.String("key", key))

	start := time.Now()
	defer func() {
		metrics.ObserveOperation(time.Since(start), "del")
	}()

	c.mu.Lock("write")
	defer c.mu.Unlock()

	_, existed := c.items[key]
	if existed {
		c.delInternal(key)
		metrics.IncOperation("del", true)
	} else {
		metrics.IncOperation("del", false)
	}

	return existed
}

func (c *Cache) delInternal(key string) {
	for i, entry := range *c.expirationHeap {
		if entry.key == key {
			heap.Remove(c.expirationHeap, i)
			break
		}
	}

	delete(c.items, key)
	c.prefixTree.Delete(key)
}
