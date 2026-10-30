package v2rayxhttp

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	W "github.com/sagernet/sing-box/common/xhttpwire"
	N "github.com/sagernet/sing/common/network"
	"golang.org/x/net/http2"
)

// A lease counts a logical proxy session. HTTP request limits are accounted for
// separately: a packet-up session can issue many requests while its download
// remains on an older connection. Retiring a generation never interrupts it.
type pool struct {
	ctx                      context.Context
	dialer                   N.Dialer
	endpoint                 *endpoint
	mu                       sync.Mutex
	closed                   bool
	closeDone                chan struct{}
	groups                   []*group
	all                      map[*generation]struct{}
	concurrency, connections int
	reuse, requests, seconds W.Range
	idleTimeout              time.Duration
}
type group struct {
	active, uses, limit int
	current             *generation
	idleTimer           *time.Timer
}
type lease struct {
	pool   *pool
	group  *group
	closed bool
}
type generation struct {
	ctx          context.Context
	cancel       context.CancelFunc
	roundTripper http.RoundTripper
	transport    *http2.Transport
	shutdown     func()
	mu           sync.Mutex
	connections  map[*trackedConn]struct{}
	// The pool mutex protects request ownership and retirement.
	refs, requests, limit int
	until                 time.Time
	retired               bool
	closeOnce             sync.Once
}

func newPool(ctx context.Context, dialer N.Dialer, e *endpoint) *pool {
	p := &pool{ctx: ctx, dialer: dialer, endpoint: e, all: make(map[*generation]struct{}), closeDone: make(chan struct{}), idleTimeout: 30 * time.Second}
	concurrency, connections, _ := e.wire.ReuseConfig.ResolveManagerConfig()
	p.concurrency, p.connections = concurrency.Rand(), connections.Rand()
	p.reuse, p.requests, p.seconds, _ = e.wire.ReuseConfig.ResolveEntryConfig()
	p.addGroup()
	return p
}
func (p *pool) addGroup() *group {
	g := &group{limit: p.reuse.Rand(), current: p.newGeneration()}
	p.groups = append(p.groups, g)
	return g
}
func (p *pool) acquire() (*lease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.ctx.Err() != nil {
		return nil, net.ErrClosed
	}
	// Remove exhausted logical clients only after all their sessions have left.
	for i := 0; i < len(p.groups); {
		g := p.groups[i]
		if g.active == 0 && g.limit > 0 && g.uses >= g.limit {
			p.retire(g.current)
			p.groups = append(p.groups[:i], p.groups[i+1:]...)
		} else {
			i++
		}
	}
	var selected *group
	if p.connections == 0 || len(p.groups) >= p.connections {
		for _, g := range p.groups {
			if g.limit > 0 && g.uses >= g.limit || p.concurrency > 0 && g.active >= p.concurrency {
				continue
			}
			if selected == nil || g.active < selected.active {
				selected = g
			}
		}
	}
	if selected == nil {
		selected = p.addGroup()
	}
	selected.active++
	if selected.idleTimer != nil {
		selected.idleTimer.Stop()
		selected.idleTimer = nil
	}
	selected.uses++
	return &lease{pool: p, group: selected}, nil
}
func (p *pool) retire(g *generation) {
	g.retired = true
	if g.refs == 0 {
		delete(p.all, g)
		g.closeSockets()
	}
}
func (l *lease) RoundTrip(req *http.Request) (*http.Response, error) {
	p := l.pool
	p.mu.Lock()
	if l.closed || p.closed {
		p.mu.Unlock()
		return nil, net.ErrClosed
	}
	g := l.group.current
	if g.retired || g.limit > 0 && g.requests >= g.limit || !g.until.IsZero() && time.Now().After(g.until) {
		p.retire(g)
		g = p.newGeneration()
		l.group.current = g
	}
	g.refs++
	g.requests++
	p.mu.Unlock()
	release := func() {
		p.mu.Lock()
		g.refs--
		if g.retired && g.refs == 0 {
			delete(p.all, g)
			g.closeSockets()
		}
		p.mu.Unlock()
	}
	r, err := g.roundTripper.RoundTrip(req)
	if err != nil {
		release()
		return nil, err
	}
	r.Body = &ownedBody{ReadCloser: r.Body, release: release}
	return r, nil
}
func (l *lease) Close() error {
	p := l.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	if !l.closed {
		l.closed = true
		l.group.active--
		if l.group.active == 0 && l.group.limit > 0 && l.group.uses >= l.group.limit {
			p.retire(l.group.current)
		}
		if l.group.active == 0 && len(p.groups) > 1 {
			g := l.group
			g.idleTimer = time.AfterFunc(p.idleTimeout, func() {
				p.mu.Lock()
				defer p.mu.Unlock()
				if p.closed || g.active != 0 || len(p.groups) <= 1 {
					return
				}
				for i, other := range p.groups {
					if g == other {
						p.groups = append(p.groups[:i], p.groups[i+1:]...)
						p.retire(g.current)
						break
					}
				}
			})
		}
	}
	return nil
}

type ownedBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *ownedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.Close()
	}
	return n, err
}
func (b *ownedBody) Close() error { err := b.ReadCloser.Close(); b.once.Do(b.release); return err }
func (p *pool) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		<-p.closeDone
		return
	}
	p.closed = true
	for _, g := range p.groups {
		if g.idleTimer != nil {
			g.idleTimer.Stop()
		}
	}
	all := make([]*generation, 0, len(p.all))
	for g := range p.all {
		all = append(all, g)
	}
	p.all = make(map[*generation]struct{})
	p.mu.Unlock()
	defer close(p.closeDone)
	for _, g := range all {
		g.closeSockets()
	}
}

func (p *pool) newGeneration() *generation {
	ctx, cancel := context.WithCancel(p.ctx)
	g := &generation{ctx: ctx, cancel: cancel, connections: make(map[*trackedConn]struct{}), limit: p.requests.Rand()}
	if seconds := p.seconds.Rand(); seconds > 0 {
		g.until = time.Now().Add(time.Duration(seconds) * time.Second)
	}
	e := p.endpoint
	keepAlive := time.Duration(0)
	if e.wire.ReuseConfig != nil && e.wire.ReuseConfig.HKeepAlivePeriod > 0 {
		keepAlive = time.Duration(e.wire.ReuseConfig.HKeepAlivePeriod) * time.Second
	}
	if e.version == "3" {
		g.roundTripper, g.shutdown = newHTTP3(ctx, p.dialer, e, keepAlive)
	} else {
		dial := func(ctx context.Context, _, _ string, _ *tls.STDConfig) (net.Conn, error) {
			dialCtx, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(g.ctx, cancel)
			defer func() { stop(); cancel() }()
			conn, err := tls.NewDialer(p.dialer, e.tls).DialTLSContext(dialCtx, e.server)
			if err != nil {
				return nil, err
			}
			state := conn.ConnectionState()
			expected := "h2"
			if e.version == "1.1" {
				expected = "http/1.1"
			}
			valid := state.NegotiatedProtocol == expected || state.NegotiatedProtocol == "" && (e.reality || e.version == "1.1")
			if !valid || state.Version < tls.VersionTLS12 || e.reality && state.Version != tls.VersionTLS13 {
				conn.NetConn().Close()
				return nil, fmt.Errorf("XHTTP negotiated incompatible TLS/ALPN")
			}
			t := &trackedConn{Conn: conn, owner: g}
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.ctx.Err() != nil {
				conn.NetConn().Close()
				return nil, g.ctx.Err()
			}
			g.connections[t] = struct{}{}
			return t, nil
		}
		if e.version == "2" {
			g.transport = &http2.Transport{DisableCompression: true, MaxHeaderListSize: uint32(e.headerBudget()), StrictMaxConcurrentStreams: true, IdleConnTimeout: 30 * time.Second, ReadIdleTimeout: keepAlive, DialTLSContext: dial}
			g.roundTripper = g.transport
			g.shutdown = g.transport.CloseIdleConnections
		} else {
			// Match Xray's HTTP/1 client: Connection: close prevents the server
			// draining a live chunked upload before flushing its response headers.
			t := &http.Transport{DisableCompression: true, DisableKeepAlives: true, ForceAttemptHTTP2: false, MaxResponseHeaderBytes: int64(e.headerBudget()), IdleConnTimeout: 30 * time.Second,
				DialTLSContext: func(ctx context.Context, n, a string) (net.Conn, error) { return dial(ctx, n, a, nil) }}
			g.roundTripper = t
			g.shutdown = t.CloseIdleConnections
		}
	}
	p.all[g] = struct{}{}
	context.AfterFunc(ctx, g.closeSockets)
	return g
}
func (g *generation) closeSockets() {
	g.closeOnce.Do(func() {
		g.cancel()
		g.mu.Lock()
		sockets := make([]*trackedConn, 0, len(g.connections))
		for c := range g.connections {
			sockets = append(sockets, c)
		}
		g.mu.Unlock()
		for _, c := range sockets {
			c.Close()
		}
		g.shutdown()
	})
}

type trackedConn struct {
	tls.Conn
	owner *generation
	once  sync.Once
}

func (c *trackedConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.Conn.NetConn().Close()
		c.owner.mu.Lock()
		delete(c.owner.connections, c)
		c.owner.mu.Unlock()
	})
	return err
}
