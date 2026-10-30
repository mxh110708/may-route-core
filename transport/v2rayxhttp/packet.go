package v2rayxhttp

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	W "github.com/sagernet/sing-box/common/xhttpwire"
	C "github.com/sagernet/sing-box/constant"
)

type errorTransport struct{ err error }

func (t errorTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

// At most four batches can await acknowledgement, below the server's configured
// buffer limit. This bounds memory without serializing every upload on one RTT.
// HTTP errors are never replayed after an ambiguous acknowledgement.
type packetWriter struct {
	ctx       context.Context
	cancel    context.CancelFunc
	wire      *W.Config
	session   string
	transport http.RoundTripper
	mu        sync.Mutex
	buffer    []byte
	limit     int
	interval  time.Duration
	last      time.Time
	seq       uint64
	timer     *time.Timer
	err       error
	closed    bool
	active    chan struct{}
	workers   sync.WaitGroup
}

func newPacketWriter(ctx context.Context, cancel context.CancelFunc, w *W.Config, session string, transport http.RoundTripper) *packetWriter {
	max, _ := w.GetNormalizedScMaxEachPostBytes()
	interval, _ := w.GetNormalizedScMinPostsIntervalMs()
	posts, err := w.GetNormalizedScMaxBufferedPosts()
	capacity := 4
	if err == nil && posts.Max > 0 {
		capacity = min(capacity, posts.Max)
	}
	return &packetWriter{ctx: ctx, cancel: cancel, wire: w, session: session, transport: transport, limit: max.Rand(), interval: time.Duration(interval.Rand()) * time.Millisecond, active: make(chan struct{}, capacity)}
}
func (p *packetWriter) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, net.ErrClosed
	}
	if p.err != nil {
		return 0, p.err
	}
	written := 0
	for len(data) > 0 {
		if err := p.ctx.Err(); err != nil {
			p.err = err
			return written, err
		}
		n := min(len(data), p.limit-len(p.buffer))
		if needed := len(p.buffer) + n; needed > cap(p.buffer) {
			// Grow geometrically, capped at this request's validated limit.
			// Do not preallocate the full POST for small sessions or retain
			// acknowledged batches in a global pool.
			buffer := make([]byte, len(p.buffer), min(p.limit, max(needed, 2*cap(p.buffer))))
			copy(buffer, p.buffer)
			p.buffer = buffer
		}
		p.buffer = append(p.buffer, data[:n]...)
		written += n
		data = data[n:]
		if len(p.buffer) == p.limit {
			if err := p.flush(); err != nil {
				return written, err
			}
		}
	}
	if len(p.buffer) > 0 && p.timer == nil {
		delay := max(time.Until(p.last.Add(p.interval)), time.Millisecond)
		p.timer = time.AfterFunc(delay, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.timer = nil
			if !p.closed && p.err == nil {
				p.flush()
			}
		})
	}
	return written, nil
}

// flush is serialized with writes, and releases the batch before the next one.
func (p *packetWriter) flush() error {
	if len(p.buffer) == 0 {
		return p.err
	}
	if wait := time.Until(p.last.Add(p.interval)); wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-p.ctx.Done():
			timer.Stop()
			p.err = p.ctx.Err()
			return p.err
		}
	}
	u := p.wire.URL()
	select {
	case p.active <- struct{}{}:
	case <-p.ctx.Done():
		p.err = p.ctx.Err()
		return p.err
	}
	ctx, cancel := context.WithTimeout(p.ctx, C.TCPTimeout)
	req, err := http.NewRequestWithContext(ctx, p.wire.GetNormalizedUplinkHTTPMethod(), u.String(), nil)
	if err == nil {
		req.Host = p.wire.Host
		err = p.wire.FillPacketRequest(req, p.session, strconv.FormatUint(p.seq, 10), p.buffer)
	}
	p.buffer = nil
	p.last = time.Now()
	p.seq++
	if err != nil {
		cancel()
		<-p.active
		p.err = err
		p.cancel()
		return err
	}
	p.workers.Add(1)
	go func() {
		defer p.workers.Done()
		defer cancel()
		r, err := p.transport.RoundTrip(req)
		if err == nil {
			if r.StatusCode != 200 {
				err = fmt.Errorf("XHTTP packet upload returned status %d", r.StatusCode)
			} else {
				_, err = io.Copy(io.Discard, io.LimitReader(r.Body, 64<<10))
			}
			r.Body.Close()
		}
		// Release capacity before taking the writer lock: a blocked writer can
		// then observe cancellation without deadlocking an error acknowledgement.
		<-p.active
		if err != nil {
			p.cancel()
			p.mu.Lock()
			if p.err == nil {
				p.err = err
			}
			p.mu.Unlock()
		}
	}()
	return nil
}
func (p *packetWriter) closeWrite() error {
	p.mu.Lock()
	if p.closed {
		err := p.err
		p.mu.Unlock()
		return err
	}
	err := p.flush()
	p.closed = true
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.mu.Unlock()
	p.workers.Wait()
	p.mu.Lock()
	if err == nil {
		err = p.err
	}
	p.mu.Unlock()
	return err
}
func (p *packetWriter) Close() error {
	p.cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.buffer = nil
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	return nil
}
