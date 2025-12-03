package cache

import (
	"reflect"
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
)

func (c *Cache) updateSizeMetrics() {
	start := time.Now()
	defer func() {
		metrics.ObserveOperation(time.Since(start), metrics.OpSizeCalculation)
	}()

	count := len(c.items)
	memSize := 0

	// Helper function to calculate size of a value
	valueSize := func(v any) int {
		switch val := v.(type) {
		case string:
			return len(val)
		case []byte:
			return len(val)
		default:
			// For unsupported types, use reflection to get type size
			return int(reflect.TypeOf(v).Size())
		}
	}

	if count < 10000 {
		for k, v := range c.items {
			memSize += len(k) + valueSize(v.value)
		}
	} else {
		sampleCount := count / 100
		if sampleCount == 0 {
			sampleCount = 1
		}
		i := 0
		for k, v := range c.items {
			if i%sampleCount == 0 {
				memSize += len(k) + valueSize(v.value)
			}
			i++
		}

		// Scale the memory size based on the sample count
		// This is a rough estimate to avoid iterating through all items
		// when the cache is large
		// It assumes that the memory size is roughly proportional to the number of items
		// This is a simplification and may not be accurate for all types of data
		memSize = memSize * count / sampleCount
	}

	metrics.CacheSize.WithLabelValues("item_count").Set(float64(count))
	metrics.CacheSize.WithLabelValues("memory_bytes").Set(float64(memSize))
}
