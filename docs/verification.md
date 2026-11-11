# Executed verification

Verified locally on **11 November 2026**, using Linux/ARM64 containers on Windows/Docker Desktop. The complete app was built and started against real PostgreSQL 18, Redis 8 and NATS 2.14 JetStream. No remote GitHub Actions run or production deployment is claimed.

| Check | Actual result / evidence |
|---|---|
| Go unit, deterministic/property, transport and real-service tests with `-race -count=1` | Passed: 26 top-level tests/fuzz targets, 37 passing named test/subtest entries, zero failures. [Full JSON test output](../benchmarks/results/tests-go.jsonl); named tests were not skipped. Packages without test files are reported separately by Go. |
| Snapshot/restart lifecycle stress, 10 repeats under race detector | Passed. [Targeted output](../benchmarks/results/lifecycle-repeat.txt) |
| Formatting, `go vet`, staticcheck v0.8.2 | Passed through `make lint` in the Go build environment |
| Buf v1.61.0 lint | Passed on the actual checked-in Protobuf contract |
| Go vulnerability scan v1.9.0 | No vulnerabilities found after compatible dependency updates. [Final scan](../benchmarks/results/security-go-final.txt), [module detail](../benchmarks/results/security-go.txt) |
| Frontend TypeScript and Vite production build | Passed; real backend calls, no mocked API endpoints |
| Vitest | Four stream/numeric tests passed, including duplicate/gap/negative-depth handling and exact fixed-unit parsing |
| npm audit | Zero reported vulnerabilities. [JSON scan](../benchmarks/results/security-npm.json) |
| Docker backend/frontend builds and full Compose startup | Passed with current container versions and real health checks. [Service output](../benchmarks/results/compose-services.jsonl) |
| Actual-service acceptance and matcher restart | Passed before and after real process restart in an isolated Compose project. [Assertions](../benchmarks/results/acceptance.txt), [captured state](../benchmarks/acceptance-state.json) |
| Desktop terminal, real submit/cancel, operations/account, mobile and Grafana | Two Playwright tests passed. [Output](../benchmarks/results/browser-tests.txt) and [screenshots](screenshots/) |
| Go benchmark matrix, profiles, snapshot optimization, event decoding/replay | Executed; raw outputs retained. [Method and numbers](performance.md) |
| Durable / shared-account / API latency benchmarks | Executed against actual services, with zero failed measured commands. See `benchmarks/results/*.json` and the performance guide |
| k6 v2.3.0, 20 scheduled requests/s for 10 s | 201 checks passed, zero failed HTTP requests; median 6.566 ms, p95 11.654 ms, p99 14.663 ms. [Raw output](../benchmarks/results/k6.json) |
| OTel export | Collector 0.165.0 received real HTTP/gRPC/engine spans; structured application logs contain trace IDs |

## Failures found and corrected

- Initial compile/type checks found an unused Go import and React 19 `useRef` initialization mismatch; both were corrected and rebuilt.
- Browser testing from a non-localhost HTTP origin exposed `crypto.randomUUID`'s secure-context requirement. Client IDs now use `crypto.getRandomValues`, and real order submission passed from the container browser.
- The first Grafana browser test navigated away before login completed, and provisioning had started before the new dashboard file existed. The test now awaits the actual login response; the provisioned current image starts with the dashboard source present and passes.
- An integration run pointed at the running demo database correctly failed ownership claims. A separate Compose test stack now isolates PostgreSQL, Redis and NATS from the demo.
- Rapid close/reopen testing exposed asynchronous advisory-lock release during TCP teardown. Graceful close now waits for PostgreSQL's explicit unlock acknowledgement; full tests and ten repeated snapshot/restart checks pass.
- Once browser specs were added, default Vitest discovery tried to execute Playwright files. Unit tests now have an explicit source-test configuration; browser assertions run under their own real Playwright runner in CI.
- An initial k6 summary could not write into a root-owned mounted results directory. The workload now emits its summary to stdout, captured by the caller; the successful v2.3 result is checked in.
- Dependency scanning initially found four module-level advisories in unused functions. Compatible patch upgrades removed those findings; the final scan reports none.

No outstanding failed executed checks remain. Initial failures are recorded here rather than hidden or converted into disabled assertions. Actual checks cover the stated simulation guarantees, not arbitrary network partitions, database failover, cross-region operation or hardware durability.

## Verification limits

The CI definition is supplied and local equivalents ran, but it has not run on GitHub until the user pushes this local repository. The measured performance samples are short and shared-system; memory/client/durable boundaries differ. Container images were built and run, while a complete OS/package CVE audit and independent security review were not performed. Financial invariants have executable tests, not a formal proof. Live TLS/mTLS, backup restoration/PITR, HA/failover, global rate limits and production capacity remain deployment work. The project is a simulated exchange with public local-only credentials.
