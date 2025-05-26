package cache

import (
	"container/heap"
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
)

func (c *Cache) Set(key, value string, expire string) error {
	start := time.Now()
	var success bool
	defer func() {
		metrics.ObserveOperation(time.Since(start), "set")
		metrics.IncOperation("set", success)
	}()

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
