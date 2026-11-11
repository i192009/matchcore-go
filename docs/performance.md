# Measured performance and reproduction

These are executed development measurements, not capacity promises. Pure matching, SQL durability and API latency have separate boundaries. Raw output is committed in [benchmarks/results](../benchmarks/results/). Baselines were collected on 11 November 2026; compare on the same machine and an idle system before drawing optimization conclusions.

## Environment and method

Host: Windows on a Snapdragon X X126100 / Qualcomm Oryon, 8 cores/logical CPUs, approximately 15.6 GiB physical RAM. Execution: Docker Desktop Linux/ARM64, WSL2 kernel `6.18.58.1-microsoft-standard-WSL2`, Go `go1.26.11`, 8 visible CPUs. PostgreSQL 18 uses Docker's persistent local volume with its default synchronous commit/WAL settings. Monitoring, image builds and other checks shared the machine during portions of this run. These are short samples with noise, not isolated bare-metal measurements or statistical confidence intervals.

`cmd/benchmark` creates the configured resting fixture before measurement, forces GC, records one client latency per order, and reports exact sample p50/p95/p99, orders/s, executions/s, runtime allocation/byte totals, ending heap, GC cycles/pause and failures. Pure mode processes sequentially on one book. Durable mode calls the actual actor/risk/SQL transaction and includes periodic snapshots. End-to-end mode uses HTTP → authenticated gateway → gRPC → actor → SQL → response. API fixture creation includes normal authentication/durability and is excluded from measured order latency.

The fixture has one-lot sell orders at ten price ticks. Measured commands are one-lot buy IOC orders, priced at ten ticks when crossing and one tick otherwise. Configured crossing/non-crossing percentages are recorded. Ensure fixture inventory covers measured crossing orders. Core microbenchmarks instead distribute larger resting orders across 1,000 price levels and reset affected state outside timing. The `Replay` microbenchmark applies one decoded plan; `BenchmarkEventReplay` separately includes JSON decoding and applying 1k/10k/100k event batches.

CPU counters use Linux `/proc/self/stat` at `CLK_TCK` resolution (normally 10 ms), normalized so 100% is one core. Short runs can have coarse CPU measurements. E2E allocation/GC/CPU fields describe the **client process**, not all backend services. Durable fields describe the engine/client process but exclude the separate PostgreSQL process. A point-in-time Docker resource capture and Prometheus/Grafana resource metrics supplement these; no whole-exchange CPU utilization is inferred from client counters.

## Executed results

| Boundary / workload | Orders | Executions | Orders/s | Executions/s | p50 | p95 | p99 | Failures |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| In-memory, 100k resting, 50/50 IOC mix, one writer | 100,000 | 50,000 | 515,741 | 257,871 | 0.90 µs | 2.20 µs | 5.20 µs | 0 |
| Durable, 1k resting, 50/50 mix, one client | 1,000 | 500 | 198.0 | 99.0 | 5.13 ms | 7.16 ms | 7.67 ms | 0 |
| Durable, 3 instruments, shared account, 8 clients | 1,000 | 500 | 187.0 | 93.5 | 30.74 ms | 124.50 ms | 264.69 ms | 0 |
| HTTP + gRPC + durable commit, one client | 200 | 100 | 191.4 | 95.7 | 4.74 ms | 8.92 ms | 12.04 ms | 0 |

The rows ran for 0.194 s, 5.050 s, 5.347 s and 1.045 s respectively. The multi-instrument run followed the single-instrument run in the same dedicated benchmark database, so it includes the earlier book/history plus 1,000 new fixture orders per instrument. It is a **contention example**, not a controlled clean-dataset scaling comparison. It deliberately uses one shared buyer and seller, so account locks constrain parallelism. The E2E result is a small local smoke baseline; increase sample duration and use independent funded accounts before a capacity study.

| Boundary | Allocs/order | Allocated bytes/order | End heap | GC cycles | GC total pause |
|---|---:|---:|---:|---:|---:|
| In-memory sample | 9.00 | 844.9 | 38,287,592 B | 2 | 0.420 ms |
| Durable single | 263.72 | 47,757.7 | 6,699,576 B | 8 | 1.674 ms |
| Durable contention | 265.18 | 51,902.1 | 15,340,528 B | 6 | 2.574 ms |
| E2E client | 164.85 | 14,642.0 | 980,584 B | 1 | 0.215 ms |

