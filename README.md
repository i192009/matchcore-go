# MatchCore

**Deterministic trading matching and transactional settlement in Go.**

MatchCore runs price-time priority order books for BTC-USD, ETH-USD and SOL-USD, with atomic PostgreSQL risk controls, balanced simulated settlement, durable event replay, and real-time market data. The React terminal exposes actual committed executions, reservations and matching-service state.

> MatchCore is a portfolio-scale exchange simulation. It uses internal issued balances and generated activity, with no connection to real exchanges, customer assets or payment systems.

**Local verification completed 11 November 2026:** 26 Go tests/fuzz targets (37 named test/subtest passes) with the race detector, four frontend tests, two browser tests, real PostgreSQL/Redis/NATS checks, and the trade/restart/idempotency acceptance scenario passed. See [verification evidence](docs/verification.md) and the [five captured result screens](screenshots/README.md).

## Run it

Docker Desktop/Engine with Linux containers and Compose v2.24.4+ is sufficient. Copy the public **development-only** credentials into an ignored `.env`, then build the entire stack:

```sh
git clone https://github.com/i192009/matchcore-go.git
cd matchcore-go
cp .env.example .env
docker compose up --build -d
docker compose ps
```

PowerShell:

```powershell
git clone https://github.com/i192009/matchcore-go.git
Set-Location matchcore-go
Copy-Item .env.example .env
docker compose up --build -d
docker compose ps
```

Open **http://localhost:5173**. Enter Alice's example API key from `.env`; use Bob's key in a separate browser session to trade between accounts. The local example Alice key is `local-alice-key-00000001`. These public credentials must be replaced for any shared installation. Migrations and idempotent funding run automatically: each demo trader starts with 100 simulated coins of each base asset and $1,000,000 simulated USD; the fee account starts empty.

Run the acceptance test **before** adding other BTC liquidity:

```sh
sh scripts/acceptance.sh
docker compose run --rm simulator --url http://gateway:8080 --run demo-1 --count 120
```

On Windows, use `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1` in place of the shell script. The simulator creates actual bid/ask ladders and crossing orders through the HTTP/gRPC path. Reusing its run ID safely repeats the same commands; choose a new ID for more activity. `docker compose down` stops the stack and preserves data.

Grafana: **http://localhost:3000/d/matchcore** (`admin`, local password in `.env`). Prometheus: **http://localhost:9091**. Readiness: **http://localhost:8080/readyz**. Detailed [deployment, troubleshooting and isolated-test instructions](docs/deployment.md) include native Go/Node development, all ports and an isolated acceptance project that can run beside an active demo.

## Engineering problems demonstrated

- Price-time priority, FIFO at a level, resting-price executions, limit GTC/IOC, market IOC, partial fills, cancellation and cancel-incoming self-trade prevention.
- Checked integer price ticks, lot quantities and minor currency units; explicit market-buy budgets, order/exposure limits, conservative fee reservations and per-execution fee rounding.
- Atomic SQL orders, reservations, balances, trades, settlement records, balanced ledger entries, fee transfers, events, idempotent results and outbox insertion.
- One bounded command processor per instrument, account locks across instruments, owner fencing and explicit unsafe-state readiness behavior.
- Append-only checksummed command events; periodic versioned snapshots, contiguous deterministic replay and projection consistency checks.
- Generated authenticated gRPC, HTTP/JSON read/command APIs, admin restrictions, rate limits, deadlines, ownership checks and explicit CORS/origin validation.
- WebSocket snapshot-plus-delta depth, derived BBO, executions/last price, per-command statistics and authorized private order updates; bounded slow-client buffers and gap recovery.
- Restartable JetStream outbox publishing and sequence deduplication; expiring Redis public market cache outside the financial authority.
- Functional terminal, operations and account views, real order entry/cancel controls, own executions, lifetime fee totals, balances and transaction history.
- Prometheus metrics, provisioned Grafana dashboards, correlated JSON logs, OTel HTTP/gRPC/engine spans and a local Collector.
- Executed core/property/race/real-service/recovery/security/browser checks, actual restart acceptance, Go benchmarks, profiles and k6 workload results.

## Architecture and workflow

```mermaid
flowchart LR
    Browser[React terminal] --> Gateway[HTTP / WebSocket gateway]
    Gateway -->|Protobuf / gRPC| API[Authentication + validation]
    API --> Queues[Bounded instrument queues]
    Queues --> Writers[One fenced writer per instrument]
    Writers --> Plan[Read-only matching plan]
    Plan --> DB[(PostgreSQL atomic risk + settlement + events)]
    DB --> Apply[Apply committed plan]
    Apply --> Book[Committed order books]
    DB --> Outbox[Transactional outbox]
    Outbox --> Publisher[Publisher]
    Publisher --> NATS[NATS JetStream]
    NATS --> Gateway
    Gateway --> Cache[(Redis cache)]
    DB --> Replay[Snapshot + deterministic replay]
    Replay --> Writers
    API & Gateway & Publisher --> Observe[Prometheus / Grafana / OTel]
```

