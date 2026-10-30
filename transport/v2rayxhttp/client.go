// Native XHTTP client. Wire helpers derive from pinned Mihomo/Xray sources;
// transports always use the selected sing-box dialer, TLS and network context.
package v2rayxhttp

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	W "github.com/sagernet/sing-box/common/xhttpwire"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var _ adapter.V2RayClientTransport = (*Client)(nil)

type Client struct {
	ctx      context.Context
	dialer   N.Dialer
	server   M.Socksaddr
	up, down *endpoint
	mode     string
	mu       sync.Mutex
	state    *clientState
	// Exposes the initial HTTP/2 pool generation to package lifecycle tests.
	generation *generation
}

type endpoint struct {
	server  M.Socksaddr
	tls     tls.Config
	wire    *W.Config
	version string
	reality bool
}

func (e *endpoint) headerBudget() int {
	padding, _ := e.wire.GetNormalizedXPaddingBytes()
	return max(64<<10, padding.Max*2+(8<<10))
}

type clientState struct {
	ctx      context.Context
	cancel   context.CancelFunc
	up, down *pool
}

func NewClient(ctx context.Context, dialer N.Dialer, server M.Socksaddr, o option.V2RayXHTTPOptions, config tls.Config) (adapter.V2RayClientTransport, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	up, err := makeEndpoint(server, o, config)
	if err != nil {
		return nil, err
	}
	c := &Client{ctx: ctx, dialer: dialer, server: server, up: up, down: up}
	if d := o.DownloadSettings; d != nil {
		tlsOptions := *d.TLS
		if d.Transport.XHTTPOptions.HTTPVersion == "3" || len(tlsOptions.ALPN) == 1 && tlsOptions.ALPN[0] == "h3" {
			tlsOptions.UTLS = nil
		}
		cfg, err := tls.NewClient(ctx, logger.NOP(), d.Server, tlsOptions)
		if err != nil {
			return nil, err
		}
		c.down, err = makeEndpoint(d.ServerOptions.Build(), d.Transport.XHTTPOptions, cfg)
		if err != nil {
			return nil, err
		}
		up.wire.DownloadConfig = c.down.wire
	}
	c.mode = up.wire.EffectiveMode(up.reality)
	c.state = c.newState()
	c.generation = c.state.up.groups[0].current
	return c, nil
}

func makeEndpoint(server M.Socksaddr, o option.V2RayXHTTPOptions, config tls.Config) (*endpoint, error) {
	if config == nil {
		return nil, fmt.Errorf("XHTTP requires authenticated TLS/Reality")
	}
	w, err := W.ParseExtra(o.Extra)
	if err != nil {
		return nil, err
	}
	w.Host = o.Host
	if w.Host == "" {
		w.Host = config.ServerName()
	}
	if w.Host == "" {
		w.Host = server.AddrString()
	}
	w.Path = o.Path
	w.Mode = o.Mode
	if err = w.ValidateMode(); err != nil {
		return nil, err
	}
	e := &endpoint{server: server, tls: config.Clone(), wire: w, version: o.HTTPVersion}
	if marker, ok := config.(interface{ IsReality() bool }); ok {
		e.reality = marker.IsReality()
	}
	if e.version == "" || e.version == "auto" {
		e.version = "2"
		if p := config.NextProtos(); len(p) == 1 {
			switch p[0] {
			case "http/1.1":
				e.version = "1.1"
			case "h3":
				e.version = "3"
			}
		}
	}
	if e.reality && e.version != "2" {
		return nil, fmt.Errorf("fixed Xray REALITY XHTTP requires HTTP/2")
	}
	if e.version == "3" && !quicAvailable {
		return nil, C.ErrQUICNotIncluded
	}
	if e.version == "3" {
		cfg, err := e.tls.STDConfig()
		if err != nil {
			return nil, err
		}
		if cfg.MaxVersion != 0 && cfg.MaxVersion < tls.VersionTLS13 {
			return nil, fmt.Errorf("XHTTP HTTP/3 requires TLS 1.3")
		}
	}
	alpn := map[string]string{"1.1": "http/1.1", "2": "h2", "3": "h3"}[e.version]
	for _, p := range config.NextProtos() {
		if p == alpn {
			goto compatible
		}
	}
	if len(config.NextProtos()) > 0 {
		return nil, fmt.Errorf("XHTTP HTTP version conflicts with TLS ALPN")
	}
compatible:
	e.tls.SetNextProtos([]string{alpn})
	return e, nil
}

