package v2rayxhttp

import (
	"context"
	"encoding/json"
	W "github.com/sagernet/sing-box/common/xhttpwire"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPacketBackpressureCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := W.ParseExtra(json.RawMessage(`{"scMaxEachPostBytes":512,"scMinPostsIntervalMs":1,"scMaxBufferedPosts":2}`))
	if err != nil {
		t.Fatal(err)
	}
	w.Host = "front.example.test"
	w.Path = "/"
	var active, peak atomic.Int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	p := newPacketWriter(ctx, cancel, w, W.NewSession(), transport)
	finished := make(chan struct{})
	go func() { p.Write([]byte(strings.Repeat("payload", 8192))); close(finished) }()
	until := time.Now().Add(time.Second)
	for active.Load() < 2 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if active.Load() != 2 {
		t.Fatal("bounded upload did not apply two-request concurrency")
	}
	p.Close()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancel left blocked write")
	}
	p.workers.Wait()
	if peak.Load() > 2 || active.Load() != 0 {
		t.Fatal("upload escaped bound or retained worker")
	}
}
