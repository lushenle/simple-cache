package bench

import (
	"testing"
	"time"

	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/log"
	"go.uber.org/zap/zapcore"
)

func newCache() *cache.Cache {
	plugin := log.NewStdoutPlugin(zapcore.ErrorLevel)
	logger := log.NewLogger(plugin)
	return cache.New(10*time.Second, logger)
}

func BenchmarkSet(b *testing.B) {
	c := newCache()
	for i := 0; i < b.N; i++ {
		_ = c.Set("key"+time.Now().String(), "value", "")
	}
}

func BenchmarkGet(b *testing.B) {
	c := newCache()
	_ = c.Set("k", "v", "")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Get("k")
	}
}