func (c *Client) newState() *clientState {
	ctx, cancel := context.WithCancel(c.ctx)
	s := &clientState{ctx: ctx, cancel: cancel}
	s.up = newPool(ctx, c.dialer, c.up)
	s.down = s.up
	if c.down != c.up {
		s.down = newPool(ctx, c.dialer, c.down)
	}
	context.AfterFunc(ctx, func() {
		s.up.close()
		if s.down != s.up {
			s.down.close()
		}
	})
	return s
}

func (c *Client) Close() error {
	c.mu.Lock()
	old := c.state
	c.state = c.newState()
	c.generation = c.state.up.groups[0].current
	c.mu.Unlock()
	old.cancel()
	old.up.close()
	if old.down != old.up {
		old.down.close()
	}
	return nil
}

func (c *Client) DialContext(ctx context.Context) (net.Conn, error) {
	c.mu.Lock()
	s := c.state
	c.mu.Unlock()
	requestCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopRoot := context.AfterFunc(s.ctx, cancel)
	dialCtx, dialCancel := context.WithTimeout(ctx, C.TCPTimeout)
	defer dialCancel()
	stopDial := context.AfterFunc(dialCtx, cancel)
	defer stopDial()
	up, err := s.up.acquire()
	if err != nil {
		stopRoot()
		cancel()
		return nil, err
	}
	down := up
	if s.down != s.up {
		down, err = s.down.acquire()
		if err != nil {
			up.Close()
			stopRoot()
			cancel()
			return nil, err
		}
	}
	reader := newResponseReader()
	conn := &Conn{reader: reader, cancel: cancel, remote: c.server}
	conn.cleanup = func() {
		stopRoot()
		up.Close()
		if down != up {
			down.Close()
		}
	}
	ready := make(chan struct{}, 1)
	headers := make(chan struct{}, 1)
	failed := make(chan error, 2)
	traceCtx := httptrace.WithClientTrace(requestCtx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) {
		select {
		case ready <- struct{}{}:
		default:
		}
	}})
	session := ""
	if c.mode != "stream-one" {
		session = W.NewSession()
	}
	type pending struct {
		transport http.RoundTripper
		req       *http.Request
		download  bool
	}
	var pendingRequests []pending
	startResponse := func(transport http.RoundTripper, req *http.Request, download bool) {
		pendingRequests = append(pendingRequests, pending{transport, req, download})
	}
	runResponse := func(transport http.RoundTripper, req *http.Request, download bool) {
		go func() {
			watch := time.AfterFunc(C.TCPTimeout, cancel)
			response, err := transport.RoundTrip(req)
			watch.Stop()
			if err == nil && response.StatusCode != http.StatusOK {
				response.Body.Close()
				err = fmt.Errorf("XHTTP unexpected status %d", response.StatusCode)
			}
			if err == nil && response.ProtoMajor != map[string]int{"1.1": 1, "2": 2, "3": 3}[func() string {
				if download {
					return c.down.version
				}
				return c.up.version
			}()] {
				response.Body.Close()
				err = fmt.Errorf("XHTTP protocol version changed")
			}
			if err != nil {
				reader.set(nil, err)
				select {
				case failed <- err:
				default:
				}
				cancel()
				return
			}
			if download {
				reader.set(response.Body, nil)
				headers <- struct{}{}
			} else {
				defer response.Body.Close()
				_, err = io.Copy(io.Discard, response.Body)
				if err != nil {
					cancel()
				}
			}
		}()
	}
	if c.mode == "stream-one" {
		r, w := io.Pipe()
		conn.writer = w
		conn.closeUpload = r.Close
		u := c.up.wire.URL()
		req, requestErr := http.NewRequestWithContext(traceCtx, c.up.wire.GetNormalizedUplinkHTTPMethod(), u.String(), r)
		if requestErr != nil {
			conn.Close()
			return nil, requestErr
		}
		req.Host = c.up.wire.Host
		if err = c.up.wire.FillStreamRequest(req, ""); err != nil {
			conn.Close()
			return nil, err
		}
		startResponse(up, req, true)
	} else {
		u := c.down.wire.URL()
		req, requestErr := http.NewRequestWithContext(traceCtx, http.MethodGet, u.String(), nil)
		if requestErr != nil {
			conn.Close()
			return nil, requestErr
		}
		req.Host = c.down.wire.Host
		if err = c.down.wire.FillDownloadRequest(req, session); err != nil {
			conn.Close()
			return nil, err
		}
		if c.mode == "stream-up" {
			r, w := io.Pipe()
			conn.writer = w
			conn.closeUpload = r.Close
			u := c.up.wire.URL()
			upload, requestErr := http.NewRequestWithContext(requestCtx, c.up.wire.GetNormalizedUplinkHTTPMethod(), u.String(), r)
			if requestErr != nil {
				conn.Close()
				return nil, requestErr
			}
			upload.Host = c.up.wire.Host
			if err = c.up.wire.FillStreamRequest(upload, session); err != nil {
				conn.Close()
				return nil, err
			}
			startResponse(up, upload, false)
		} else {
			conn.writer = newPacketWriter(requestCtx, cancel, c.up.wire, session, up)
		}
		startResponse(down, req, true)
	}
	// Register cleanup only after construction; cancellation must not race with
	// assigning the pipe or packet writer. All requests start after that point.
	context.AfterFunc(requestCtx, func() { conn.Close() })
	for _, p := range pendingRequests {
		runResponse(p.transport, p.req, p.download)
	}
	completion := ready
	if c.mode == "stream-one" {
		completion = headers
	}
	select {
	case <-completion:
		stopDial()
		if err = dialCtx.Err(); err != nil {
			conn.Close()
			return nil, err
		}
		if err = requestCtx.Err(); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	case err = <-failed:
		conn.Close()
		return nil, err
	case <-dialCtx.Done():
		conn.Close()
		return nil, dialCtx.Err()
	case <-requestCtx.Done():
		conn.Close()
		return nil, requestCtx.Err()
	}
}

