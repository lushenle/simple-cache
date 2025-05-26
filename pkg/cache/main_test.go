package cache

import (
	"os"
	"testing"
	"time"

	"github.com/lushenle/simple-cache/pkg/log"
	"go.uber.org/zap/zapcore"
)

func newTestCache() *Cache {
	plugin := log.NewStdoutPlugin(zapcore.DebugLevel)
	logger := log.NewLogger(plugin)

	c := New(time.Minute, logger)
	defer c.Close()

	return c
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
