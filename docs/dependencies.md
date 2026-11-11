# Dependency selection and API verification

Versions were resolved and exercised on 11 November 2026. The Go checksum database and npm lockfile pin dependency resolution; Docker release references intentionally remain readable patch/major tags, so a future pull can receive vendor patches. Record image digests when reproducing a fixed artifact. `go1.26.11` was actually executed; Go 1.26 remains within the Go project's two-current-major support policy.

Primary sources consulted:

- [Go releases](https://go.dev/dl/) and [release policy](https://go.dev/doc/devel/release).
- [gRPC Go generation and server/client API](https://grpc.io/docs/languages/go/quickstart/).
- [pgx pool and transaction API](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool).
- [coder WebSocket API](https://pkg.go.dev/github.com/coder/websocket).
- [NATS JetStream headers and deduplication](https://docs.nats.io/nats-concepts/jetstream/headers).
- [OpenTelemetry Go initialization and exporters](https://opentelemetry.io/docs/languages/go/getting-started/).
- [Vite setup and Node compatibility](https://vite.dev/guide/).
- [Prometheus LTS policy](https://prometheus.io/docs/introduction/release-cycle/) and [3.13.5](https://github.com/prometheus/prometheus/releases/tag/v3.13.5): supported 3.13 LTS series.
- [Grafana 13.3.1](https://github.com/grafana/grafana/releases/tag/v13.3.1): current vendor patch release.
- [Collector 0.165.0](https://github.com/open-telemetry/opentelemetry-collector-releases/releases/tag/v0.165.0).
- [k6 2.3.0](https://github.com/grafana/k6/releases/tag/v2.3.0).
- [NGINX stable download stream](https://nginx.org/en/download.html): 1.30 stable used for the non-root frontend image.

Initial module-level vulnerability findings were removed by upgrading `x/net`, `x/crypto`, `x/sync`, `x/sys`, `x/text` and `klauspost/compress` to compatible patched releases. The final scan is stored alongside raw test/benchmark evidence. No advisories were suppressed, no assertions were disabled, and no successful test/benchmark outcome is inferred from unexecuted code. Repeat scans regularly; a clean dated scan is not a permanent security guarantee.
