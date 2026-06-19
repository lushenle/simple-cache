package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestMetrics(t *testing.T) {
	Init()

	t.Run("OperationCount", func(t *testing.T) {
		IncOperation("get", true)
		count := testutil.ToFloat64(OperationCount.WithLabelValues("get", "success"))
		assert.Equal(t, 1.0, count)
	})

	t.Run("MemoryMetrics", func(t *testing.T) {
		time.Sleep(6 * time.Second) // Wait for update
		alloc := testutil.ToFloat64(MemoryUsage.WithLabelValues("alloc"))
		assert.True(t, alloc > 0)
	})
}
