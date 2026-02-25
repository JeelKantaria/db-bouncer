# DBBouncer Benchmark Baseline

**Date:** 2026-02-25
**Machine:** Intel Core i7-8750H @ 2.20GHz, Windows 11, 8GB RAM
**Go version:** (run `go version` to check)
**Branch:** feature/benchmarks

---

## How to Run

```bash
# Pool benchmarks
go test ./internal/pool/ -run "^$" -bench "." -benchtime=2s -benchmem

# Proxy relay benchmarks (PG relay uses Nx format due to net.Pipe calibration quirk)
go test ./internal/proxy/ -run "^$" -bench "BenchmarkMySQLRelayTransactionMode|BenchmarkMySQLStatusFlagsParsing" -benchtime=2s -benchmem
go test ./internal/proxy/ -run "^$" -bench "BenchmarkPGRelayTransactionMode" -benchtime=10000x -benchmem
```

---

## Results

### Pool (`internal/pool`)

| Benchmark | ops | ns/op | B/op | allocs/op |
|-----------|-----|-------|------|-----------|
| `BenchmarkAcquireReturn` | 19,157,576 | **131.6 ns** | 0 | 0 |
| `BenchmarkAcquireReturnParallel` | 10,050,254 | **238.7 ns** | 0 | 0 |
| `BenchmarkAcquireContended` (4 conns, 16+ goroutines) | 14,674 | **163,973 ns** | 128 | 2 |
| `BenchmarkPoolStats` | 100,000,000 | **20.1 ns** | 0 | 0 |
| `BenchmarkConcurrentAcquireReturnThroughput` (8 conns, 32 goroutines) | 8,625,962 | **274.7 ns** | 4 | 0 |

**Key insights:**
- Single-goroutine acquire/return: **131 ns** — mutex + slice append/pop
- Parallel (12 conns, GOMAXPROCS goroutines): **238 ns** — minimal contention
- Contended (4 conns, many goroutines): **164 µs** — includes `time.Sleep(1µs)` work simulation + cond.Wait overhead
- Stats read: **20 ns** — read-only mutex

---

### Proxy Relay (`internal/proxy`)

| Benchmark | ops | ns/op | B/op | allocs/op |
|-----------|-----|-------|------|-----------|
| `BenchmarkMySQLRelayTransactionMode` | 113,130 | **22,521 ns** (~44k req/s) | 179 | 18 |
| `BenchmarkPGRelayTransactionMode` | 58,318 | **40,854 ns** (~24k req/s) | 297 | 39 |
| `BenchmarkMySQLStatusFlagsParsing` | 857,092,656 | **2.66 ns** | 0 | 0 |

**Key insights:**
- MySQL transaction relay: **22 µs/transaction** — includes packet parse, status flag check, RESET CONNECTION round-trip
- PG transaction relay: **41 µs/transaction** — includes DISCARD ALL round-trip (more protocol overhead than MySQL RESET CONNECTION)
- PG is ~2x slower than MySQL in transaction mode due to the more verbose PG wire protocol (CommandComplete + RFQ vs single OK packet)
- Status flag parsing: **2.66 ns** — negligible overhead per packet
- All relay benchmarks use `net.Pipe` (in-process, no real network); real-world latency will be dominated by network RTT

---

## Notes

- Session-mode relay (`relay()` / blind `io.Copy`) benchmarks are excluded because `net.Pipe`'s synchronous semantics are incompatible with Go's benchmark calibration passes
- Pool `BenchmarkAcquireContended` includes a 1µs `time.Sleep` to simulate real work; remove it for pure lock contention measurement
- PG relay benchmark uses `-benchtime=Nx` format (not time-based) due to the same `net.Pipe` calibration issue
