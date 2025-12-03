package cache

import (
	"container/heap"
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
)

func (c *Cache) cleanupWorker() {
	c.logger.Info("starting cleanup worker")

	defer c.wg.Done()

	for {
		delay := c.nextCleanupDelay()
		if delay <= 0 {
			c.cleanupExpired()
			delay = c.nextCleanupDelay()
		}
		select {
		case <-time.After(delay):
			c.cleanupExpired()
		case <-c.stopChan:
			return
		}
	}
}

func (c *Cache) cleanupExpired() {
	c.logger.Info("starting cleanup expired")

	start := time.Now()
	budget := c.cleanupInterval / 20
	if budget <= 0 {
		budget = 5 * time.Millisecond
	}

	c.mu.Lock("write")
	defer c.mu.Unlock()

	now := time.Now()
	processed := 0
	for processed < 2000 && time.Since(start) < budget {
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
				processed++
			}
		}
	}
	metrics.UpdateExpirationHeapSize(c.expirationHeap.Len())
	metrics.ObserveOperation(time.Since(start), metrics.OpCleanup)
}

func (c *Cache) nextCleanupDelay() time.Duration {
	delay := c.cleanupInterval
	c.mu.RLock("read")
	if c.expirationHeap.Len() > 0 {
		next := (*c.expirationHeap)[0].expiration
		until := time.Until(next)
		if until > 0 && until < delay {
			delay = until
		}
	}
	c.mu.RUnlock()
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}
	return delay
}
