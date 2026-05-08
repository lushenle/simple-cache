package cache

import (
	"container/heap"
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

func (c *Cache) Set(key string, value any, expire string) error {
	c.logger.Debug("set", zap.String("key", key))

	start := time.Now()
	var success bool
	defer func() {
		metrics.ObserveOperation(time.Since(start), metrics.OpSet)
		metrics.IncOperation(metrics.OpSet, success)
	}()

	var expiration time.Time
	if expire != "" {
		duration, err := time.ParseDuration(expire)
		if err != nil {
			return err
		}
		expiration = time.Now().Add(duration)
	}

	if c.maxValueSize > 0 {
		if sz := approxValueSize(value); sz > c.maxValueSize {
			return ErrValueTooLarge{Size: sz, MaxSize: c.maxValueSize}
		}
	}

	c.mu.Lock(metrics.LockWrite)
	defer c.mu.Unlock()

	// Check max keys limit (only for new keys, not updates)
	if c.maxKeys > 0 && len(c.items) >= c.maxKeys {
		if _, exists := c.items[key]; !exists {
			return ErrMaxKeysReached{MaxKeys: c.maxKeys}
		}
	}

	// Clean up old entry if key already exists (fixes stale expiration in heap)
	if _, exists := c.items[key]; exists {
		c.delInternal(key)
	}

	if !expiration.IsZero() {
		heap.Push(c.expirationHeap, &expirationEntry{
			key:        key,
			expiration: expiration,
		})
		// expirationIndex is updated by the onSwap callback during heap.Push
		metrics.UpdateExpirationHeapSize(c.expirationHeap.Len())
	}

	c.setInternal(key, &Item{
		value:      value,
		expiration: expiration,
	})

	success = true

	metrics.UpdateKeysTotal(len(c.items))

	return nil
}

// approxValueSize returns an approximate byte size of a value for limit checking.
func approxValueSize(v any) int {
	switch val := v.(type) {
	case string:
		return len(val)
	case []byte:
		return len(val)
	case int, int32, int64, uint, uint32, uint64:
		return 8
	case float32, float64:
		return 8
	case bool:
		return 1
	default:
		return 64 // rough estimate for complex types
	}
}

func (c *Cache) setInternal(key string, item *Item) {
	c.items[key] = item
	c.prefixTree.Insert(key, nil)
}
