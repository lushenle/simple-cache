package cache

import (
	"container/heap"
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

func (c *Cache) Set(key, value string, expire string) error {
	c.logger.Info("set", zap.String("key", key))

	start := time.Now()
	var success bool
	defer func() {
		metrics.ObserveOperation(time.Since(start), "set")
		metrics.IncOperation("set", success)
	}()

	// Acquire an item from the pool
	item := itemPool.Get().(*Item)
	item.value = value

	var expiration time.Time
	if expire != "" {
		duration, err := time.ParseDuration(expire)
		if err != nil {
			return err
		}

		expiration = time.Now().Add(duration)
	}

	c.mu.Lock("write")
	defer c.mu.Unlock()

	if !expiration.IsZero() {
		heap.Push(c.expirationHeap, &expirationEntry{
			key:        key,
			expiration: expiration,
		})
	}

	c.setInternal(key, &Item{
		value:      value,
		expiration: expiration,
	})

	success = true

	// Release the item back to the pool
	itemPool.Put(item)

	c.updateSizeMetrics()

	return nil
}

func (c *Cache) setInternal(key string, item *Item) {
	if _, exists := c.items[key]; exists {
		c.prefixTree.Delete(key)
	}

	c.items[key] = item
	c.prefixTree.Insert(key, nil)
}