1. Authentication fixes the account identity; the gateway translates typed commands to gRPC.
2. A bounded instrument queue establishes a single command order. The core computes executions without mutating the book.
3. PostgreSQL locks the instrument, verifies its owner epoch/sequence, locks financial participants in a stable order and validates initial funding/restrictions/idempotency.
4. One transaction commits all order, accounting, settlement, event, result and outbox effects. Asset deltas must sum to zero; balances remain nonnegative.
5. The writer applies that committed plan and publishes an immutable depth view. An uncertain commit or failed application makes it unsafe until recovery.
6. The publisher waits for JetStream acknowledgements before marking outbox delivery. Gateways deliver committed data; clients ignore duplicate sequences and resnapshot after gaps.

The core uses deterministic price-level treaps, linked FIFO queues, aggregate quantities and direct order-ID lookup. Ordinary insertion/empty-level deletion is expected O(log L); cancellation within a surviving level is O(1); sweeps visit relevant levels and makers. Canonical snapshot extraction is O(N+L). Durable depth publication currently materializes all levels and adds O(L log L) work per command. See [algorithm and exact numeric rules](docs/matching-algorithm.md).

PostgreSQL is the financial authority. Redis can be cleared, and NATS can be temporarily unavailable, without changing settlement correctness. A session ownership lock plus a durable epoch fences competing writers. Books can plan concurrently; shared USD still needs SQL account locks. Transport retries are safe only with the same idempotency key. External transport is at least once, and no financial settlement occurs in consumers. [Consistency model](docs/consistency-model.md), [events](docs/event-sourcing.md), [recovery](docs/recovery.md) and [ADRs](docs/adr/) explain the boundaries and trade-offs.

## API example

Quantity is in 0.001 coin lots. Price ticks are USD cents per lot, equivalent to $10 per coin. Thus 2 BTC at $50,000/BTC is `quantity=2000, price=5000`. Both sides pay 10 bps in USD, rounded up per execution. A market buy must specify a maximum spend in cents including fees.

```sh
curl http://localhost:8080/api/orders \
  -H 'Authorization: Bearer local-bob-key-0000000002' \
  -H 'Content-Type: application/json' \
  -d '{"client_id":"sell-1","instrument":"BTC-USD","side":"SELL","type":"LIMIT","tif":"GTC","price":5000,"quantity":2000}'

curl http://localhost:8080/api/orders \
  -H 'Authorization: Bearer local-alice-key-00000001' \
  -H 'Content-Type: application/json' \
  -d '{"client_id":"buy-1","instrument":"BTC-USD","side":"BUY","type":"LIMIT","tif":"GTC","price":5000,"quantity":1000}'
```

Use new client IDs for new orders; exact retries return the original outcome. Copy the seller order ID into `/api/cancel` with a unique cancellation key to release its remaining reservation. The [API guide](docs/api.md) has cancellation, PowerShell, grpcurl, error and WebSocket examples. [OpenAPI](api/openapi/openapi.json) and [Protobuf](api/proto/matchcore/v1/exchange.proto) are checked in with generated clients. gRPC responses carry the shared JSON domain document in `Result.json`; fully typed protobuf responses are a documented future improvement.

For WebSocket data, connect to `/ws`, first send `{"op":"auth","token":"YOUR_KEY"}`, then `{"op":"subscribe","instrument":"BTC-USD"}`. Start from the returned sequence-tagged snapshot, apply contiguous deltas, ignore duplicates, and resubscribe on a gap. Private order updates are always filtered to the authenticated account.

## Verification and performance

The automated acceptance command executes the full 2 BTC sell / 1 BTC buy scenario against real services, verifies the exact execution price, partial seller, filled buyer, USD/BTC movements, reservations, fee transfers, SQL trade/settlement/event records and committed WebSocket delivery, cancels the remainder, restarts the matching process, and proves an identical retry has no new financial effects. The state evidence is saved in `benchmarks/acceptance-state.json`.

```sh
go test ./...                 # unit tests; integration skips unless explicitly configured
make test-race
make test-integration        # configure the dedicated real-service test stack first
make lint
make frontend
make acceptance
make benchmark
```

The isolated test stack and environment variables are documented in [deployment](docs/deployment.md). Checks and actual outcomes, including repaired initial failures and verification limits, are recorded in [verification](docs/verification.md). CI runs formatting, staticcheck, vet, race/unit/integration tests, Buf lint, frontend type/build/tests, dependency scans, Docker Compose startup, restart acceptance and Playwright browser checks. Remote GitHub Actions execution is not claimed until this repository is pushed and CI runs there.

Selected **executed local baselines**, with 50/50 non-crossing/crossing IOC submissions:

| Measured boundary | Orders/s | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| Pure in-memory matching, 100k resting | 515,741 | 0.90 µs | 2.20 µs | 5.20 µs |
| Actor + real SQL risk/commit, 1k resting | 198 | 5.13 ms | 7.16 ms | 7.67 ms |
| HTTP + gRPC + durable commit | 191 | 4.74 ms | 8.92 ms | 12.04 ms |

