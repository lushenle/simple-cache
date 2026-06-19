package cache

import "time"

type expirationEntry struct {
	key        string
	expiration time.Time
	index      int // Index in the heap, maintained by Swap
}

// ExpirationHeap implements container/heap.Interface for managing key expiration times.
// It uses an optional onSwap callback to notify the Cache when entries are moved.
type ExpirationHeap struct {
	entries []*expirationEntry
	onSwap  func(key string, newIndex int) // called when an entry's index changes
}

func (h *ExpirationHeap) Len() int { return len(h.entries) }

func (h *ExpirationHeap) Less(i, j int) bool {
	return h.entries[i].expiration.Before(h.entries[j].expiration)
}

func (h *ExpirationHeap) Swap(i, j int) {
	h.entries[i], h.entries[j] = h.entries[j], h.entries[i]
	h.entries[i].index = i
	h.entries[j].index = j
	if h.onSwap != nil {
		h.onSwap(h.entries[i].key, i)
		h.onSwap(h.entries[j].key, j)
	}
}

func (h *ExpirationHeap) Push(x interface{}) {
	n := len(h.entries)
	entry := x.(*expirationEntry)
	entry.index = n
	h.entries = append(h.entries, entry)
	// Notify the index tracking callback for the initial position
	if h.onSwap != nil {
		h.onSwap(entry.key, n)
	}
}

func (h *ExpirationHeap) Pop() interface{} {
	old := h.entries
	n := len(old)
	x := old[n-1]
	old[n-1] = nil // avoid memory leak
	h.entries = old[0 : n-1]
	return x
}

// Peek returns the top entry without removing it.
func (h *ExpirationHeap) Peek() *expirationEntry {
	if h.Len() == 0 {
		return nil
	}
	return h.entries[0]
}

// EntryAt returns the entry at the given index.
func (h *ExpirationHeap) EntryAt(i int) *expirationEntry {
	return h.entries[i]
}
