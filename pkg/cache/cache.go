package cache

import (
	"container/heap"
	"container/list"
	"fmt"
	"sync"
	"time"

	"github.com/armon/go-radix"
	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

// EvictionPolicy controls how the cache behaves when it reaches max_keys.
type EvictionPolicy string

const (
	EvictionNone EvictionPolicy = "none" // return ErrMaxKeysReached when full
	EvictionLRU  EvictionPolicy = "lru"  // evict least recently used when full
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

	maxKeys        int // max cache keys (0 = unlimited)
	maxValueSize   int // max value size in bytes (0 = unlimited)
	evictionPolicy EvictionPolicy
	lruMu          sync.Mutex
	lruList        *list.List               // front = most recent, back = evict candidate
	lruElements    map[string]*list.Element // key -> list element

	logger *zap.Logger
}

func New(cleanupInterval time.Duration, logger *zap.Logger) *Cache {
	return NewWithLimits(cleanupInterval, 0, 0, string(EvictionNone), logger)
}

func NewWithLimits(cleanupInterval time.Duration, maxKeys, maxValueSize int, evictionPolicy string, logger *zap.Logger) *Cache {
	if cleanupInterval <= 0 {
		cleanupInterval = time.Minute // Default cleanup interval
	}
	if maxKeys < 0 {
		maxKeys = 0
	}
	if maxValueSize < 0 {
		maxValueSize = 0
	}

	ep := EvictionPolicy(evictionPolicy)
	switch ep {
	case EvictionNone, EvictionLRU:
	default:
		ep = EvictionNone
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
		evictionPolicy:  ep,
		lruElements:     make(map[string]*list.Element),
		logger:          logger,
	}
	if ep == EvictionLRU {
		c.lruList = list.New()
	}

	// Set up index tracking callback so expirationIndex stays in sync with heap swaps
	c.expirationHeap.onSwap = func(key string, newIndex int) {
		c.expirationIndex[key] = newIndex
	}

	heap.Init(c.expirationHeap)
	c.wg.Add(2)
	go c.cleanupWorker()
	go c.sizeMetricsWorker()
	return c
}

type CacheStats struct {
	KeyCount               int    `json:"key_count"`
	ExpirationHeapSize     int    `json:"expiration_heap_size"`
	EvictionPolicy         string `json:"eviction_policy"`
	MaxKeys                int    `json:"max_keys"`
	MaxValueSize           int    `json:"max_value_size"`
	ApproximateMemoryBytes int64  `json:"approximate_memory_bytes"`
}

func (c *Cache) Stats() CacheStats {
	c.mu.RLock(metrics.LockRead)
	defer c.mu.RUnlock()

	count := len(c.items)
	heapSize := c.expirationHeap.Len()
	memSize := approxKeyValueSize(c.items, count)

	return CacheStats{
		KeyCount:               count,
		ExpirationHeapSize:     heapSize,
		EvictionPolicy:         string(c.evictionPolicy),
		MaxKeys:                c.maxKeys,
		MaxValueSize:           c.maxValueSize,
		ApproximateMemoryBytes: memSize,
	}
}

func approxKeyValueSize(items map[string]*Item, count int) int64 {
	if count == 0 {
		return 0
	}
	if count > 10000 {
		count = count / 100
		if count == 0 {
			count = 1
		}
	}
	var total int64
	i := 0
	step := 1
	if len(items) > 10000 {
		step = len(items) / count
		if step < 1 {
			step = 1
		}
	}
	for k, v := range items {
		if i%step == 0 {
			total += int64(len(k) + approxValueSize(v.value))
		}
		i++
	}
	if len(items) > 10000 && step > 1 {
		total = total * int64(step)
	}
	return total
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

// access marks a key as recently used.  In LRU mode it moves the key to the
// front of the eviction list.  The caller must hold at least a read lock.
func (c *Cache) access(key string) {
	c.lruMu.Lock()
	defer c.lruMu.Unlock()
	if c.lruList == nil || c.lruElements == nil {
		return
	}
	if elem, ok := c.lruElements[key]; ok {
		c.lruList.MoveToFront(elem)
	}
}

// evictLRU removes the least recently used key from the cache.
// The caller must hold the write lock. Returns true if a key was evicted.
func (c *Cache) evictLRU() bool {
	c.lruMu.Lock()
	if c.lruList == nil || c.lruList.Len() == 0 {
		c.lruMu.Unlock()
		return false
	}
	elem := c.lruList.Back()
	if elem == nil {
		c.lruMu.Unlock()
		return false
	}
	key := elem.Value.(string)
	c.lruList.Remove(elem)
	delete(c.lruElements, key)
	c.lruMu.Unlock()
	c.delInternal(key)
	metrics.IncEvictions()
	return true
}

func (c *Cache) delLRU(key string) {
	c.lruMu.Lock()
	defer c.lruMu.Unlock()
	if c.lruElements != nil {
		if elem, ok := c.lruElements[key]; ok {
			c.lruList.Remove(elem)
			delete(c.lruElements, key)
		}
	}
}

func (c *Cache) resetLRU() {
	c.lruMu.Lock()
	defer c.lruMu.Unlock()
	if c.lruList != nil {
		c.lruList.Init()
	}
	if c.lruElements != nil {
		c.lruElements = make(map[string]*list.Element)
	}
}

func (c *Cache) setLRU(key string) {
	c.lruMu.Lock()
	defer c.lruMu.Unlock()
	if c.lruList == nil {
		return
	}
	if elem, ok := c.lruElements[key]; ok {
		c.lruList.MoveToFront(elem)
		return
	}
	elem := c.lruList.PushFront(key)
	c.lruElements[key] = elem
}

// sizeMetricsWorker periodically estimates cache memory usage in the
// background, avoiding the need to scan all items under a write lock.
func (c *Cache) sizeMetricsWorker() {
	defer c.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopChan:
			return
		case <-ticker.C:
			c.updateSizeMetrics()
		}
	}
}
