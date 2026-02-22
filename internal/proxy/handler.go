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

// relayBufSize is the buffer size used for bidirectional relay.
// 32KB matches Go's default io.Copy buffer size.
const relayBufSize = 32 * 1024

// relayBufPool reuses buffers across relay goroutines to reduce GC pressure.
// Without this, io.Copy allocates a fresh 32KB buffer per direction per session.
// At 10K concurrent sessions, that's ~640MB of transient allocations.
var relayBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, relayBufSize)
		return &buf
	},
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
		bufp := relayBufPool.Get().(*[]byte)
		defer relayBufPool.Put(bufp)
		_, err := io.CopyBuffer(backend, client, *bufp)
		errCh <- err
		// Signal the backend that the client is done writing
		if tc, ok := backend.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	// Backend → Client
	go func() {
		defer wg.Done()
		bufp := relayBufPool.Get().(*[]byte)
		defer relayBufPool.Put(bufp)
		_, err := io.CopyBuffer(client, backend, *bufp)
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
