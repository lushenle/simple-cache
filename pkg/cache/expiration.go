package cache

import "time"

type ExpirationHeap []*expirationEntry

type expirationEntry struct {
	key        string
	expiration time.Time
}

func (h ExpirationHeap) Len() int {
	return len(h)
}

func (h ExpirationHeap) Less(i, j int) bool {
	return h[i].expiration.Before(h[j].expiration)
}

func (h ExpirationHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *ExpirationHeap) Push(x interface{}) {
	*h = append(*h, x.(*expirationEntry))
}

func (h *ExpirationHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}
