# Matching, units and protection

Each side is a deterministic treap keyed by positive price ticks. A fixed integer mixing function derives treap priority from price; no random source or wall clock influences matching. Each level contains a doubly linked FIFO queue and aggregate remaining quantity. An order-ID map points directly into a queue. Makers execute at their resting price, starting with the lowest ask for a buy and the highest bid for a sell. A partial maker retains its queue position.

Let N be resting orders, L price levels and M executions in a command. Expected insertion and deleting an empty price level cost O(log L); cancelling an order within a surviving level is O(1) after the map lookup. Best-price lookup is O(log L); a sweep costs O(M + visited levels × log L). Memory is O(N + L), plus O(M) for a plan. The price hash gives practical balance, not a worst-case adversarial logarithmic guarantee. Ordinary matching never scans every order. Snapshot extraction walks levels and FIFO queues in O(N + L), in canonical bid/ask and ascending-price order. JSON encoding adds proportional work. Full depth views currently cost O(L log L) per durable command; this explicit simplicity trade-off is outside the pure matching benchmark.

All three instruments use 1,000 lots per coin. Base balances are lots. Quote balances and `max_spend` are USD cents. One price tick is one USD cent per lot, equivalent to $10 per coin. Thus `price=5000, quantity=1000` means one BTC at $50,000/BTC, with a notional of 5,000,000 cents. This intentionally coarse simulated tick grid avoids fractional cents and keeps conversions exact. Prices and quantities are positive checked `int64` values; multiplication overflow is rejected. JavaScript uses BigInt to parse and format fixed units; accepted configured financial ranges fit exact JS integers.

Both counterparties pay 10 bps in quote currency, rounded up **per execution**: `fee(n) = n/1000 + (n%1000 != 0)`. A seller receives `notional-fee`; a buyer pays `notional+fee`. Fees move into the explicit `fees` account. Average execution price is cumulative notional divided by filled lots, rounded down to a price tick for display; original integer trade prices and quantities remain authoritative. No float is used for financial arithmetic.

For a resting buy, reserve `remaining × (limit_price + fee(limit_price))`. The per-lot fee bound safely covers rounding even if fills fragment into one-lot executions. A sell reserves its base lots. A market buy checks its entire explicit budget, including fees, against available USD before execution and uses a binary search for the affordable fill quantity. A market sell is bounded by configured notional exposure. Excess buy reservation is released at commit; market and IOC remainders never rest.

Supported behavior:

| Situation | Deterministic result |
|---|---|
| Limit GTC, uncrossed remainder | Rest at its limit price |
| Limit IOC or market IOC remainder | Cancel remainder; retain filled quantity |
| Empty opposing book | GTC rests; IOC/market cancels without a trade |
| Incoming order reaches its own account's liquidity | Cancel incoming remainder immediately; earlier non-self fills stand |
| Exhausted market budget or notional cap | Cancel incoming remainder |
| 1,024 executions in one command | Cancel remaining aggressive quantity to bound SQL and message sizes |
| More than 100,000 open orders | Reject command without applying its planned trades |
| Invalid quantity, limit price, TIF, market budget, or market price other than zero | Persist rejection; no financial effects |
| Cancellation after fill/cancel, or another owner's order | Persist rejected cancellation; no balance change |
| Exact idempotent retry | Return the original committed response |
| Same key with changed request | Conflict; no new event |

No improperly crossed GTC remainder is left when a safety protection stops a sweep. Terminal states cannot return to open states. Cancelled orders keep their unfilled quantity so `filled + remaining = original`; cancellation is represented by status, not by discarding unfilled lots. Invalid negative request quantities are normalized to zero in the rejected projection. FOK is deliberately unsupported; it was optional in the brief and requires a separate atomic liquidity and self-trade precheck.
