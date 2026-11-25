package v2rayxhttp

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/interrupt"
	M "github.com/sagernet/sing/common/metadata"
)

func echoSession(t *testing.T, conn net.Conn) {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := io.WriteString(conn, "still-live"); done <- err }()
	data := make([]byte, len("still-live"))
	read := make(chan error, 1)
	go func() { _, err := io.ReadFull(conn, data); read <- err }()
	for _, pending := range []<-chan error{done, read} {
		select {
		case err := <-pending:
			if err != nil {
				t.Fatal("idle cleanup interrupted an active session:", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("active session stalled")
		}
	}
	if string(data) != "still-live" {
		t.Fatal("active session payload changed")
	}
}

func waitGenerationClosed(t *testing.T, g *generation) {
	t.Helper()
	select {
	case <-g.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("unreferenced idle session retained its physical transport")
	}
}

func currentGeneration(c *Client) *generation {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.state.up
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.groups[0].current
}

func TestUpstreamIdlePolicyPreservesActiveSessionsAndReconnects(t *testing.T) {
	var opened atomic.Int64
	c, _ := testClient(t, echo(t, &opened), true)
	dial := func() net.Conn {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		conn, err := c.DialContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	first, second := dial(), dial()
	g := currentGeneration(c)
	c.SetKeepIdleConnections(false)
	c.CloseIdleConnections()
	echoSession(t, first)
	echoSession(t, second)
	first.Close()
	c.CloseIdleConnections()
	echoSession(t, second)
	second.Close()
	waitGenerationClosed(t, g)

	third := dial()
	echoSession(t, third)
	g = currentGeneration(c)
	c.SetKeepIdleConnections(true)
	third.Close()
	select {
	case <-g.ctx.Done():
		t.Fatal("referenced idle session could not be reused")
	default:
	}
	c.CloseIdleConnections()
	waitGenerationClosed(t, g)

	// Network resets rebuild the pool while retaining the upstream idle policy.
	c.SetKeepIdleConnections(false)
	c.Close()
	fourth := dial()
	echoSession(t, fourth)
	g = currentGeneration(c)
	fourth.Close()
	waitGenerationClosed(t, g)
}

func TestUpstreamURLTestWarmSessionSurvivesUntilMeasuredRequest(t *testing.T) {
	var opened atomic.Int64
	c, _ := testClient(t, echo(t, &opened), true)
	if !c.MultiplexEnabled() {
		t.Fatal("HTTP/2 XHTTP was excluded from session warmup")
	}
	c.SetKeepIdleConnections(false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	warm, err := c.DialContext(adapter.ContextWithKeepSession(ctx))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { warm.Close() })
	echoSession(t, warm)
	g := currentGeneration(c)
	warm.Close()
	select {
	case <-g.ctx.Done():
		t.Fatal("warmup discarded the measured request's session")
	default:
	}
	measured, err := c.DialContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { measured.Close() })
	echoSession(t, measured)
	if currentGeneration(c) != g {
		t.Fatal("measured request did not reuse its warm session")
	}
	measured.Close()
	waitGenerationClosed(t, g)
}

type sessionContextDialer struct {
	testDialer
	t     *testing.T
	calls atomic.Int64
}

func (d *sessionContextDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	metadata := adapter.ContextFrom(ctx)
	if interrupt.IsExternalConnectionFromContext(ctx) || metadata == nil || metadata.Outbound != "may-xhttp" || metadata.Source.IsValid() || metadata.Destination.IsValid() || adapter.KeepSessionFromContext(ctx) != true || adapterDNSTag(ctx) != "" {
		d.t.Error("physical multiplex session inherited a routed flow's interruption or attribution")
	}
	d.calls.Add(1)
	return d.testDialer.DialContext(ctx, network, destination)
}

func adapterDNSTag(ctx context.Context) string {
	tag, _ := adapter.DNSTransportTagFromContext(ctx)
	return tag
}

func externalSessionContext() context.Context {
	ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{Outbound: "may-xhttp", Source: M.ParseSocksaddr("198.51.100.10:1234"), Destination: M.ParseSocksaddr("192.0.2.10:443")})
	ctx = interrupt.ContextWithIsExternalConnection(ctx, true)
	ctx = adapter.ContextWithDNSTransportTag(ctx, "private-dns")
	return adapter.ContextWithKeepSession(ctx)
}

func TestUpstreamHTTP2PhysicalSessionContext(t *testing.T) {
	var opened atomic.Int64
	c, _ := testClient(t, echo(t, &opened), true)
	dialer := &sessionContextDialer{t: t}
	c.state.up.dialer = dialer
	ctx, cancel := context.WithTimeout(externalSessionContext(), 3*time.Second)
	defer cancel()
	conn, err := c.DialContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	echoSession(t, conn)
	if dialer.calls.Load() == 0 {
		t.Fatal("no physical HTTP/2 session was observed")
	}
}
