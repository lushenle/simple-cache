package cache

import (
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
)

func (c *Cache) updateSizeMetrics() {
	start := time.Now()
	defer func() {
		metrics.ObserveOperation(time.Since(start), "size_calculation")
	}()

	count := len(c.items)
	memSize := 0

	if count < 10000 {
		for k, v := range c.items {
			memSize += len(k) + len(v.value)
		}
	} else {
		sampleCount := count / 100
		if sampleCount == 0 {
			sampleCount = 1
		}
		i := 0
		for k, v := range c.items {
			if i%sampleCount == 0 {
				memSize += len(k) + len(v.value)
			}
			i++
		}
		memSize = memSize * count / sampleCount
	}

	metrics.CacheSize.WithLabelValues("item_count").Set(float64(count))
	metrics.CacheSize.WithLabelValues("memory_bytes").Set(float64(memSize))
}
