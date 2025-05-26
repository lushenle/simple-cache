package cache

import (
	"container/heap"
	"sync"
	"sync/atomic"
	"time"

	"github.com/armon/go-radix"
	"github.com/lushenle/simple-cache/pkg/metrics"
)

type Item struct {
	value      string
	expiration time.Time
}

type Cache struct {
	mu            *metrics.InstrumentedRWMutex
	items         map[string]*Item
	prefixTree    *radix.Tree // Prefix tree for keys
	searchMetrics *searchMetrics

	expirationHeap  *ExpirationHeap
	stopChan        chan struct{}
	cleanupInterval time.Duration
	wg              sync.WaitGroup
}

type searchMetrics struct {
	total     atomic.Int64
	regexHits atomic.Int64
}

func New(cleanupInterval time.Duration) *Cache {
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
	}

	heap.Init(c.expirationHeap)
	c.wg.Add(1)
	go c.cleanupWorker()
	return c
}

func (c *Cache) Close() {
	close(c.stopChan)
	c.wg.Wait()
}
