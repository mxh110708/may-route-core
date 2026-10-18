package main

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
)

// Reclaim the unused heap once after bursty configuration/schema work. This
// belongs to the application worker, never to the running proxy service.
type workerMemoryReclaimer struct {
	access     sync.Mutex
	active     int
	dirty      bool
	closed     bool
	generation uint64
	timer      *time.Timer
	delay      time.Duration
	collect    func()
}

func newWorkerMemoryReclaimer(delay time.Duration, collect func()) *workerMemoryReclaimer {
	return &workerMemoryReclaimer{delay: delay, collect: collect}
}

func (r *workerMemoryReclaimer) begin(memoryIntensive bool) func() {
	r.access.Lock()
	r.active++
	r.dirty = r.dirty || memoryIntensive
	r.generation++
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.access.Unlock()
	return sync.OnceFunc(func() {
		r.access.Lock()
		defer r.access.Unlock()
		r.active--
		if r.closed || r.active != 0 || !r.dirty {
			return
		}
		generation := r.generation
		r.timer = time.AfterFunc(r.delay, func() {
			r.access.Lock()
			if r.closed || r.active != 0 || generation != r.generation {
				r.access.Unlock()
				return
			}
			r.dirty = false
			r.timer = nil
			r.access.Unlock()
			// Do not hold the request lock while the runtime collects. A new
			// RPC can proceed; this is ordinary runtime GC, not a service pause.
			r.collect()
		})
	})
}

func (r *workerMemoryReclaimer) close() {
	r.access.Lock()
	defer r.access.Unlock()
	r.closed = true
	r.generation++
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
}

func (r *workerMemoryReclaimer) unary(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	intensive := false
	switch info.FullMethod {
	case ApplicationService_CheckConfig_FullMethodName, ApplicationService_FormatConfig_FullMethodName,
		ApplicationService_GenerateConfigSchema_FullMethodName, ApplicationService_EncodeProfile_FullMethodName,
		ApplicationService_DecodeProfile_FullMethodName:
		intensive = true
	}
	finish := r.begin(intensive)
	defer finish()
	return handler(ctx, request)
}

func (r *workerMemoryReclaimer) stream(server any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	finish := r.begin(false)
	defer finish()
	return handler(server, stream)
}
