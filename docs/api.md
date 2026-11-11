# API and streaming protocol

The authoritative service uses generated Protobuf/gRPC at `localhost:9090`. The HTTP gateway at `localhost:8080` translates to that service; it has no alternate matching logic. The browser's Nginx origin proxies `/api` and `/ws` to the gateway. [Protobuf](../api/proto/matchcore/v1/exchange.proto), [generated Go clients](../api/gen/matchcore/v1/) and [OpenAPI](../api/openapi/openapi.json) are included. `buf lint` validates the contract. Regenerate Go bindings with `make generate` after installing protoc; generator plugin versions are pinned in the script.

All requests require `Authorization: Bearer <API key>`, except liveness, readiness, metrics and the initial WebSocket upgrade. PlaceOrder, CancelOrder, GetOrder, GetOrderBook, GetRecentTrades, GetAccountBalances, GetOrderHistory, ListInstruments, GetOperations, GetAudit, GetAccountSummary and GetMyTrades are implemented RPCs. Inputs are strongly typed Protobuf. `Result.json` is a byte field carrying the documented JSON domain document, shared with HTTP and the frontend; grpcurl displays that bytes field in base64. This keeps one domain representation but sacrifices fully typed Protobuf responses; it is an explicit API trade-off.

Use integer lots and ticks. One coin = 1,000 lots; `price=5000` is $50,000 per coin; `max_spend=5005000` is $50,050 including fees. Timestamps are Unix milliseconds. Query histories are bounded to 200 orders/ledger entries and 100 trades; pagination is a known limitation. `/api/account` supplies lifetime fee/execution/order totals, independent of history limits. Prices in a multi-level order's average field are rounded down ticks; inspect individual executions for exact prices.

## HTTP walkthrough

The following keys are **public, local-only development credentials from `.env.example`**. Use replacement environment secrets for shared installations.

```sh
# Bob offers 2 BTC.
curl -sS http://localhost:8080/api/orders \
  -H 'Authorization: Bearer local-bob-key-0000000002' \
  -H 'Content-Type: application/json' \
  -d '{"client_id":"example-sell-1","instrument":"BTC-USD","side":"SELL","type":"LIMIT","tif":"GTC","price":5000,"quantity":2000}'

# Alice buys 1 BTC. Copy the seller's returned order ID to cancel its remainder.
curl -sS http://localhost:8080/api/orders \
  -H 'Authorization: Bearer local-alice-key-00000001' \
  -H 'Content-Type: application/json' \
  -d '{"client_id":"example-buy-1","instrument":"BTC-USD","side":"BUY","type":"LIMIT","tif":"GTC","price":5000,"quantity":1000}'

curl -sS 'http://localhost:8080/api/book?instrument=BTC-USD' \
  -H 'Authorization: Bearer local-alice-key-00000001'
curl -sS http://localhost:8080/api/balances \
  -H 'Authorization: Bearer local-alice-key-00000001'
curl -sS http://localhost:8080/api/cancel \
  -H 'Authorization: Bearer local-bob-key-0000000002' \
  -H 'Content-Type: application/json' \
  -d '{"key":"example-cancel-1","instrument":"BTC-USD","order_id":"COPY_RETURNED_SELL_ORDER_ID"}'
```

Other GET routes: `/api/instruments`, `/api/trades?instrument=BTC-USD`, `/api/orders`, `/api/order?order_id=...`, `/api/operations`, `/api/audit`, `/api/identity`, `/api/account`, `/api/my-trades` and `/api/market?instrument=BTC-USD`. The last endpoint is a best-effort expiring Redis last-trade cache and may return null.

PowerShell order entry:

```powershell
$headers = @{Authorization='Bearer local-alice-key-00000001'}
$body = @{client_id='ps-buy-1';instrument='BTC-USD';side='BUY';type='LIMIT';tif='GTC';price=5000;quantity=1000} | ConvertTo-Json
Invoke-RestMethod http://localhost:8080/api/orders -Method Post -Headers $headers -ContentType application/json -Body $body
```

An example market buy uses `type=MARKET`, `tif=IOC`, `price=0`, and a positive `max_spend`. Limit orders accept GTC/IOC. Client IDs and cancellation keys share an account-global idempotency namespace. Retrying the identical command returns the original outcome, including rejection. Changing fields under a used key is a conflict.

```sh
grpcurl -plaintext -import-path api/proto -proto matchcore/v1/exchange.proto \
  -H 'authorization: Bearer local-alice-key-00000001' \
  -d '{"instrument":"BTC-USD"}' localhost:9090 matchcore.v1.Exchange/GetOrderBook
```

## Errors

| HTTP | gRPC / domain meaning |
|---|---|
| 400 | InvalidArgument: malformed/oversized transport input, missing key |
| 401 | Unauthenticated |
| 403 | PermissionDenied / origin or admin-role rejection |
| 404 | NotFound: unknown instrument, absent owned order or route |
| 409 | AlreadyExists: changed request under an existing idempotency key |
| 422 | Committed business rejection envelope: `error`, `events`, `sequence`, optional REJECTED order |
| 429 | ResourceExhausted: rate limit or queue saturation |
| 503 | Unavailable: unsafe engine or uncertain commit; retry the original key after recovery |
| 504 / 408 | DeadlineExceeded / Canceled; outcome may already have committed |
| 500 | Internal: persistence/serialization failure with sanitized message |

Business rejections are gRPC successful `Result` values with `error` and a committed sequence; they are domain outcomes, distinct from transport errors. HTTP gives them 422 while preserving the same envelope. HTTP responses carry `X-Request-ID`; transport errors include it in JSON. Matching logs record the command correlation key, sequence and trace ID. The server caps gRPC command lifetime at ten seconds even if a client omits its own deadline.

## WebSocket snapshot and delta

Open a WebSocket from an allowed origin (`http://localhost:5173` by default). Within five seconds send:

```json
{"op":"auth","token":"YOUR_API_KEY"}
```

After the `authenticated` message, send:

```json
{"op":"subscribe","instrument":"BTC-USD","channels":["depth","bbo","trades","last","statistics","orders"]}
```

Subscriptions deliver the **combined instrument stream**; the channel list validates capability names and is not a server-side field filter. Omit it to receive the same combined stream. An optional `account` field must equal the authenticated account. `{"op":"unsubscribe","instrument":"BTC-USD"}` stops delivery.

First comes a `snapshot` with full aggregated `depth`, instrument, sequence and timestamp. The gateway registers a synchronization buffer before fetching the snapshot, then queues it before buffered later events. Thereafter a `delta` contains instrument, committed command sequence, timestamp, signed aggregate `levels` changes, public `trades`, authenticated account `orders`, and statistics for that command (`executions`, `volume_lots`, `notional_cents`). Derive BBO from the ordered depth and last price from the latest execution. Per-command statistics can be accumulated over a chosen window; the UI labels its tape as the most recent executions rather than inventing 24-hour metrics.

Discard messages with sequence <= the snapshot/current sequence. Require the next distinct delta to equal current+1, including zero-trade rejection/cancellation commands. On a gap or negative derived depth, stop applying deltas and resubscribe for a snapshot. The frontend implements this algorithm in `domain.ts`. A gateway restart or slow-client disconnect likewise requires a fresh snapshot. Messages are committed-only, delivered at least once, and contain no other account's private order IDs or identities.

## Administrative restrictions

`POST http://localhost:8081/admin/restrictions` accepts `{"account":"bob","restricted":true}` with an admin bearer key. A trader receives 403. The update takes the same account row lock as financial transactions; it blocks later placements and still allows cancellation. It is a local administrative HTTP endpoint, separate from the customer gateway.
