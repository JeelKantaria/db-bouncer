package proxy

import (
	"context"
	"io"
	"net"
	"sync"
)

// ConnectionHandler handles a client connection for a specific DB protocol.
type ConnectionHandler interface {
	Handle(ctx context.Context, clientConn net.Conn) error
}

// relay copies data bidirectionally between client and backend connections.
// It returns when either side closes, an error occurs, or the context is cancelled.
// Both connections are closed on exit to ensure neither goroutine leaks.
func relay(ctx context.Context, client, backend net.Conn) error {
	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	wg.Add(2)

	// Client → Backend
	go func() {
		defer wg.Done()
		_, err := io.Copy(backend, client)
		errCh <- err
		// Signal the backend that the client is done writing
		if tc, ok := backend.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	// Backend → Client
	go func() {
		defer wg.Done()
		_, err := io.Copy(client, backend)
		errCh <- err
		// Signal the client that the backend is done writing
		if tc, ok := client.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	var firstErr error

	// Wait for context cancellation or one side to finish
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && err != io.EOF {
			firstErr = err
		}
	}

	// Always close both connections to unblock the other goroutine,
	// then wait for both to exit. This prevents goroutine leaks.
	client.Close()
	backend.Close()
	wg.Wait()

	return firstErr
}
