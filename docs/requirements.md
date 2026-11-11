# Delivery coverage

| Requirement area | Implementation / evidence |
|---|---|
| Multiple simulated instruments and independent books | Fixed BTC/ETH/SOL configurations; three actor goroutines; no external exchange/payment integration |
| Limit GTC/IOC, market IOC, partial/full fills and cancel | `internal/core/book.go`; price-time, bid/ask, FIFO and self-trade tests |
| Explicit order states and checked integer finance | `core/model.go`; transitions, overflow, fee and reservation tests |
| Efficient level/FIFO/index structures | Deterministic treap, aggregate level quantities, direct order index; measured Go microbenchmarks |
| Funding, restrictions, reservations and fees | SQL seed issuance/supply, account row locks, conservative reservations, admin restrictions, fee account |
| Atomic settlement and asset conservation | Single SQL transaction for all effects; balanced ledger; conservation and rounding tests |
| Cross-instrument risk and duplicate requests | Sorted account/balance locks; account-global command keys; concurrent reservation/duplicate tests |
| Persist before memory changes | Read-only plan; commit/apply boundary; pre-commit SQL failure and post-commit crash injection |
| Ownership / fencing | PostgreSQL session lock, instrument epoch, sequence compare; duplicate-owner/stale-writer tests |
| Append-only event log and outbox | Checksummed byte payloads, semantic subevents, mutation trigger, transactionally inserted outbox |
| Snapshots / deterministic recovery | Version/config/checksum validation, contiguous replay and SQL projection comparison; valid/absent/corrupt snapshot tests |
| Reliable downstream delivery | Durable JetStream ack, stable message IDs, at-least-once semantics, actual broker-ack crash/retry test |
| Generated gRPC and HTTP APIs | Generated Protobuf clients/server, actual gateway translation, Buf lint, OpenAPI and usage guide |
| Authentication and secure transports | Scoped environment keys, ownership, admin role, explicit origins, rate/request/deadline limits; security tests |
| Real market-data WebSockets | Committed snapshot + signed depth deltas, executions/last/BBO derivation, command statistics, private filtering and gap recovery |
| Bounded lifecycle / slow clients | Bounded actor and client queues, cancellation, shutdown, overflow disconnect; saturation/disconnection race tests |
| Usable dashboard | Real terminal/order/cancel, operations, balances, own trades, lifetime fees and journal; desktop/mobile browser assertions/screenshots |
| Redis caching | Expiring sequenced public last-trade cache; Redis never participates in financial decisions |
| Monitoring and tracing | Prometheus metrics, provisioned Grafana, correlated JSON logs and OTel Collector receiving real spans |
| Performance matrix | 1k/10k/100k books × 10/50/90% crossing; insertion/matching/partial/cancel/sweep/snapshot/replay microbenchmarks |
| Durable and E2E measurements | Real SQL actor benchmark, shared-account contention benchmark, API benchmark and k6; raw output and hardware/method documentation |
| Profiling and optimization | CPU profile; canonical snapshot traversal; prior and optimized samples retained |
| Container startup and migrations | Actual Compose startup, health checks, persistent volumes, migration/seed ordering and non-root app images |
| CI / code quality | Formatting, vet, staticcheck, race/real dependencies, frontend build/tests, dependency scans, Buf, Docker acceptance and Playwright workflow |
| End-to-end acceptance | Automated exact 2 BTC sell / 1 BTC buy, financial/SQL/stream assertions, cancel, actual matcher restart and idempotent retry |
| Documentation and architecture rationale | README, API/deployment, consistency, algorithm, event/recovery, security, failure matrix, performance and five ADRs |

Explicit scope decisions: FOK was optional and is not supported. Instruments and initial demo accounts are configured, not a public listing/account-onboarding product. Response documents use a JSON bytes payload inside real generated gRPC messages. Histories are bounded without pagination. Fee-account contention, full-depth materialization, coarse simulated ticks, plaintext local networking, no automatic HA/election and no persistent trace store are documented limits. No Kubernetes production deployment, compliance, real-asset custody or arbitrary-partition availability is claimed.
