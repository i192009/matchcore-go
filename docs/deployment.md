# Local deployment

Requirements: Docker Engine/Desktop with Linux containers, Compose v2.24.4 or newer, approximately 4 GiB free RAM for the full demo, and free loopback ports 5173, 8080, 8081, 9090, 9091, 3000, 15432, 16379, 14222 and 18222. Docker alone can build/run the application. Go 1.26+, Node 24 and GNU Make are needed only for native development commands. Image/dependency locks are recorded in the source and lockfiles; vendor patch tags are verified at build time.

```sh
cp .env.example .env
docker compose up --build -d
docker compose ps
```

PowerShell:

```powershell
Copy-Item .env.example .env
docker compose up --build -d
docker compose ps
```

Migrations and deterministic seeding are one-shot services. MatchCore waits for successful migration/seed, and the gateway waits for recovered matching state, Redis and NATS. Seeded trader accounts `alice`, `bob`, `maker-buy` and `maker-sell` each receive 100 simulated coins of BTC/ETH/SOL plus $1,000,000 simulated USD. `fees` starts empty. Repeated startup/seed is idempotent and does not top up accounts. PostgreSQL 18 uses its current `/var/lib/postgresql` volume layout.

Open http://localhost:5173 and enter Alice's local example key from `.env`. Open a second private window with Bob's key to trade between accounts. The terminal supports limit/market orders, buy budgets, cancellation, balances, depth and executions. Operations shows real process/SQL measurements; Account shows available/reserved balances, execution fees, own trades and ledger history. No data is sourced from a real market.

Before running market activity, execute the isolated, empty-book acceptance scenario:

```sh
sh scripts/acceptance.sh
docker compose run --rm simulator --url http://gateway:8080 --run demo-1 --count 120
```

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1
docker compose run --rm simulator --url http://gateway:8080 --run demo-1 --count 120
```

The simulator creates deterministic bid/ask ladders and crossing activity for all three instruments. Reusing `--run demo-1` replays the same idempotent requests; use a new run ID to create fresh activity. It stops on an unsuccessful response instead of printing fake progress. The acceptance script saves `benchmarks/acceptance-state.json`, restarts the real matcher, waits for gateway readiness and verifies recovery/retry invariants. It needs an empty BTC book so other simulated liquidity cannot alter the specified maker priority.

To run acceptance beside an active demo, use a distinct Compose project and the included port-free override. Set `COMPOSE_PROJECT_NAME=matchcore-acceptance` and `COMPOSE_FILE=docker-compose.yml:docker-compose.acceptance.yml` on Unix (semicolon delimiter on Windows), start postgres/redis/nats/matchcore/gateway/publisher with `docker compose up -d --wait`, then run the same acceptance script. This creates separate persistent volumes, service DNS and ownership locks. Stop that isolated project with its same environment settings.

Grafana: http://localhost:3000/d/matchcore, login `admin` and the **local-only** password in `.env`. Prometheus: http://localhost:9091. Matching metrics: http://localhost:8081/metrics; gateway metrics: http://localhost:8080/metrics. OTel spans are exported to the included Collector's debug pipeline (`docker compose logs otel`); persistent trace storage is not included. Structured matching/API logs contain correlated trace IDs.

| Endpoint | Purpose |
|---|---|
| 5173 | React terminal through non-root Nginx |
| 8080 | HTTP / WebSocket gateway |
| 9090 | Authoritative gRPC |
| 8081 | Matching health, metrics and admin restriction API |
| 9091 / 3000 | Prometheus / Grafana |
| 15432 / 16379 / 14222 | Local PostgreSQL / Redis / NATS access |
| 18222 | Local NATS monitoring |

Shut down with `docker compose down`; volumes persist. Do not delete volumes to fix a recovery error. Graceful termination finishes or rolls back in-flight work, saves safe snapshots and leaves unpublished events in SQL. `docker compose logs --tail 100 matchcore gateway publisher` diagnoses startup errors. Changing a published port requires matching browser origin configuration.

## Development verification

```sh
go test ./...                 # integration tests explicitly skip if not configured
docker compose -f docker-compose.test.yml up -d --wait
export MATCHCORE_TEST_DATABASE_URL='postgres://matchcore:test-only-password@localhost:25432/matchcore_test?sslmode=disable'
export MATCHCORE_TEST_NATS_URL='nats://localhost:24222'
export MATCHCORE_TEST_REDIS_ADDR='localhost:26379'
make test-integration
make test-race
make lint
cd web && npm ci && npm run build && npm test
```

PowerShell uses `$env:MATCHCORE_TEST_DATABASE_URL='...'` (and similarly for NATS/Redis), then `go test -race -count=1 ./...`. The test Compose project deliberately has separate PostgreSQL/NATS/Redis services and ports; do not point tests at a running demo matcher. Ownership locks are database-wide per instrument even when the test harness isolates tables in schemas. The race detector requires a supported platform and C compiler; the supplied Go Debian image and Ubuntu CI include one.

Browser tests: with Compose running, `cd web; npm ci; npx playwright install --with-deps chromium; npm run test:e2e`. They submit/cancel an actual order, inspect account/operations views, test mobile layout, and verify the provisioned Grafana dashboard. Screenshots go into `docs/screenshots`. Set `DEMO_API_KEY` and `GRAFANA_PASSWORD` if changing development credentials. Local Vite development uses `npm run dev` and proxies to `localhost:8080`; Docker uses Nginx service URLs.

## Limits before shared deployment

This Compose setup binds loopback and is a demonstration, not a production deployment. Add TLS, separated database roles, service authentication, backups/PITR, resource limits, alert routing, secret rotation and persistent trace storage for a shared environment. There is no Kubernetes/HA deployment claim. One authoritative matching process is the supported topology; gateways can be replicated with distinct durable `GATEWAY_ID` values. Moving instrument ownership requires a controlled stop/recovery; automatic sharding and election are future work.
