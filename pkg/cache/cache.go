package cache

import (
	"container/heap"
	"fmt"
	"sync"
	"time"

	"github.com/armon/go-radix"
	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

type Item struct {
	value      any
	expiration time.Time
}

type Cache struct {
	mu         *metrics.InstrumentedRWMutex
	items      map[string]*Item
	prefixTree *radix.Tree // Prefix tree for keys

	expirationHeap  *ExpirationHeap
	expirationIndex map[string]int // key -> heap index for O(log n) deletion
	stopChan        chan struct{}
	cleanupInterval time.Duration
	wg              sync.WaitGroup

	maxKeys      int // max cache keys (0 = unlimited)
	maxValueSize int // max value size in bytes (0 = unlimited)

	logger *zap.Logger
}

func New(cleanupInterval time.Duration, logger *zap.Logger) *Cache {
	return NewWithLimits(cleanupInterval, 0, 0, logger)
}

func NewWithLimits(cleanupInterval time.Duration, maxKeys, maxValueSize int, logger *zap.Logger) *Cache {
	if cleanupInterval <= 0 {
		cleanupInterval = time.Minute // Default cleanup interval
	}
	if maxKeys < 0 {
		maxKeys = 0
	}
	if maxValueSize < 0 {
		maxValueSize = 0
	}

	c := &Cache{
		mu:              &metrics.InstrumentedRWMutex{},
		items:           make(map[string]*Item),
		prefixTree:      radix.New(),
		expirationHeap:  &ExpirationHeap{},
		expirationIndex: make(map[string]int),
		stopChan:        make(chan struct{}),
		cleanupInterval: cleanupInterval,
		maxKeys:         maxKeys,
		maxValueSize:    maxValueSize,
		logger:          logger,
	}

	// Set up index tracking callback so expirationIndex stays in sync with heap swaps
	c.expirationHeap.onSwap = func(key string, newIndex int) {
		c.expirationIndex[key] = newIndex
	}

	heap.Init(c.expirationHeap)
	c.wg.Add(1)
	go c.cleanupWorker()
	return c
}

func (c *Cache) Close() {
	c.logger.Info("closing cache")
	close(c.stopChan)
	c.wg.Wait()
}

// ErrValueTooLarge is returned when the value exceeds the configured max value size.
type ErrValueTooLarge struct {
	Size    int
	MaxSize int
}

func (e ErrValueTooLarge) Error() string {
	return fmt.Sprintf("value size %d bytes exceeds max_value_size %d bytes", e.Size, e.MaxSize)
}

// ErrMaxKeysReached is returned when the cache has reached its max key limit.
type ErrMaxKeysReached struct {
	MaxKeys int
}

func (e ErrMaxKeysReached) Error() string {
	return fmt.Sprintf("cache has reached max_keys limit of %d", e.MaxKeys)
}
