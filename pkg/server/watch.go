package server

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/lushenle/simple-cache/pkg/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

var (
	droppedEvents int64
	subIDCounter  int64
)

// Subscriber holds a channel onto which watch events are pushed.
type Subscriber struct {
	ID      string
	Pattern string
	Ch      chan *pb.WatchEvent
	mu      sync.Mutex
	closed  bool
}

func (s *Subscriber) Send(evt *pb.WatchEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.Ch <- evt:
	default:
		atomic.AddInt64(&droppedEvents, 1)
	}
}

func (s *Subscriber) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		close(s.Ch)
		s.closed = true
	}
}

// Matches checks whether the given key matches the subscriber's pattern.
func (s *Subscriber) Matches(key string) bool {
	if s.Pattern == "" {
		return true
	}
	matched, _ := filepath.Match(s.Pattern, key)
	return matched
}

// WatchService manages subscribers and publishes cache change events.
type WatchService struct {
	mu          sync.RWMutex
	subscribers []*Subscriber
}

// NewWatchService creates a new WatchService.
func NewWatchService() *WatchService {
	return &WatchService{}
}

// Subscribe registers a new subscriber for the given pattern.
func (w *WatchService) Subscribe(pattern string) *Subscriber {
	id := fmt.Sprintf("sub-%d", atomic.AddInt64(&subIDCounter, 1))
	s := &Subscriber{
		ID:      id,
		Pattern: pattern,
		Ch:      make(chan *pb.WatchEvent, 64),
	}
	w.mu.Lock()
	w.subscribers = append(w.subscribers, s)
	w.mu.Unlock()
	return s
}

// Unsubscribe removes a subscriber and closes its channel.
func (w *WatchService) Unsubscribe(s *Subscriber) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, sub := range w.subscribers {
		if sub == s {
			w.subscribers = append(w.subscribers[:i], w.subscribers[i+1:]...)
			sub.Close()
			return
		}
	}
}

// List returns a snapshot of all active subscribers.
func (w *WatchService) List() []*Subscriber {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]*Subscriber, len(w.subscribers))
	copy(out, w.subscribers)
	return out
}

// Kill removes and closes a subscriber by ID. Returns false if not found.
func (w *WatchService) Kill(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, sub := range w.subscribers {
		if sub.ID == id {
			w.subscribers = append(w.subscribers[:i], w.subscribers[i+1:]...)
			sub.Close()
			return true
		}
	}
	return false
}

// Publish sends an event to all matching subscribers.
func (w *WatchService) Publish(evt *pb.WatchEvent) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, s := range w.subscribers {
		if s.Matches(evt.Key) {
			s.Send(evt)
		}
	}
}

// Close removes and closes all subscribers.
func (w *WatchService) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.subscribers {
		s.Close()
	}
	w.subscribers = nil
}

// PublishSet publishes a SET event for the given key/value.
func (w *WatchService) PublishSet(key string, value *anypb.Any) {
	w.Publish(&pb.WatchEvent{
		Type:  pb.WatchEventType_EVENT_SET,
		Key:   key,
		Value: value,
	})
}

// PublishDel publishes a DEL event for the given key.
func (w *WatchService) PublishDel(key string) {
	w.Publish(&pb.WatchEvent{
		Type: pb.WatchEventType_EVENT_DEL,
		Key:  key,
	})
}

// PublishExpire publishes an EXPIRE event for the given key.
func (w *WatchService) PublishExpire(key string) {
	w.Publish(&pb.WatchEvent{
		Type: pb.WatchEventType_EVENT_EXPIRE,
		Key:  key,
	})
}

// ---------------------------------------------------------------------------
// CacheService Watch handler
// ---------------------------------------------------------------------------

// Watch implements pb.CacheServiceServer.
func (s *CacheService) Watch(req *pb.WatchRequest, stream pb.CacheService_WatchServer) error {
	if s.watchSvc == nil {
		return status.Error(codes.FailedPrecondition, "watch service not available")
	}
	sub := s.watchSvc.Subscribe(req.Pattern)
	defer s.watchSvc.Unsubscribe(sub)

	for {
		select {
		case evt, ok := <-sub.Ch:
			if !ok {
				return nil
			}
			if err := stream.Send(evt); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}