type responseReader struct {
	mu      sync.Mutex
	ready   chan struct{}
	body    io.ReadCloser
	err     error
	setOnce sync.Once
}

func newResponseReader() *responseReader { return &responseReader{ready: make(chan struct{})} }
func (r *responseReader) set(body io.ReadCloser, err error) {
	set := false
	r.setOnce.Do(func() {
		r.mu.Lock()
		r.body = body
		r.err = err
		r.mu.Unlock()
		close(r.ready)
		set = true
	})
	if !set && body != nil {
		body.Close()
	}
}
func (r *responseReader) Read(p []byte) (int, error) {
	<-r.ready
	r.mu.Lock()
	body, err := r.body, r.err
	r.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return body.Read(p)
}
func (r *responseReader) Close() error {
	r.set(nil, net.ErrClosed)
	r.mu.Lock()
	body := r.body
	r.mu.Unlock()
	if body != nil {
		return body.Close()
	}
	return nil
}

type Conn struct {
	reader      io.ReadCloser
	writer      io.WriteCloser
	cancel      context.CancelFunc
	cleanup     func()
	closeUpload func() error
	remote      net.Addr
	once        sync.Once
}

func (c *Conn) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	if err != nil {
		c.Close()
	}
	return n, err
}
func (c *Conn) Write(p []byte) (int, error) {
	n, err := c.writer.Write(p)
	if err != nil {
		c.Close()
	}
	return n, err
}
func (c *Conn) CloseWrite() error {
	if p, ok := c.writer.(*packetWriter); ok {
		return p.closeWrite()
	}
	return c.writer.Close()
}
func (c *Conn) Close() error {
	c.once.Do(func() {
		c.cancel()
		if c.closeUpload != nil {
			c.closeUpload()
		}
		if c.writer != nil {
			c.writer.Close()
		}
		c.reader.Close()
		if c.cleanup != nil {
			c.cleanup()
		}
	})
	return nil
}
func (c *Conn) LocalAddr() net.Addr              { return M.Socksaddr{} }
func (c *Conn) RemoteAddr() net.Addr             { return c.remote }
func (c *Conn) SetDeadline(time.Time) error      { return os.ErrInvalid }
func (c *Conn) SetReadDeadline(time.Time) error  { return os.ErrInvalid }
func (c *Conn) SetWriteDeadline(time.Time) error { return os.ErrInvalid }
func (c *Conn) NeedAdditionalReadDeadline() bool { return true }
