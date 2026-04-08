package cache

import (
	"container/heap"

	"github.com/armon/go-radix"
	"github.com/lushenle/simple-cache/pkg/metrics"
)

func (c *Cache) Reset() int {
	c.logger.Info("resetting cache")

	c.mu.Lock(metrics.LockWrite)
	defer c.mu.Unlock()
	count := len(c.items)
	c.items = make(map[string]*Item)
	c.prefixTree = radix.New()
	c.expirationHeap = &ExpirationHeap{onSwap: c.expirationHeap.onSwap}
	heap.Init(c.expirationHeap)
	c.expirationIndex = make(map[string]int)
	metrics.UpdateKeysTotal(0)
	metrics.UpdateExpirationHeapSize(0)
	return count
}
