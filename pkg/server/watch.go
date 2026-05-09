package server

import (
	"path/filepath"
	"sync"

	"sync/atomic"

	"github.com/lushenle/simple-cache/pkg/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

var droppedEvents int64

// subscriber holds a channel onto which watch events are pushed.
type subscriber struct {
	pattern string
	ch      chan *pb.WatchEvent
	mu      sync.Mutex
	closed  bool
}

func (s *subscriber) send(evt *pb.WatchEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- evt:
	default:
		atomic.AddInt64(&droppedEvents, 1)
	}
}

func (s *subscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		close(s.ch)
		s.closed = true
	}
}

// watcher matches patterns against keys.
func (s *subscriber) matches(key string) bool {
	if s.pattern == "" {
		return true
	}
	matched, _ := filepath.Match(s.pattern, key)
	return matched
}

// WatchService manages subscribers and publishes cache change events.
type WatchService struct {
	mu          sync.RWMutex
	subscribers []*subscriber
}

// NewWatchService creates a new WatchService.
func NewWatchService() *WatchService {
	return &WatchService{}
}

// Subscribe registers a new subscriber for the given pattern.
// Returns a channel that receives WatchEvent values. The caller must
// call Unsubscribe when done to avoid leaking goroutines.
func (w *WatchService) Subscribe(pattern string) *subscriber {
	s := &subscriber{
		pattern: pattern,
		ch:      make(chan *pb.WatchEvent, 64),
	}
	w.mu.Lock()
	w.subscribers = append(w.subscribers, s)
	w.mu.Unlock()
	return s
}

// Unsubscribe removes a subscriber and closes its channel.
func (w *WatchService) Unsubscribe(s *subscriber) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, sub := range w.subscribers {
		if sub == s {
			w.subscribers = append(w.subscribers[:i], w.subscribers[i+1:]...)
			sub.close()
			return
		}
	}
}

// Publish sends an event to all matching subscribers.
func (w *WatchService) Publish(evt *pb.WatchEvent) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, s := range w.subscribers {
		if s.matches(evt.Key) {
			s.send(evt)
		}
	}
}

// Close removes and closes all subscribers.
func (w *WatchService) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.subscribers {
		s.close()
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
		case evt, ok := <-sub.ch:
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
