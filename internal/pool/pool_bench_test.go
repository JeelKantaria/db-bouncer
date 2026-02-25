package pool

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/config"
)

// newBenchPool creates a TenantPool pre-loaded with n injected net.Pipe
// connections and a large AcquireTimeout so waits don't skew results.
func newBenchPool(b *testing.B, n int) (*TenantPool, []net.Conn) {
	b.Helper()
	tc := config.TenantConfig{
		DBType:   "postgres",
		Host:     "localhost",
		Port:     15432,
		DBName:   "bench",
		Username: "user",
	}
	defaults := config.PoolDefaults{
		MinConnections: 0,
		MaxConnections: n,
		IdleTimeout:    5 * time.Minute,
		MaxLifetime:    30 * time.Minute,
		AcquireTimeout: 30 * time.Second,
	}
	tp := NewTenantPool("bench", tc, defaults)

	pipes := make([]net.Conn, 0, n*2)
	for i := 0; i < n; i++ {
		client, server := net.Pipe()
		pipes = append(pipes, client, server)
		pc := NewPooledConn(client, "bench", "postgres", tp)
		// Mark authenticated so Acquire skips the 100ms Ping() health check.
		pc.SetAuthenticated(map[string]string{"server_version": "15.0"}, 1, 2)
		tp.mu.Lock()
		tp.idle = append(tp.idle, pc)
		tp.total++
		tp.mu.Unlock()
	}
	return tp, pipes
}

// BenchmarkAcquireReturn measures the throughput of a single goroutine
// repeatedly acquiring and immediately returning a connection.
// Pool size = 1 so no contention; measures pure acquire/return overhead.
func BenchmarkAcquireReturn(b *testing.B) {
	tp, pipes := newBenchPool(b, 1)
	defer tp.Close()
	defer func() {
		for _, p := range pipes {
			p.Close()
		}
	}()

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pc, err := tp.Acquire(ctx)
		if err != nil {
			b.Fatalf("Acquire failed: %v", err)
		}
		tp.Return(pc)
	}
}

// BenchmarkAcquireReturnParallel measures throughput under concurrent access
// with a pool sized to allow all goroutines to acquire simultaneously.
func BenchmarkAcquireReturnParallel(b *testing.B) {
	// Size pool to GOMAXPROCS so goroutines rarely wait
	tp, pipes := newBenchPool(b, 12)
	defer tp.Close()
	defer func() {
		for _, p := range pipes {
			p.Close()
		}
	}()

	ctx := context.Background()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			pc, err := tp.Acquire(ctx)
			if err != nil {
				continue
			}
			tp.Return(pc)
		}
	})
}

// BenchmarkAcquireContended measures latency when goroutines compete for
// fewer connections than goroutines (realistic production scenario).
func BenchmarkAcquireContended(b *testing.B) {
	const poolSize = 4
	tp, pipes := newBenchPool(b, poolSize)
	defer tp.Close()
	defer func() {
		for _, p := range pipes {
			p.Close()
		}
	}()

	ctx := context.Background()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			pc, err := tp.Acquire(ctx)
			if err != nil {
				continue
			}
			// 1µs simulated work to ensure genuine contention at poolSize=4
			time.Sleep(time.Microsecond)
			tp.Return(pc)
		}
	})
}

// BenchmarkPoolStats measures the overhead of reading pool stats
// (called every 5s by the Prometheus metrics loop in production).
func BenchmarkPoolStats(b *testing.B) {
	tp, pipes := newBenchPool(b, 4)
	defer tp.Close()
	defer func() {
		for _, p := range pipes {
			p.Close()
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tp.Stats()
	}
}

// BenchmarkConcurrentAcquireReturnThroughput measures aggregate ops/sec with a
// realistic worker-pool pattern: N workers each acquire → work → return.
func BenchmarkConcurrentAcquireReturnThroughput(b *testing.B) {
	const poolSize = 8
	tp, pipes := newBenchPool(b, poolSize)
	defer tp.Close()
	defer func() {
		for _, p := range pipes {
			p.Close()
		}
	}()

	ctx := context.Background()
	const workers = 32
	work := make(chan struct{}, b.N)
	for i := 0; i < b.N; i++ {
		work <- struct{}{}
	}
	close(work)

	b.ResetTimer()
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range work {
				pc, err := tp.Acquire(ctx)
				if err != nil {
					continue
				}
				tp.Return(pc)
			}
		}()
	}
	wg.Wait()
}
