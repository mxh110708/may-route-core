// Disposable reverse proxy for XHTTP acceptance. Loopback only, no user profile.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/sagernet/quic-go/http3"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"

	boxTLS "github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"golang.org/x/net/http2"
)

type endpoint struct {
	Port                                   int
	Host, Path, HTTPVersion, SessionCookie string
}
type configuration struct {
	Certificate, Key, CA, Backend, BackendHost, BackendPath, Token string
	BackendRealityPublicKey, BackendRealityShortID                 string
	Endpoints                                                      []endpoint
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "run" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	file := flag.String("config", "", "synthetic fixture JSON")
	flag.Parse()
	data, err := os.ReadFile(*file)
	must(err)
	var c configuration
	must(json.Unmarshal(data, &c))
	cert, err := tls.X509KeyPair([]byte(c.Certificate), []byte(c.Key))
	must(err)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(c.CA)) {
		panic("synthetic CA missing")
	}
	target, err := url.Parse("https://" + c.Backend)
	must(err)
	if host, _, err := net.SplitHostPort(c.Backend); err != nil || host != "127.0.0.1" {
		panic("backend must be loopback")
	}
	transport := &http2.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "xhttp.example.test", MinVersion: tls.VersionTLS13}}
	if c.BackendRealityPublicKey != "" {
		config, err := boxTLS.NewClient(context.Background(), logger.NOP(), "127.0.0.1", option.OutboundTLSOptions{Enabled: true, ServerName: "xhttp.example.test", ALPN: []string{"h2"}, UTLS: &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"}, Reality: &option.OutboundRealityOptions{Enabled: true, PublicKey: c.BackendRealityPublicKey, ShortID: c.BackendRealityShortID}})
		must(err)
		dialer := boxTLS.NewDialer(&loopbackDialer{}, config)
		transport.DialTLSContext = func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
			return dialer.DialTLSContext(ctx, M.ParseSocksaddr(c.Backend))
		}
	}
	var mu sync.Mutex
	counts := map[string]int{}
	failed := map[string]bool{}
	for _, endpoint := range c.Endpoints {
		e := endpoint
		proxy := &httputil.ReverseProxy{Transport: transport, FlushInterval: -1, Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			p.Out.Host = c.BackendHost
			p.Out.URL.Path = c.BackendPath + strings.TrimPrefix(p.In.URL.Path, e.Path)
			p.Out.URL.RawPath = ""
			if e.SessionCookie != "" {
				if cookie, err := p.In.Cookie(e.SessionCookie); err == nil {
					p.Out.Header.Set("X-Up-Session", cookie.Value)
				}
			}
		}}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/__stats" || r.URL.Path == "/__fail" {
				if r.Header.Get("X-Fixture-Token") != c.Token {
					w.WriteHeader(403)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path == "/__fail" {
					failed[e.Host] = r.URL.Query().Get("enabled") == "1"
				}
				json.NewEncoder(w).Encode(counts)
				return
			}
			if r.Host != e.Host || !strings.HasPrefix(r.URL.Path, e.Path) {
				w.WriteHeader(404)
				return
			}
			mu.Lock()
			counts[e.Host]++
			fail := failed[e.Host]
			mu.Unlock()
			if fail {
				w.WriteHeader(502)
				return
			}
			http.NewResponseController(w).EnableFullDuplex()
			proxy.ServeHTTP(w, r)
		})
		server := &http.Server{Handler: handler, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}, MaxHeaderBytes: 64 << 10}
		if e.HTTPVersion == "3" {
			packet, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", e.Port))
			must(err)
			cfg := server.TLSConfig.Clone()
			cfg.NextProtos = []string{http3.NextProtoH3}
			h3 := &http3.Server{Handler: handler, TLSConfig: cfg, MaxHeaderBytes: 64 << 10}
			go h3.Serve(packet)
		} else {
			must(http2.ConfigureServer(server, &http2.Server{}))
			listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", e.Port))
			must(err)
			go func() { server.ServeTLS(listener, "", "") }()
		}
	}
	select {}
}

type loopbackDialer struct{ net.Dialer }

func (d *loopbackDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if destination.AddrString() != "127.0.0.1" {
		return nil, fmt.Errorf("loopback required")
	}
	return d.Dialer.DialContext(ctx, network, destination.String())
}
func (d *loopbackDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, fmt.Errorf("TCP fixture only")
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
