package collab

import "sync"

const MaxSubscribersPerRoom = 512

// Hub coalesces wakeups instead of queueing event bodies. Subscribers always
// fetch persisted events by sequence, so a slow client cannot block writers or
// permanently lose a frame.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[string]map[chan struct{}]struct{}{}} }

func (h *Hub) Subscribe(room string) (<-chan struct{}, func()) {
	ch, cancel, err := h.TrySubscribe(room)
	if err == nil {
		return ch, cancel
	}
	closed := make(chan struct{})
	close(closed)
	return closed, func() {}
}

func (h *Hub) TrySubscribe(room string) (<-chan struct{}, func(), error) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if len(h.subs[room]) >= MaxSubscribersPerRoom {
		h.mu.Unlock()
		return nil, nil, fail(CodeUnavailable, "room subscriber limit reached")
	}
	if h.subs[room] == nil {
		h.subs[room] = map[chan struct{}]struct{}{}
	}
	h.subs[room][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if roomSubs := h.subs[room]; roomSubs != nil {
			if _, ok := roomSubs[ch]; ok {
				delete(roomSubs, ch)
				close(ch)
			}
			if len(roomSubs) == 0 {
				delete(h.subs, room)
			}
		}
		h.mu.Unlock()
	}, nil
}

func (h *Hub) Publish(room string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[room] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) Subscribers(room string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[room])
}
