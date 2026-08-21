package dsh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// ErrTransportClosed reports that the runtime's stdout ended before a response
// or terminal notification arrived.
var ErrTransportClosed = errors.New("dsh: transport closed")

// maxBufferedNotifications bounds how many notifications the client may hold
// while no handler is installed (the initialize/prompt window). Exceeding it is
// an explicit transport error, never silent unbounded growth.
const maxBufferedNotifications = 256

// Client is a newline-delimited JSON-RPC 2.0 client over a DSH runtime child's
// stdio. A single reader goroutine owns all stdout frames: it resolves pending
// request ids and fans notifications to one handler. Writes are serialized and
// every response is matched by id, so the runtime's concurrent notifications
// never interleave with or corrupt a request result.
type Client struct {
	out io.Writer
	dec *Decoder

	writeMu  sync.Mutex
	notifyMu sync.Mutex
	mu       sync.Mutex
	pending  map[string]*pending
	notify   func(Frame)
	onErr    func(error)
	buffer   []Frame
	nextID   int
	done     chan struct{}
	once     sync.Once
	err      error
}

type pending struct {
	ch chan Frame
}

// NewClient starts the stdout reader over stdin/stdout. maxFrame <= 0 selects
// DefaultMaxFrameSize. The returned client's Done channel closes when the reader
// stops (clean EOF or a read error).
func NewClient(stdin io.Writer, stdout io.Reader, maxFrame int) *Client {
	c := &Client{
		out:     stdin,
		dec:     NewDecoder(stdout, maxFrame),
		pending: make(map[string]*pending),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// SetHandler installs the notification handler. Notifications that arrived
// before a handler was installed are buffered (never dropped) and replayed to
// the handler in wire order by the reader goroutine.
func (c *Client) SetHandler(h func(Frame)) {
	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()

	c.mu.Lock()
	c.notify = h
	if h == nil {
		c.mu.Unlock()
		return
	}
	buf := c.buffer
	c.buffer = nil
	c.mu.Unlock()

	// Replay directly rather than waiting for another stdout frame to wake the
	// reader. notifyMu keeps this replay ordered ahead of a concurrently decoded
	// newer notification.
	for _, f := range buf {
		h(f)
	}
}

// Done returns a channel that closes once the reader goroutine stops.
func (c *Client) Done() <-chan struct{} { return c.done }

// SetTransportErrorHandler installs a callback invoked from the reader goroutine
// when stdout yields a non-EOF read error (for example a malformed frame). A
// clean EOF does not invoke it.
func (c *Client) SetTransportErrorHandler(h func(error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onErr = h
}

// Call sends one JSON-RPC request and awaits its response, decoding a success
// result into dst (when non-nil). It returns an *RPCError for a protocol error
// response, ctx.Err() on cancellation/deadline, or ErrTransportClosed when the
// runtime exited.
func (c *Client) Call(ctx context.Context, method string, params any, dst any) error {
	c.mu.Lock()
	if c.isDoneLocked() {
		c.mu.Unlock()
		return c.readerErr()
	}
	c.nextID++
	id := strconv.Itoa(c.nextID)
	p := &pending{ch: make(chan Frame, 1)}
	c.pending[id] = p
	c.mu.Unlock()
	defer c.removePending(id)

	req, err := Request(id, method, params)
	if err != nil {
		return err
	}
	if err := c.write(req); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case f := <-p.ch:
			return c.resultOf(f, dst)
		case <-c.done:
			// The reader stopped; a response delivered just before EOF may still
			// be queued, so drain it once before reporting the transport loss.
			select {
			case f := <-p.ch:
				return c.resultOf(f, dst)
			default:
				return c.readerErr()
			}
		}
	}
}

// Notify sends a JSON-RPC notification (no id, no response). It is used for
// best-effort control messages that must not block a response stream.
func (c *Client) Notify(method string, params any) error {
	f, err := Notification(method, params)
	if err != nil {
		return err
	}
	return c.write(f)
}

func (c *Client) write(f Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return Encode(c.out, f)
}

func (c *Client) resultOf(f Frame, dst any) error {
	if f.Error != nil {
		return &RPCError{Code: f.Error.Code, Message: f.Error.Message, Data: f.Error.Data}
	}
	if dst == nil {
		return nil
	}
	return f.DecodeResult(dst)
}

func (c *Client) removePending(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, id)
}

func (c *Client) readLoop() {
	defer c.once.Do(func() { close(c.done) })
	for {
		f, err := c.dec.Decode()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.setReaderErr(err)
				if h := c.onErrHandler(); h != nil {
					h(err)
				}
			}
			return
		}
		switch {
		case f.IsResponse():
			c.deliver(f)
		case f.IsNotification():
			if err := c.deliverNotification(f); err != nil {
				c.setReaderErr(err)
				if h := c.onErrHandler(); h != nil {
					h(err)
				}
				return
			}
		case f.IsRequest():
			// A runtime never sends requests to the SDK client; a peer that does
			// is speaking a different protocol.
			c.setReaderErr(fmt.Errorf("dsh: unexpected request from runtime: %s", f.Method))
			return
		default:
			c.setReaderErr(fmt.Errorf("dsh: runtime sent an invalid frame"))
			return
		}
	}
}

// deliverNotification either buffers before handler installation or calls the
// handler immediately. notifyMu serializes it with SetHandler's replay so a
// newer frame can never overtake buffered frames.
func (c *Client) deliverNotification(f Frame) error {
	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()

	c.mu.Lock()
	h := c.notify
	if h != nil {
		c.mu.Unlock()
		h(f)
		return nil
	}
	if len(c.buffer) >= maxBufferedNotifications {
		c.mu.Unlock()
		return fmt.Errorf("dsh: pre-handler notification buffer exceeded %d", maxBufferedNotifications)
	}
	c.buffer = append(c.buffer, f)
	c.mu.Unlock()
	return nil
}

func (c *Client) deliver(f Frame) {
	var id string
	if err := json.Unmarshal(f.ID, &id); err != nil {
		// A non-string id (number/null) has no pending string request; drop it.
		return
	}
	c.mu.Lock()
	p, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if ok {
		p.ch <- f
	}
}

func (c *Client) onErrHandler() func(error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onErr
}

func (c *Client) setReaderErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
	}
}

func (c *Client) readerErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return ErrTransportClosed
}

func (c *Client) isDoneLocked() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}
