package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// serveWithShutdown listens until an OS signal or unexpected HTTP failure.
// A graceful drain is bounded; on timeout Close tears down active sockets,
// cancelling request contexts and their exec.CommandContext FFmpeg children.
func serveWithShutdown(ctx context.Context, server *http.Server, listener net.Listener, timeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		err := server.Shutdown(shutdownCtx)
		if err != nil {
			_ = server.Close()
		}
		serveErr := <-errCh
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return err
	}
}
