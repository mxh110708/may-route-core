package v2rayxhttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	W "github.com/sagernet/sing-box/common/xhttpwire"
)

// Isolate upload construction from TLS and server RTT. The receiver still reads
// every asynchronous body before acknowledging it; no caller buffer is retained.
func BenchmarkPacketUpload(b *testing.B) {
	w, err := W.ParseExtra(json.RawMessage(`{"scMaxEachPostBytes":1000000,"scMinPostsIntervalMs":1}`))
	if err != nil {
		b.Fatal(err)
	}
	w.Host, w.Path = "front.example.test", "/upload/"
	for _, size := range []struct {
		name  string
		bytes int
	}{{"small-4KiB", 4 << 10}, {"upload-1MiB", 1 << 20}, {"upload-8MiB", 8 << 20}} {
		b.Run(size.name, func(b *testing.B) {
			chunk := []byte(strings.Repeat("x", 32<<10))
			var received atomic.Int64
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				n, err := io.Copy(io.Discard, r.Body)
				r.Body.Close()
				received.Add(n)
				return &http.Response{StatusCode: 200, Body: http.NoBody}, err
			})
			b.ReportAllocs()
			b.SetBytes(int64(size.bytes))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				p := newPacketWriter(ctx, cancel, w, "benchmark-session", transport)
				// Scheduling and protocol intervals are covered by runtime tests;
				// this benchmark measures allocation/copying, without simulated RTT.
				p.interval = 0
				for left := size.bytes; left > 0; {
					n := min(left, len(chunk))
					if _, err := p.Write(chunk[:n]); err != nil {
						b.Fatal(err)
					}
					left -= n
				}
				if err := p.closeWrite(); err != nil {
					b.Fatal(err)
				}
				p.Close()
			}
			b.StopTimer()
			if received.Load() != int64(b.N)*int64(size.bytes) {
				b.Fatal("upload bytes changed")
			}
		})
	}
}
