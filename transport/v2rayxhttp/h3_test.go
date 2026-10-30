//go:build with_quic

package v2rayxhttp

import (
	"context"
	stdtls "crypto/tls"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
)

func TestHTTP3DuplexResetAndDialContextRelease(t *testing.T) {
	certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
	defer certificateServer.Close()
	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateServer.Certificate().Raw}))
	cfg := certificateServer.TLS.Clone()
	cfg.NextProtos = []string{http3.NextProtoH3}
	cfg.MinVersion = stdtls.VersionTLS13
	server := &http3.Server{TLSConfig: cfg, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 3 {
			t.Error("HTTP version changed")
			return
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		data := make([]byte, 4096)
		for {
			n, err := r.Body.Read(data)
			if n > 0 {
				w.Write(data[:n])
				w.(http.Flusher).Flush()
			}
			if err != nil {
				return
			}
		}
	})}
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(packet)
	defer server.Close()
	tlsConfig, err := tls.NewClient(context.Background(), logger.NOP(), "127.0.0.1", option.OutboundTLSOptions{Enabled: true, ServerName: "example.com", Certificate: []string{certificate}, ALPN: []string{"h3"}})
	if err != nil {
		t.Fatal(err)
	}
	root, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()
	transport, err := NewClient(root, &testDialer{}, M.ParseSocksaddr(packet.LocalAddr().String()), option.V2RayXHTTPOptions{Mode: "stream-one", HTTPVersion: "3", Path: "/"}, tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := transport.DialContext(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := io.WriteString(conn, "http3-duplex"); done <- err }()
		data := make([]byte, 12)
		if _, err = io.ReadFull(conn, data); err != nil || string(data) != "http3-duplex" {
			t.Fatal("dial-context release interrupted H3", err)
		}
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		transport.Close()
		read := make(chan struct{})
		go func() { conn.Read(make([]byte, 1)); close(read) }()
		select {
		case <-read:
		case <-time.After(time.Second):
			t.Fatal("H3 reset retained a blocked read")
		}
		conn.Close()
	}
	cancelRoot()
	if conn, err := transport.DialContext(context.Background()); err == nil {
		conn.Close()
		t.Fatal("root cancellation accepted H3 dial")
	}
}
