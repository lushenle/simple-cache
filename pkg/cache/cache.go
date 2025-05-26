package cache

import (
	"container/heap"
	"sync"
	"time"

	"github.com/armon/go-radix"
	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

type Item struct {
	value      string
	expiration time.Time
}

type Cache struct {
	mu         *metrics.InstrumentedRWMutex
	items      map[string]*Item
	prefixTree *radix.Tree // Prefix tree for keys

	expirationHeap  *ExpirationHeap
	stopChan        chan struct{}
	cleanupInterval time.Duration
	wg              sync.WaitGroup

	logger *zap.Logger
}

func New(cleanupInterval time.Duration, logger *zap.Logger) *Cache {
	if cleanupInterval <= 0 {
		cleanupInterval = time.Minute // Default cleanup interval
	}

	c := &Cache{
		mu:              &metrics.InstrumentedRWMutex{},
		items:           make(map[string]*Item),
		prefixTree:      radix.New(),
		expirationHeap:  &ExpirationHeap{},
		stopChan:        make(chan struct{}),
		cleanupInterval: cleanupInterval,
		logger:          logger,
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
