package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestShutdownServeBoundsStalledCleanup(t *testing.T) {
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- shutdownServe(ctx, &http.Server{}, func(context.Context) error { return nil }, func() {
			close(started)
			<-release
			close(finished)
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited for stalled cleanup")
	}
	select {
	case <-finished:
		t.Fatal("cleanup unexpectedly finished")
	default:
	}
}

func TestShutdownServeDefersCleanupUntilSessionsFinish(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	cleaned := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- shutdownServe(ctx, &http.Server{}, func(context.Context) error {
			close(started)
			<-release
			return nil
		}, func() { close(cleaned) })
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown = %v", err)
	}
	select {
	case <-cleaned:
		t.Fatal("shared services closed before sessions")
	default:
	}
	close(release)
	select {
	case <-cleaned:
	case <-time.After(5 * time.Second):
		t.Fatal("eventual cleanup did not finish")
	}
}

func TestShutdownServeCooperative(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cleaned := false
	if err := shutdownServe(ctx, &http.Server{}, func(context.Context) error { return nil }, func() { cleaned = true }); err != nil {
		t.Fatal(err)
	}
	if !cleaned {
		t.Fatal("cleanup not awaited")
	}
}

func TestShutdownServeDeadlineForceClosesHTTP(t *testing.T) {
	entered, disconnected := make(chan struct{}), make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(disconnected)
	}))
	defer hs.Close()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := http.Get(hs.URL)
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP request did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := shutdownServe(ctx, hs.Config, func(context.Context) error { return nil }, func() {}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown = %v", err)
	}
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("deadline did not force-close active HTTP connection")
	}
	select {
	case <-clientDone:
	case <-time.After(5 * time.Second):
		t.Fatal("client still connected")
	}
}
