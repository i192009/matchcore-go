# MatchCore screenshots

Captured on **11 November 2026** from the production React/Nginx build and provisioned Grafana dashboard, using Playwright against real local application services.

PostgreSQL, Redis, NATS JetStream, the matching service, outbox publisher and WebSocket gateway were running. Orders, executions, balance reservations and metrics came from those services. Prices and balances are **explicitly simulated**; these images do not represent a real exchange or live market feed.

The browser verified authentication, streaming synchronization, actual order submission and cancellation, account and operations views, mobile layout, and the provisioned Grafana dashboard. The automated acceptance scenario separately verified settlement, fees, SQL/event records and matching-service restart recovery. See [verification details](../docs/verification.md) and [browser output](../benchmarks/results/browser-tests.txt).

| File | Screen | Dimensions |
|---|---|---|
| [terminal.png](terminal.png) | Trading terminal with simulated depth, executions and a resting order | 1512 ? 1624 |
| [operations.png](operations.png) | Operations, writer state, queues and measured latency estimates | 1512 ? 1157 |
| [account.png](account.png) | Available/reserved assets, fees, executions and transaction journal | 1512 ? 22601 |
| [grafana.png](grafana.png) | Provisioned engine and event-delivery metrics dashboard | 1512 ? 982 |
| [mobile.png](mobile.png) | Responsive trading terminal at mobile width | 390 ? 2790 |

## Main views

[![Trading terminal](terminal.png)](terminal.png)

[![Operations dashboard](operations.png)](operations.png)

[![Grafana monitoring](grafana.png)](grafana.png)

## Reproduce

Follow the [local setup](../README.md#run-it), seed/start the simulator, then run the [Playwright checks](../docs/deployment.md#development-verification). The browser suite writes fresh captures to `docs/screenshots/`; this folder contains the selected publication copies.
