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
// It returns when either side closes or an error occurs.
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

	// Wait for context cancellation or one side to finish
	select {
	case <-ctx.Done():
		client.Close()
		backend.Close()
	case err := <-errCh:
		if err != nil && err != io.EOF {
			return err
		}
	}

	wg.Wait()
	return nil
}
