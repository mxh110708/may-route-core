package v2rayxhttp

import (
	"context"
	stdtls "crypto/tls"
	"encoding/pem"
	W "github.com/sagernet/sing-box/common/xhttpwire"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
)

type testDialer struct{ net.Dialer }

func (d *testDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return d.Dialer.DialContext(ctx, network, destination.String())
}
func (d *testDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	panic("TCP only")
}

func testClient(t *testing.T, handler http.Handler, h2 bool) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = h2
	server.TLS = &stdtls.Config{MinVersion: stdtls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)
	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	config, err := tls.NewClient(context.Background(), logger.NOP(), "127.0.0.1", option.OutboundTLSOptions{Enabled: true, ServerName: "example.com", Certificate: []string{certificate}})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(server.URL)
	transport, err := NewClient(context.Background(), &testDialer{}, M.ParseSocksaddr(u.Host), option.V2RayXHTTPOptions{Host: "front.example.test", Path: "/may-stream/", Mode: "stream-one"}, config)
	if err != nil {
		t.Fatal(err)
	}
	c := transport.(*Client)
	t.Cleanup(func() { c.Close() })
	return c, server
}

func echo(t *testing.T, opened *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 || r.Method != "POST" || r.Host != "front.example.test" || r.URL.Path != "/may-stream/" || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/grpc" {
			t.Error("wire identity changed")
			w.WriteHeader(400)
			return
		}
		referrer, err := url.Parse(r.Header.Get("Referer"))
		if err != nil || len(referrer.Query().Get("x_padding")) < 100 || len(referrer.Query().Get("x_padding")) > 1000 {
			t.Error("missing bounded XHTTP padding")
			w.WriteHeader(400)
			return
		}
		opened.Add(1)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		buffer := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buffer)
			if n > 0 {
				if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
			if err != nil {
				return
			}
		}
	})
}

func TestStreamsReuseAndReset(t *testing.T) {
	var opened atomic.Int64
	c, _ := testClient(t, echo(t, &opened), true)
	for i := 0; i < 8; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		conn, err := c.DialContext(ctx)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		payload := strings.Repeat("payload", 4096)
		done := make(chan error, 1)
		go func() { _, err := io.WriteString(conn, payload); conn.(*Conn).CloseWrite(); done <- err }()
		data, err := io.ReadAll(conn)
		if err != nil || string(data) != payload {
			t.Fatal("duplex payload differs", err)
		}
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		conn.Close()
		cancel()
	}
	if opened.Load() != 8 {
		t.Fatal("streams missing")
	}
	c.generation.mu.Lock()
	sockets := len(c.generation.connections)
	c.generation.mu.Unlock()
	if sockets != 1 {
		t.Fatal("serial requests did not reuse one h2 socket", sockets)
	}
	old := c.generation
	c.Close()
	old.mu.Lock()
	sockets = len(old.connections)
	old.mu.Unlock()
	if sockets != 0 {
		t.Fatal("reset kept old physical sockets")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := c.DialContext(ctx)
	if err != nil {
		t.Fatal("network reset prevented new dials", err)
	}
	conn.Close()
}

func TestCancellationAndClose(t *testing.T) {
	var opened atomic.Int64
	c, _ := testClient(t, echo(t, &opened), true)
	for _, reset := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		conn, err := c.DialContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		// The enclosing dialer releases its context on success. It must not
		// tear down an established stream before VLESS sends its first bytes.
		written := make(chan error, 1)
		go func() { _, err := io.WriteString(conn, "ready"); written <- err }()
		data := make([]byte, 5)
		if _, err = io.ReadFull(conn, data); err != nil || string(data) != "ready" {
			t.Fatal("dial-context release killed an established stream", err)
		}
		if err = <-written; err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = conn.Read(make([]byte, 1)) }()
		if reset {
			c.Close()
		} else {
			conn.Close()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("cancel left a blocked read")
		}
		conn.Close()
		cancel()
	}
}

func TestHandshakeCancellationAndStatus(t *testing.T) {
	for _, status := range []int{301, 401, 404, 500, 0} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == 0 {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(status)
			}), true)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			if conn, err := c.DialContext(ctx); err == nil {
				conn.Close()
				t.Fatal("bad response or stalled headers accepted")
			}
		})
	}
}

func TestNoHTTP1FallbackAndIdleCleanup(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("HTTP/1 fallback reached handler") }), false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, err := c.DialContext(ctx); err == nil {
		conn.Close()
		t.Fatal("HTTP/1 accepted")
	}
	var opened atomic.Int64
	c, _ = testClient(t, echo(t, &opened), true)
	c.generation.transport.IdleConnTimeout = 30 * time.Millisecond
	conn, err := c.DialContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	until := time.Now().Add(2 * time.Second)
	for {
		c.generation.mu.Lock()
		n := len(c.generation.connections)
		c.generation.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("idle pool did not release socket")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConcurrentStreamsAndRootCancellation(t *testing.T) {
	var opened atomic.Int64
	c, _ := testClient(t, echo(t, &opened), true)
	root, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	c.ctx = root
	c.Close()
	var workers sync.WaitGroup
	errors := make(chan error, 16)
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			conn, err := c.DialContext(ctx)
			if err != nil {
				errors <- err
				return
			}
			defer conn.Close()
			written := make(chan error, 1)
			go func() { _, err := io.WriteString(conn, "parallel"); written <- err }()
			buffer := make([]byte, 8)
			_, err = io.ReadFull(conn, buffer)
			if err == nil && string(buffer) != "parallel" {
				err = io.ErrUnexpectedEOF
			}
			if err != nil {
				errors <- err
				return
			}
			errors <- <-written
		}()
	}
	workers.Wait()
	for i := 0; i < 16; i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	conn, err := c.DialContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { _, _ = conn.Read(make([]byte, 1)); close(closed) }()
	g := c.generation
	stop()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("root cancellation kept an active stream")
	}
	until := time.Now().Add(time.Second)
	for {
		g.mu.Lock()
		remaining := len(g.connections)
		g.mu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("root cancellation kept pool sockets")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestXmuxRetirementKeepsActiveStreams(t *testing.T) {
	var opened atomic.Int64
	c, _ := testClient(t, echo(t, &opened), true)
	p := c.state.up
	p.requests = W.Range{Min: 2, Max: 2}
	p.groups[0].current.limit = 2
	var connections []net.Conn
	for i := 0; i < 4; i++ {
		conn, err := c.DialContext(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	for _, conn := range connections {
		written := make(chan error, 1)
		go func() { _, err := io.WriteString(conn, "retained"); written <- err }()
		data := make([]byte, 8)
		if _, err := io.ReadFull(conn, data); err != nil || string(data) != "retained" {
			t.Fatal("retirement interrupted live stream", err)
		}
		if err := <-written; err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}
	p.mu.Lock()
	retained := len(p.all)
	p.mu.Unlock()
	if retained != 1 {
		t.Fatal("expired generations retained after final response", retained)
	}
}
