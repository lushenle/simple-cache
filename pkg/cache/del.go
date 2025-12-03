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
		metrics.ObserveOperation(time.Since(start), metrics.OpDel)
	}()

	c.mu.Lock(metrics.LockWrite)
	defer c.mu.Unlock()

	_, existed := c.items[key]
	if existed {
		c.delInternal(key)
		metrics.IncOperation(metrics.OpDel, true)
	} else {
		metrics.IncOperation(metrics.OpDel, false)
	}

	return existed
}

func (c *Cache) delInternal(key string) {
	for i, entry := range *c.expirationHeap {
		if entry.key == key {
			heap.Remove(c.expirationHeap, i)
			metrics.UpdateExpirationHeapSize(c.expirationHeap.Len())
			break
		}
	}

	delete(c.items, key)
	c.prefixTree.Delete(key)
	metrics.UpdateKeysTotal(len(c.items))
}
