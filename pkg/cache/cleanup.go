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
	start := time.Now()
	budget := c.cleanupInterval / 20
	if budget <= 0 {
		budget = 5 * time.Millisecond
	}

	c.mu.Lock(metrics.LockWrite)
	defer c.mu.Unlock()

	now := time.Now()
	processed := 0
	for processed < 2000 && time.Since(start) < budget {
		if c.expirationHeap.Len() == 0 {
			break
		}

		entry := c.expirationHeap.Peek()
		if entry.expiration.After(now) {
			break
		}

		heap.Pop(c.expirationHeap)
		// Remove from expirationIndex
		delete(c.expirationIndex, entry.key)

		if item, exists := c.items[entry.key]; exists {
			if item.expiration.Equal(entry.expiration) {
				delete(c.items, entry.key)
				c.prefixTree.Delete(entry.key)
				processed++
			}
		}
	}
	metrics.UpdateExpirationHeapSize(c.expirationHeap.Len())
	metrics.UpdateKeysTotal(len(c.items))
	metrics.ObserveOperation(time.Since(start), metrics.OpCleanup)
}

func (c *Cache) nextCleanupDelay() time.Duration {
	delay := c.cleanupInterval
	c.mu.RLock(metrics.LockRead)
	if c.expirationHeap.Len() > 0 {
		entry := c.expirationHeap.Peek()
		if entry != nil {
			until := time.Until(entry.expiration)
			if until > 0 && until < delay {
				delay = until
			}
		}
	}
	c.mu.RUnlock()
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}
	return delay
}
