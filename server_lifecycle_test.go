package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestGracefulShutdownWaitsForActiveHandler(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})}
	done := make(chan error, 1)
	go func() { done <- serveWithShutdown(ctx, server, listener, time.Second) }()
	client := &http.Client{Timeout: 3 * time.Second}
	requestDone := make(chan error, 1)
	go func() {
		resp, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("test HTTP request never entered handler")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("server exited before active handler drained: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful shutdown failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server shutdown did not complete after active request finished")
	}
	if err := <-requestDone; err != nil {
		t.Fatalf("request failed: %v", err)
	}
}

func TestShutdownClosesListenerAfterCancelledContext(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := &http.Server{Handler: http.NewServeMux()}
	if err := serveWithShutdown(ctx, server, listener, time.Second); err != nil {
		t.Fatalf("shutdown returned %v", err)
	}
}