The in-memory process CPU sample was 0.24 CPU seconds (123.8% of a core including runtime/GC activity). Durable contention recorded 2.36 CPU seconds (44.1%); E2E client recorded 0.07 (6.7%). The older durable-single raw record predates the CPU-counter addition; no CPU value is invented for it. See [raw sustained matching](../benchmarks/results/memory-sustained.json), [durable](../benchmarks/results/durable-single.json), [contention](../benchmarks/results/durable-multi.json), [E2E](../benchmarks/results/e2e.json) and [resource sample](../benchmarks/results/container-resources.jsonl).

The full 3×3 in-memory matrix covers 1k/10k/100k resting orders and 10/50/90% crossing mixes with 1,000 measured IOC submissions each. Those much shorter samples are retained as raw JSON. Microbenchmarks include insertion, matching, partial fill, cancellation, multilevel sweep, snapshot extraction/encoding and plan replay, with allocations in [go-bench.txt](../benchmarks/results/go-bench.txt).

## Measurement-driven change

[CPU profile output](../benchmarks/results/cpu-profile-top.txt) identified snapshot `Orders`, sorting and struct-copy/JSON encoding work. Snapshot extraction now walks the treap/FIFO structure canonically in O(N+L), replacing a global O(N log N) struct sort. The earlier 100k snapshot sample took 124.71 ms/op and 25 allocations; the separately executed optimized sample took 64.07 ms/op and 18 allocations ([optimized raw output](../benchmarks/results/snapshot-optimized.txt)). Shared-system timing means the exact difference is not a universal speedup guarantee. The asymptotic removal of the sort is independent of timing noise. JSON snapshot encoding and publishing full immutable depth views remain known cost centers.

## Reproduce

```sh
make benchmark
go run ./cmd/benchmark --mode memory --n 100000 --resting 100000 --crossing 50
go test ./internal/core -run '^$' -bench BenchmarkBook -benchmem -benchtime=100x
go test ./internal/core -run '^$' -bench BenchmarkEventReplay -benchmem -benchtime=1x
go test ./internal/core -run '^$' -bench BenchmarkBook -benchtime=100x -cpuprofile=cpu.prof -memprofile=mem.prof
go tool pprof -top cpu.prof
```

Use a fresh **dedicated PostgreSQL database** for durable runs, with no other matching process owning its instruments:

```sh
# DATABASE_URL must name that dedicated database; this does not reset any data.
go run ./cmd/matchcore migrate
go run ./cmd/matchcore seed
go run ./cmd/benchmark --mode durable --n 1000 --resting 1000 --crossing 50 --run durable-single
go run ./cmd/benchmark --mode durable --scenario contention --concurrency 8 --n 1000 --resting 1000 --crossing 50 --run durable-multi
```

For E2E use a fresh seeded demo and the credentials for Alice and Bob, not the maker accounts. Fixture/order history persists; use a new run ID for new work. Avoid running acceptance after adding resting benchmark liquidity.

```sh
export SIM_BUY_KEY='YOUR_ALICE_KEY' SIM_SELL_KEY='YOUR_BOB_KEY'
go run ./cmd/benchmark --mode e2e --url http://localhost:8080 --n 200 --resting 1000 --crossing 50 --run api-baseline

# Docker network name is matchcore_default for the default Compose project.
docker run --rm --network matchcore_default -v "$PWD/benchmarks:/scripts:ro" \
  -e API_KEY="$SIM_BUY_KEY" -e RATE=20 -e DURATION=10s -e RUN_ID=load-1 \
  grafana/k6:2.3.0 --quiet run /scripts/load.js > benchmarks/results/k6.json
```

k6 reports p50/95/99, errors and commit checks. The default load is bounded below the configured rate limit; the threshold is a test expectation, not a throughput guarantee. Higher load should intentionally observe 429 backpressure rather than unbounded queues. [k6 output](../benchmarks/results/k6.json) contains the executed result. All published metrics come from these real commands; there is no claim of production exchange throughput.
