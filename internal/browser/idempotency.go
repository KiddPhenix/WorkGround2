package browser

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
)

const maxRequestCache = 256

type RequestState string

const (
	RequestPending   RequestState = "pending"
	RequestCompleted RequestState = "completed"
)

// RequestRecord stores the complete outcome of a write request. done is closed
// exactly once, allowing concurrent duplicates to wait instead of dispatching
// the browser action a second time.
type RequestRecord struct {
	ID        string
	Signature string
	State     RequestState
	Value     any
	Err       error
	done      chan struct{}
}

type RequestCache struct {
	mu    sync.Mutex
	items []*RequestRecord
	byID  map[string]*RequestRecord
}

func NewRequestCache() *RequestCache { return &RequestCache{byID: make(map[string]*RequestRecord)} }

func requestSignature(toolName string, args map[string]any) string {
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte{0})
	if args != nil {
		b, _ := json.Marshal(args)
		h.Write(b)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Begin returns leader=true only for the caller that reserved the request.
func (rc *RequestCache) Begin(requestID, sig string) (*RequestRecord, bool, error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if r, ok := rc.byID[requestID]; ok {
		if r.Signature != sig {
			return nil, false, NewError(ErrRequestIDConflict, fmt.Sprintf("request_id %s already used with different params", requestID), nil)
		}
		return r, false, nil
	}
	r := &RequestRecord{ID: requestID, Signature: sig, State: RequestPending, done: make(chan struct{})}
	rc.items = append(rc.items, r)
	rc.byID[requestID] = r
	rc.evictLocked()
	return r, true, nil
}

func (rc *RequestCache) evictLocked() {
	for len(rc.items) > maxRequestCache {
		idx := -1
		for i, r := range rc.items {
			if r.State == RequestCompleted {
				idx = i
				break
			}
		}
		if idx < 0 {
			return
		}
		old := rc.items[idx]
		rc.items = append(rc.items[:idx], rc.items[idx+1:]...)
		delete(rc.byID, old.ID)
	}
}

func (r *RequestRecord) Wait(ctx context.Context) (any, error) {
	select {
	case <-r.done:
		return r.Value, r.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (rc *RequestCache) Complete(r *RequestRecord, value any, err error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if r.State == RequestCompleted {
		return
	}
	r.Value = value
	r.Err = err
	r.State = RequestCompleted
	close(r.done)
}

func (rc *RequestCache) Clear() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for _, r := range rc.items {
		if r.State == RequestPending {
			r.Err = NewError(ErrBrowserDisconnected, "browser session closed while request was pending", nil)
			r.State = RequestCompleted
			close(r.done)
		}
	}
	rc.items = nil
	rc.byID = make(map[string]*RequestRecord)
}
