//go:build with_quic

package v2rayxhttp

import (
	"context"
	stdtls "crypto/tls"
	"net/http"
	"time"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-quic"
	N "github.com/sagernet/sing/common/network"
)

const quicAvailable = true

func newHTTP3(ctx context.Context, dialer N.Dialer, e *endpoint, keepAlive time.Duration) (http.RoundTripper, func()) {
	cfg, err := e.tls.STDConfig()
	if err != nil {
		return errorTransport{err}, func() {}
	}
	t := &http3.Transport{TLSClientConfig: cfg, QUICConfig: &quic.Config{KeepAlivePeriod: keepAlive, MaxIdleTimeout: 30 * time.Second, DisablePathMTUDiscovery: !C.IsLinux && !C.IsWindows}, DisableCompression: true, MaxResponseHeaderBytes: e.headerBudget()}
	t.Dial = func(callCtx context.Context, _ string, _ *stdtls.Config, q *quic.Config) (*quic.Conn, error) {
		dialCtx, cancel := context.WithCancel(callCtx)
		stop := context.AfterFunc(ctx, cancel)
		defer func() { stop(); cancel() }()
		raw, err := dialer.DialContext(dialCtx, "udp", e.server)
		if err != nil {
			return nil, err
		}
		conn, err := qtls.Dial(dialCtx, raw, e.tls, q)
		if err != nil {
			raw.Close()
			return nil, err
		}
		// The QUIC connection outlives its handshake context. The owning HTTP
		// transport closes it on reset, and this goroutine releases its socket.
		go func() { <-conn.Context().Done(); raw.Close() }()
		return conn, nil
	}
	return t, func() { t.Close() }
}
