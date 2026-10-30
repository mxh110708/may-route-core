//go:build !with_quic

package v2rayxhttp

import (
	"context"
	C "github.com/sagernet/sing-box/constant"
	N "github.com/sagernet/sing/common/network"
	"net/http"
	"time"
)

const quicAvailable = false

func newHTTP3(context.Context, N.Dialer, *endpoint, time.Duration) (http.RoundTripper, func()) {
	return errorTransport{C.ErrQUICNotIncluded}, func() {}
}
