package cache

import (
	"fmt"
	"testing"
)

func BenchmarkSetGet(b *testing.B) {
	c := newTestCache()
	b.ReportAllocs()

	b.Run("SingleKey", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			c.Set("key", "value", "")
			c.Get("key")
		}
	})

	b.Run("MultiKeys", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				key := fmt.Sprintf("key%d", i%1000)
				c.Set(key, "value", "1s")
				c.Get(key)
				i++
			}
		})
	})
}

func BenchmarkSearch(b *testing.B) {
	c := newTestCache()
	// Add 10,000 test data
	for i := 0; i < 10000; i++ {
		key := fmt.Sprintf("key:%04d", i)
		c.Set(key, "value", "")
	}

	b.Run("PrefixSearch", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			c.Search("key:01*", false)
		}
	})

	b.Run("RegexSearch", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			c.Search("^key:\\d{4}$", true)
		}
	})
}