These short development runs are different measurement boundaries, not interchangeable throughput claims. [Performance methodology, hardware, durations, contention results, allocation/GC/CPU data and reproduction](docs/performance.md) link the raw evidence. The matrix includes 1k/10k/100k resting orders and 90/10, 50/50 and 10/90 non-crossing/crossing mixes. Profiling identified a snapshot sort; canonical traversal removed it, with both prior and new benchmark records preserved.

## Dashboard and observability

The [screenshot gallery](screenshots/README.md) records the capture context, screen descriptions and image dimensions.

The terminal uses actual WebSocket updates. The execution chart contains committed simulated executions, not invented external prices. Operations shows queue/recovery state, commit counts, measured order-count rate, event delay and histogram latency estimates. Account shows available/reserved balances, lifetime fees, own executions and a bounded ledger/history view.

![Operations dashboard](screenshots/operations.png)

![Provisioned Grafana dashboard](screenshots/grafana.png)

Metrics cover command/order/trade counts, match attempts and latency, commit latency/failures, book depth, queues, safe state, recovery time, clients, outbox backlog, publication failures, delivery delay and Go/process resource metrics. Histogram bucket estimates in the UI are labelled; Grafana supplies rate-window quantiles. OTel Collector output and JSON trace IDs support HTTP → gRPC → engine correlation. [Account](screenshots/account.png) and [mobile](screenshots/mobile.png) screenshots are also included.

## Stack and repository

Go 1.26, generated gRPC/Protobuf, PostgreSQL 18, Redis 8, NATS 2.14 JetStream, Prometheus 3.13 LTS, Grafana 13.3, OpenTelemetry, Docker Compose, k6 2.3, React 19, TypeScript, Vite, Vitest, Playwright and GitHub Actions. Exact Go/npm dependencies are locked in `go.mod`, `go.sum` and `web/package-lock.json`. Supported versions and APIs were checked against vendor documentation; [dependency choices](docs/dependencies.md) records the sources.

```text
matchcore-go/
├── cmd/
│   ├── matchcore/        # matcher, migration, deterministic account seeding, health probe
│   ├── gateway/          # stateless HTTP / WebSocket gateway
│   ├── publisher/        # transactional outbox → JetStream
│   ├── simulator/        # reproducible real API activity
│   ├── acceptance/       # real-service before/after-restart assertions
│   └── benchmark/        # memory / durable / end-to-end measurements
├── internal/
│   ├── core/             # integer domain, treap/FIFO book, matching and property tests
│   ├── engine/           # single-writer lifecycle, bounded queues and fencing
│   ├── store/            # SQL risk, settlement, ledger, events, snapshots and outbox
│   ├── transport/        # auth, generated gRPC implementation, HTTP and WebSockets
│   └── observability/    # Prometheus and OpenTelemetry
├── api/{proto,gen,openapi}/
├── migrations/           # embedded SQL schema and constraints
├── tests/integration/    # real SQL/Redis/NATS, concurrency and recovery checks
├── web/                  # React terminal, operations/account UI and browser tests
├── deployments/          # Prometheus, Grafana provisioning and OTel Collector
├── benchmarks/           # k6 workload, raw measurements and acceptance evidence
├── scripts/              # generation, benchmark, integration and acceptance commands
├── docs/                 # architecture, consistency, recovery, security, performance, ADRs
├── .github/workflows/ci.yml
├── Dockerfile
├── docker-compose.yml
├── docker-compose.test.yml
├── docker-compose.acceptance.yml
├── Makefile
├── buf.yaml
├── go.mod / go.sum
├── .env.example / .gitignore / .dockerignore
└── LICENSE / CONTRIBUTING.md
```

## Security, limitations and next work

Authentication uses environment-scoped keys with account ownership and admin checks. Requests, command lifetimes, queues, book size, execution count and subscriber buffers are bounded. SQL values are parameterized; exact origins govern CORS/WebSocket upgrade. App containers run non-root, and host ports bind loopback. See the [security assumptions](docs/security.md) and [failure matrix](docs/failure-scenarios.md).

This is a credible, executable engineering artifact, not a production exchange or a compliance claim. The current deployment has one authoritative process, static instrument configuration, a coarse simulated price grid, no automatic election/HA, no FOK, no external asset movement and bounded history without pagination. Shared-account financial locks and full depth materialization limit scale. The OTel Collector logs traces without a persistent trace backend. Development networking is plaintext and its seeded credentials are public. TLS/mTLS, separated database roles, backups/PITR, distributed quotas, dedicated account-load profiles, typed response messages and durable trace storage are future deployment improvements.

The matching, accounting, idempotency and recovery guarantees described above have executable evidence; availability under arbitrary partitions, real-market latency and production capacity do not.

## License

MIT - see [LICENSE](LICENSE).
