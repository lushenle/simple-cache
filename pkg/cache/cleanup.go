package cache

import (
	"container/heap"
	"time"
)

func (c *Cache) cleanupWorker() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.cleanupExpired()
		case <-c.stopChan:
			return
		}
	}
}

func (c *Cache) cleanupExpired() {
	c.mu.Lock("write")
	defer c.mu.Unlock()

	now := time.Now()

	for i := 0; i < 1000; i++ {
		if c.expirationHeap.Len() == 0 {
			break
		}

		entry := (*c.expirationHeap)[0]
		if entry.expiration.After(now) {
			break
		}

		heap.Pop(c.expirationHeap)
		if item, exists := c.items[entry.key]; exists {
			if item.expiration.Equal(entry.expiration) {
				c.delInternal(entry.key)
			}
		}
	}
}
