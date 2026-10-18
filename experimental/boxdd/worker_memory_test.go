package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
)

func awaitWorkerCollection(t *testing.T, calls *atomic.Int32, expected int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() == expected {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("collection calls: got %d, want %d", calls.Load(), expected)
}

func TestWorkerMemoryReclaimsOnceAfterWork(t *testing.T) {
	var calls atomic.Int32
	r := newWorkerMemoryReclaimer(10*time.Millisecond, func() { calls.Add(1) })
	defer r.close()
	finish := r.begin(true)
	time.Sleep(25 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("collected while a request was active")
	}
	finish()
	finish()
	awaitWorkerCollection(t, &calls, 1)
	time.Sleep(25 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatal("collection must not be periodic")
	}
	r.begin(false)()
	time.Sleep(25 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatal("small control RPC must not schedule collection")
	}
}

func TestWorkerMemoryDefersAcrossAnotherActiveRequest(t *testing.T) {
	var calls atomic.Int32
	r := newWorkerMemoryReclaimer(15*time.Millisecond, func() { calls.Add(1) })
	defer r.close()
	r.begin(true)()
	finish := r.begin(false)
	time.Sleep(35 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("new request must cancel the idle timer")
	}
	finish()
	awaitWorkerCollection(t, &calls, 1)
}

func TestWorkerMemoryClosesWithoutPendingCollection(t *testing.T) {
	var calls atomic.Int32
	r := newWorkerMemoryReclaimer(15*time.Millisecond, func() { calls.Add(1) })
	r.begin(true)()
	r.close()
	time.Sleep(35 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("closed worker must not run a collection timer")
	}
}

func TestWorkerMemoryInterceptorsPreserveRequestsAndProtectStreams(t *testing.T) {
	var calls atomic.Int32
	r := newWorkerMemoryReclaimer(10*time.Millisecond, func() { calls.Add(1) })
	defer r.close()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	request := new(int)
	expectedError := errors.New("configuration rejected")
	go func() {
		done <- r.stream(nil, nil, nil, func(any, grpc.ServerStream) error {
			close(entered)
			<-release
			return expectedError
		})
	}()
	<-entered
	response, err := r.unary(context.Background(), request,
		&grpc.UnaryServerInfo{FullMethod: ApplicationService_CheckConfig_FullMethodName},
		func(_ context.Context, received any) (any, error) {
			if received != request {
				t.Fatal("interceptor changed the request")
			}
			return request, expectedError
		})
	if response != request || err != expectedError {
		t.Fatal("interceptor changed the handler result")
	}
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("collection ran during an active application stream")
	}
	close(release)
	if <-done != expectedError {
		t.Fatal("stream interceptor changed the handler result")
	}
	awaitWorkerCollection(t, &calls, 1)
}
