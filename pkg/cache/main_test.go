package cache

import (
	"os"
	"testing"
	"time"

	"github.com/lushenle/simple-cache/pkg/log"
	"go.uber.org/zap/zapcore"
)

func newTestCache(cleanupInterval ...time.Duration) *Cache {
	plugin := log.NewStdoutPlugin(zapcore.DebugLevel)
	logger := log.NewLogger(plugin)

	interval := time.Minute
	if len(cleanupInterval) > 0 {
		interval = cleanupInterval[0]
	}

	c := New(interval, logger)
	// defer c.Close() // Close should be handled by the caller of newTestCache

	return c
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
