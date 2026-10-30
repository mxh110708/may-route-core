package vless

import (
	"context"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
)

func TestXHTTPRequiresAuthenticatedBoundedProfile(t *testing.T) {
	for _, change := range []struct {
		name, expected string
		edit           func(*option.VLESSOutboundOptions)
	}{
		{"vision", "empty VLESS flow", func(o *option.VLESSOutboundOptions) { o.Flow = "xtls-rprx-vision" }},
		{"mux", "no additional multiplex", func(o *option.VLESSOutboundOptions) { o.Multiplex = &option.OutboundMultiplexOptions{Enabled: true} }},
		{"no-tls", "requires verified TLS", func(o *option.VLESSOutboundOptions) { o.TLS = nil }},
		{"insecure", "requires verified TLS", func(o *option.VLESSOutboundOptions) { o.TLS.Insecure = true }},
		{"no-sni", "requires verified TLS", func(o *option.VLESSOutboundOptions) { o.TLS.DisableSNI = true }},
		{"old-tls", "requires TLS 1.2", func(o *option.VLESSOutboundOptions) { o.TLS.MaxVersion = "1.1" }},
		{"ktls", "does not support kTLS", func(o *option.VLESSOutboundOptions) { o.TLS.KernelTx = true }},
	} {
		t.Run(change.name, func(t *testing.T) {
			o := option.VLESSOutboundOptions{Transport: &option.V2RayTransportOptions{Type: "xhttp"}, OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{TLS: &option.OutboundTLSOptions{Enabled: true}}}
			change.edit(&o)
			_, err := NewOutbound(context.Background(), nil, logger.NOP(), "test", o)
			if err == nil || !strings.Contains(err.Error(), change.expected) {
				t.Fatal("invalid XHTTP profile reached generic dialer initialization", err)
			}
		})
	}
}
