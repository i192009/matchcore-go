import React, { useCallback, useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  decimalToUnits,
  dollars,
  fixed,
  lots,
  mergeDepth,
  money,
  newClientID,
  type Balance,
  type Delta,
  type Depth,
  type Order,
  type Trade,
} from "./domain";
import "./style.css";

type Ops = {
  latency?: Record<string, number>;
  ready: boolean;
  engines: {
    instrument: string;
    sequence: number;
    queue_depth: number;
    queue_capacity: number;
    safe: boolean;
  }[];
  orders: number;
  trades: number;
  outbox_backlog: number;
  event_delay_seconds: number;
  uptime_seconds: number;
};
class APIError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message);
  }
}
function App() {
  const [summary, setSummary] = useState<{
    fees_cents: number;
    executions: number;
    orders: number;
  }>();
  const [executions, setExecutions] = useState<
    (Trade & { instrument: string; side: string; fee: number })[]
  >([]);
  const [token, setToken] = useState(
    () => sessionStorage.getItem("matchcore-key") ?? "",
  );
  const [keyInput, setKeyInput] = useState("");
  const [identity, setIdentity] = useState("");
  const [tab, setTab] = useState("Terminal");
  const [instrument, setInstrument] = useState("BTC-USD");
  const [instruments, setInstruments] = useState<string[]>([]);
  const [depth, setDepth] = useState<Depth>();
  const [trades, setTrades] = useState<Trade[]>([]);
  const [orders, setOrders] = useState<Order[]>([]);
  const [balances, setBalances] = useState<Balance[]>([]);
  const [audit, setAudit] = useState<
    { asset: string; available_delta: number; reserved_delta: number }[]
  >([]);
  const [ops, setOps] = useState<Ops>();
  const [rate, setRate] = useState(0);
  const previousOps = useRef<{ time: number; orders: number } | undefined>(
    undefined,
  );
  const [connection, setConnection] = useState("Disconnected");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [side, setSide] = useState("BUY");
  const [type, setType] = useState("LIMIT");
  const [tif, setTif] = useState("GTC");
  const [price, setPrice] = useState("50000");
  const [quantity, setQuantity] = useState("1");
  const [budget, setBudget] = useState("50100");
  const pending = useRef<Record<string, unknown> | null>(null);
  const [uncertain, setUncertain] = useState(false);
  const socket = useRef<WebSocket | null>(null);
  const api = useCallback(
    async (path: string, body?: unknown) => {
      const r = await fetch("/api/" + path, {
        method: body ? "POST" : "GET",
        headers: {
          Authorization: "Bearer " + token,
          "Content-Type": "application/json",
        },
        body: body ? JSON.stringify(body) : undefined,
      });
      const value = await r.json();
      if (!r.ok) throw new APIError(value.error ?? "Request failed", r.status);
      return value;
    },
    [token],
  );
  const refreshAccount = useCallback(async () => {
    try {
      const [o, b, a, s, t] = await Promise.all([
        api("orders"),
        api("balances"),
        api("audit"),
        api("account"),
        api("my-trades"),
      ]);
      setOrders(o);
      setBalances(b);
      setAudit(a);
      setSummary(s);
      setExecutions(t);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [api]);
  useEffect(() => {
    if (!token) return;
    let active = true;
    setError("");
    api("identity")
      .then((v) => {
        if (active) {
          setIdentity(v.account);
          sessionStorage.setItem("matchcore-key", token);
        }
      })
      .catch((e) => {
        if (active) {
          setError(e.message);
          setToken("");
          sessionStorage.removeItem("matchcore-key");
        }
      });
    api("instruments")
      .then((v) => {
        if (active) setInstruments(v.map((i: { id: string }) => i.id));
      })
      .catch((e) => setError(e.message));
    void refreshAccount();
    return () => {
      active = false;
    };
  }, [token, api, refreshAccount]);
  useEffect(() => {
    if (!token) return;
    let disposed = false;
    let retry: ReturnType<typeof setTimeout>;
    let ws: WebSocket;
    let book: Depth | undefined;
    setDepth(undefined);
    setTrades([]);
    const connect = () => {
      setConnection("Connecting");
      ws = new WebSocket(
        (location.protocol === "https:" ? "wss://" : "ws://") +
          location.host +
          "/ws",
      );
      socket.current = ws;
      ws.onopen = () => ws.send(JSON.stringify({ op: "auth", token }));
      ws.onmessage = (event) => {
        const message = JSON.parse(event.data);
        if (message.type === "authenticated") {
          setConnection("Synchronizing");
          ws.send(
            JSON.stringify({
              op: "subscribe",
              instrument,
              channels: ["depth", "trades", "orders"],
            }),
          );
          return;
        }
        if (message.instrument !== instrument) return;
        if (message.type === "snapshot") {
          book = message.depth;
          setDepth(book);
          setConnection("Streaming");
          void api("trades?instrument=" + instrument).then((v) => setTrades(v));
          return;
        }
        if (message.type === "delta" && book) {
          try {
            const next = mergeDepth(book, message as Delta);
            if (next === book) return;
            book = next;
            setDepth(book);
            if (message.trades.length)
              setTrades((v) =>
                [...message.trades, ...v]
                  .filter((t, i, a) => a.findIndex((x) => x.id === t.id) === i)
                  .slice(0, 60),
              );
            if (message.orders.length) void refreshAccount();
          } catch (e) {
            setConnection("Recovering sequence");
            book = undefined;
            ws.send(JSON.stringify({ op: "subscribe", instrument }));
          }
        }
        if (message.type === "error") setError(message.error);
      };
      ws.onclose = () => {
        if (!disposed) {
          setConnection("Disconnected · reconnecting");
          retry = setTimeout(connect, 2000);
        }
      };
      ws.onerror = () => ws.close();
    };
    connect();
    return () => {
      disposed = true;
      clearTimeout(retry);
      ws.close();
      socket.current = null;
    };
  }, [token, instrument, api, refreshAccount]);
  useEffect(() => {
    if (!token) return;
    const update = () =>
      api("operations")
        .then((v: Ops) => {
          const now = Date.now();
          if (previousOps.current)
            setRate(
              Math.max(
                0,
                ((v.orders - previousOps.current.orders) * 1000) /
                  (now - previousOps.current.time),
              ),
            );
          previousOps.current = { time: now, orders: v.orders };
          setOps(v);
        })
        .catch((e) => setError(e.message));
    void update();
    const timer = setInterval(update, 5000);
    return () => clearInterval(timer);
  }, [token, api]);
  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    setNotice("");
    try {
      let body = pending.current;
      if (!body) {
        const q = decimalToUnits(quantity, 1000);
        if (q <= 0) throw new Error("Quantity must be positive");
        let ticks = 0;
        if (type === "LIMIT") {
          const dollarsInt = decimalToUnits(price, 1);
          if (dollarsInt % 10 !== 0)
            throw new Error("Price must be a multiple of $10 per coin");
          ticks = dollarsInt / 10;
          if (ticks <= 0) throw new Error("Price must be positive");
        }
        body = {
          client_id: newClientID(),
          instrument,
          side,
          type,
          tif: type === "MARKET" ? "IOC" : tif,
          price: ticks,
          quantity: q,
          max_spend:
            type === "MARKET" && side === "BUY"
              ? decimalToUnits(budget, 100)
              : 0,
        };
        pending.current = body;
      }
      const response = await api("orders", body);
      pending.current = null;
      setUncertain(false);
      setNotice(
        `${response.order?.status ?? "Committed"} · sequence ${response.sequence} · ${response.plan.trades.length} executions`,
      );
      await refreshAccount();
    } catch (e) {
      const err = e as APIError;
      setError(err.message);
      if (
        pending.current &&
        (!err.status || err.status >= 500 || err.status === 408)
      ) {
        setUncertain(true);
      } else {
        pending.current = null;
        setUncertain(false);
      }
    } finally {
      setBusy(false);
    }
  }
  async function cancel(order: Order) {
    setBusy(true);
    setError("");
    try {
      const v = await api("cancel", {
        key: "cancel-" + order.id,
        instrument: order.instrument,
        order_id: order.id,
      });
      setNotice("Cancellation committed · sequence " + v.sequence);
      await refreshAccount();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const open = orders.filter(
    (o) =>
      o.instrument === instrument &&
      (o.status === "NEW" || o.status === "PARTIALLY_FILLED"),
  );
  const bestBid = depth?.bids[0]?.price;
  const bestAsk = depth?.asks[0]?.price;
  const last = trades[0]?.price;
  const usd = balances.find((b) => b.asset === "USD");
  const tape = [...trades].reverse();
  const minPrice = tape.length ? Math.min(...tape.map((t) => t.price)) : 0;
  const maxPrice = tape.length ? Math.max(...tape.map((t) => t.price)) : 0;
  const chart = tape
    .map(
      (t, i) =>
        `${20 + (i * 760) / Math.max(1, tape.length - 1)},${170 - ((t.price - minPrice) * 130) / Math.max(1, maxPrice - minPrice)}`,
    )
    .join(" ");
  if (!token)
    return (
      <main className="login">
        <div className="login-card">
          <div className="logo">
            M<span>↗</span>
          </div>
          <p className="eyebrow">MATCHCORE / EXCHANGE LAB</p>
          <h1>
            Every execution.
            <br />
            Accounted for.
          </h1>
          <p className="muted">
            A deterministic trading simulation with durable matching and
            real-time market data. All balances are simulated.
          </p>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              setToken(keyInput.trim());
            }}
          >
            <label htmlFor="api-key">Local API key</label>
            <input
              id="api-key"
              type="password"
              autoComplete="off"
              required
              value={keyInput}
              onChange={(e) => setKeyInput(e.target.value)}
              placeholder="Use a development key from .env"
            />
            <button className="primary">Open terminal →</button>
          </form>
          {error && (
            <p role="alert" className="error">
              {error}
            </p>
          )}
          <small>
            Run <code>make seed</code> to fund the demo accounts.
            <br />
            No real assets. No external exchange connections.
          </small>
        </div>
      </main>
    );
  return (
    <div className="app">
      <aside>
        <div className="brand">
          <div className="logo">
            M<span>↗</span>
          </div>
          <strong>
            matchcore<span>EXCHANGE LAB</span>
          </strong>
        </div>
        <p className="nav-label">WORKSPACE</p>
        <nav>
          {["Terminal", "Operations", "Account"].map((name, i) => (
            <button
              aria-label={name}
              key={name}
              className={tab === name ? "active" : ""}
              onClick={() => setTab(name)}
            >
              <span>{["▦", "◴", "◈"][i]}</span>
              {name}
              {tab === name && <b>•</b>}
            </button>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <div className="sim-badge">◇ SIMULATION ONLY</div>
          <p>
            Deterministic matching.
            <br />
            Transparent execution.
          </p>
          <div className="identity">
            <div className="avatar">{identity.slice(0, 1).toUpperCase()}</div>
            <div>
              <strong>{identity || "Authenticating"}</strong>
              <small>Development account</small>
            </div>
            <button
              aria-label="Sign out"
              onClick={() => {
                sessionStorage.removeItem("matchcore-key");
                setToken("");
                setIdentity("");
              }}
            >
              ↗
            </button>
          </div>
        </div>
      </aside>
      <div className="workspace">
        <header>
          <div className="breadcrumbs">
            Workspace <span>/</span> <strong>{tab}</strong>
          </div>
          <div className="header-right">
            <span
              className={
                "status " + (connection === "Streaming" ? "online" : "offline")
              }
            >
              ● {connection}
            </span>
            <span className="env">LOCAL ENVIRONMENT</span>
          </div>
        </header>
        <main>
          <div className="page-title">
            <div>
              <p className="eyebrow">MATCHCORE / {tab.toUpperCase()}</p>
              <h1>
                {tab === "Terminal"
                  ? "Trading terminal"
                  : tab === "Operations"
                    ? "System overview"
                    : "Account overview"}
                <span className="pill">SIMULATED</span>
              </h1>
              <p className="muted">
                {tab === "Terminal"
                  ? "Price-time priority. Committed executions. Full visibility."
                  : tab === "Operations"
                    ? "Measured service state from the authoritative engine."
                    : "Your simulated assets, reservations and execution history."}
              </p>
            </div>
            <div className="seq">
              ENGINE SEQUENCE
              <strong>{depth?.sequence.toLocaleString() ?? "—"}</strong>
            </div>
          </div>
          {error && (
            <div role="alert" className="banner error">
              {error}
              <button aria-label="Dismiss error" onClick={() => setError("")}>
                ×
              </button>
            </div>
          )}
          {notice && (
            <div role="status" className="banner success">
              ✓ {notice}
              <button
                aria-label="Dismiss notification"
                onClick={() => setNotice("")}
              >
                ×
              </button>
            </div>
          )}
          {tab === "Terminal" && (
            <>
              <section className="market-bar">
                <div className="instrument">
                  <div className="coin">{instrument[0]}</div>
                  <div>
                    <label className="sr-only" htmlFor="instrument">
                      Instrument
                    </label>
                    <select
                      id="instrument"
                      value={instrument}
                      onChange={(e) => setInstrument(e.target.value)}
                    >
                      {(instruments.length ? instruments : [instrument]).map(
                        (i) => (
                          <option key={i}>{i}</option>
                        ),
                      )}
                    </select>
                    <small>Spot · simulated market</small>
                  </div>
                </div>
                <Stat
                  label="LAST EXECUTION"
                  value={last ? dollars(last) : "No trades yet"}
                  green
                />
                <Stat
                  label="BEST BID"
                  value={bestBid ? dollars(bestBid) : "—"}
                />
                <Stat
                  label="BEST ASK"
                  value={bestAsk ? dollars(bestAsk) : "—"}
                />
                <Stat
                  label="SPREAD"
                  value={bestBid && bestAsk ? dollars(bestAsk - bestBid) : "—"}
                />
              </section>
              <div className="trading-grid">
                <section className="panel chart-panel">
                  <PanelTitle
                    title="Execution tape"
                    detail="Committed trades only"
                  />
                  <div className="chart-caption">
                    <strong>
                      {last ? dollars(last) : "Waiting for executions"}
                    </strong>
                    <span>
                      {instrument} · most recent {trades.length} trades
                    </span>
                  </div>
                  {trades.length > 1 ? (
                    <svg
                      className="chart"
                      viewBox="0 0 800 210"
                      role="img"
                      aria-label="Recent simulated execution prices"
                    >
                      <defs>
                        <linearGradient id="fill" x1="0" y1="0" x2="0" y2="1">
                          <stop stopColor="#48dfba" stopOpacity=".18" />
                          <stop
                            offset="1"
                            stopColor="#48dfba"
                            stopOpacity="0"
                          />
                        </linearGradient>
                      </defs>
                      {[40, 85, 130, 175].map((y) => (
                        <line
                          key={y}
                          x1="20"
                          y1={y}
                          x2="780"
                          y2={y}
                          stroke="#25303b"
                          strokeDasharray="3 5"
                        />
                      ))}
                      <polygon
                        points={`20,200 ${chart} 780,200`}
                        fill="url(#fill)"
                      />
                      <polyline
                        points={chart}
                        stroke="#48dfba"
                        strokeWidth="2"
                        fill="none"
                      />
                    </svg>
                  ) : (
                    <div className="chart-empty">
                      <span>⌁</span>Execution history will appear here.
                      <small>
                        Start the local simulator or place crossing orders.
                      </small>
                    </div>
                  )}
                  <div className="chart-footer">
                    <span>● PostgreSQL committed</span>
                    <span>0.001 coin / lot · $10 / price tick</span>
                  </div>
                </section>
                <section className="panel order-entry">
                  <PanelTitle title="Place an order" detail="10 bps per side" />
                  <div className="segmented">
                    {["BUY", "SELL"].map((v) => (
                      <button
                        key={v}
                        disabled={uncertain}
                        onClick={() => setSide(v)}
                        className={side === v ? v.toLowerCase() : ""}
                      >
                        {v === "BUY" ? "Buy" : "Sell"}
                      </button>
                    ))}
                  </div>
                  <form onSubmit={submit}>
                    <fieldset disabled={busy || uncertain}>
                      <div className="field-row">
                        <label>
                          Order type
                          <select
                            value={type}
                            onChange={(e) => setType(e.target.value)}
                          >
                            <option>LIMIT</option>
                            <option>MARKET</option>
                          </select>
                        </label>
                        <label>
                          Time in force
                          <select
                            value={type === "MARKET" ? "IOC" : tif}
                            disabled={type === "MARKET"}
                            onChange={(e) => setTif(e.target.value)}
                          >
                            <option>GTC</option>
                            <option>IOC</option>
                          </select>
                        </label>
                      </div>
                      {type === "LIMIT" && (
                        <label>
                          Price per coin
                          <input
                            aria-label="Price per coin"
                            inputMode="decimal"
                            value={price}
                            onChange={(e) => setPrice(e.target.value)}
                          />
                          <span className="input-unit">
                            USD · increments of 10
                          </span>
                        </label>
                      )}
                      <label>
                        Quantity
                        <input
                          aria-label="Quantity"
                          inputMode="decimal"
                          value={quantity}
                          onChange={(e) => setQuantity(e.target.value)}
                        />
                        <span className="input-unit">
                          {instrument.split("-")[0]} · increments of 0.001
                        </span>
                      </label>
                      {type === "MARKET" && side === "BUY" && (
                        <label>
                          Maximum spend, including fees
                          <input
                            aria-label="Maximum spend"
                            inputMode="decimal"
                            value={budget}
                            onChange={(e) => setBudget(e.target.value)}
                          />
                          <span className="input-unit">USD</span>
                        </label>
                      )}
                    </fieldset>
                    <div className="available">
                      Available USD{" "}
                      <strong>{usd ? money(usd.available) : "Loading…"}</strong>
                    </div>
                    <button
                      className={"submit " + side.toLowerCase()}
                      disabled={busy}
                    >
                      {busy
                        ? "Committing…"
                        : uncertain
                          ? "Retry pending request"
                          : `${side === "BUY" ? "Buy" : "Sell"} ${instrument.split("-")[0]}`}{" "}
                      <span>→</span>
                    </button>
                  </form>
                  <p className="entry-note">
                    Funds are reserved atomically. Market orders cancel any
                    unfilled remainder.
                  </p>
                </section>
                <section className="panel order-book">
                  <PanelTitle title="Order book" detail="Price / coin" />
                  <div className="table-head">
                    <span>Price (USD)</span>
                    <span>Size</span>
                    <span>Total</span>
                  </div>
                  <BookSide
                    levels={depth?.asks.slice(0, 8).reverse() ?? []}
                    side="sell"
                  />
                  <div className="midprice">
                    <strong>{last ? dollars(last) : "—"}</strong>
                    <span>
                      {bestBid && bestAsk
                        ? "Spread " + dollars(bestAsk - bestBid)
                        : "Waiting for liquidity"}
                    </span>
                  </div>
                  <BookSide levels={depth?.bids.slice(0, 8) ?? []} side="buy" />
                  {depth &&
                    depth.asks.length === 0 &&
                    depth.bids.length === 0 && (
                      <Empty text="No resting orders" />
                    )}
                  <div className="panel-foot">
                    ● Snapshot + sequenced deltas
                  </div>
                </section>
                <section className="panel recent">
                  <PanelTitle
                    title="Recent executions"
                    detail={trades.length + " trades"}
                  />
                  <div className="table-head">
                    <span>Price (USD)</span>
                    <span>Size</span>
                    <span>Time</span>
                  </div>
                  <div className="tape-rows">
                    {trades.slice(0, 12).map((t) => (
                      <div className="table-row" key={t.id}>
                        <strong className="green">{dollars(t.price)}</strong>
                        <span>{lots(t.quantity)}</span>
                        <span className="muted">
                          {new Date(t.timestamp).toLocaleTimeString("en-GB")}
                        </span>
                      </div>
                    ))}
                    {!trades.length && <Empty text="No executions yet" />}
                  </div>
                </section>
              </div>
              <section className="panel orders-panel">
                <PanelTitle
                  title={"Open orders (" + open.length + ")"}
                  detail="Your account only"
                />
                <div className="table-scroll">
                  <table>
                    <thead>
                      <tr>
                        <th>Order</th>
                        <th>Side / type</th>
                        <th>Price</th>
                        <th>Original</th>
                        <th>Remaining</th>
                        <th>Status</th>
                        <th />
                      </tr>
                    </thead>
                    <tbody>
                      {open.map((o) => (
                        <tr key={o.id}>
                          <td>{o.client_id.slice(0, 12)}</td>
                          <td className={o.side === "BUY" ? "green" : "red"}>
                            {o.side} <span className="muted">{o.type}</span>
                          </td>
                          <td>{dollars(o.price)}</td>
                          <td>{lots(o.quantity)}</td>
                          <td>{lots(o.remaining)}</td>
                          <td>
                            <span className="order-status">
                              {o.status.replaceAll("_", " ")}
                            </span>
                          </td>
                          <td>
                            <button
                              disabled={busy}
                              className="text-button"
                              onClick={() => void cancel(o)}
                            >
                              Cancel
                            </button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                  {!open.length && (
                    <Empty text="No open orders for this instrument" />
                  )}
                </div>
              </section>
            </>
          )}
          {tab === "Operations" && (
            <>
              <div className="stat-grid">
                <Stat
                  label="ORDER THROUGHPUT"
                  value={rate.toFixed(1) + " /s"}
                />
                <Stat
                  label="COMMITTED ORDERS"
                  value={String(ops?.orders ?? "—")}
                />
                <Stat
                  label="EXECUTED TRADES"
                  value={String(ops?.trades ?? "—")}
                  green
                />
                <Stat
                  label="OUTBOX BACKLOG"
                  value={String(ops?.outbox_backlog ?? "—")}
                />
                <Stat
                  label="MATCH PLAN P99 (BUCKET)"
                  value={
                    ops?.latency?.matching_p99_ms !== undefined
                      ? ops.latency.matching_p99_ms.toFixed(3) + " ms"
                      : "No samples"
                  }
                />
                <Stat
                  label="COMMAND P99 (BUCKET)"
                  value={
                    ops?.latency?.command_p99_ms !== undefined
                      ? ops.latency.command_p99_ms.toFixed(3) + " ms"
                      : "No samples"
                  }
                />
              </div>
              <section className="panel">
                <PanelTitle
                  title="Instrument writers"
                  detail={ops?.ready ? "All recovered" : "Not ready"}
                />
                <div className="table-scroll">
                  <table>
                    <thead>
                      <tr>
                        <th>Instrument</th>
                        <th>State</th>
                        <th>Sequence</th>
                        <th>Queue depth</th>
                        <th>Utilization</th>
                      </tr>
                    </thead>
                    <tbody>
                      {ops?.engines.map((e) => (
                        <tr key={e.instrument}>
                          <td>{e.instrument}</td>
                          <td className={e.safe ? "green" : "red"}>
                            {e.safe
                              ? "Recovered · accepting"
                              : "Unsafe · stopped"}
                          </td>
                          <td>{e.sequence}</td>
                          <td>
                            {e.queue_depth} / {e.queue_capacity}
                          </td>
                          <td>
                            {((e.queue_depth * 100) / e.queue_capacity).toFixed(
                              1,
                            )}
                            %
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </section>
              <div className="ops-grid">
                <section className="panel">
                  <PanelTitle
                    title="Event delivery"
                    detail="Transactional outbox"
                  />
                  <div className="ops-content">
                    <Stat
                      label="OLDEST PENDING EVENT"
                      value={(ops?.event_delay_seconds ?? 0).toFixed(3) + " s"}
                    />
                    <Stat
                      label="ENGINE UPTIME"
                      value={Math.floor(ops?.uptime_seconds ?? 0) + " s"}
                    />
                    <p className="muted">
                      NATS delivers at least once. Instrument sequences let
                      clients detect duplicates and recover gaps.
                    </p>
                  </div>
                </section>
                <section className="panel">
                  <PanelTitle
                    title="Metrics & tracing"
                    detail="Open telemetry"
                  />
                  <div className="ops-content">
                    <p className="muted">
                      Inspect command p50/p95/p99, commit latency, queues, CPU,
                      memory, recovery and event delivery.
                    </p>
                    <a
                      href="http://localhost:3000/d/matchcore"
                      target="_blank"
                      rel="noreferrer"
                      className="link-button"
                    >
                      Open Grafana ↗
                    </a>
                    <a
                      href="http://localhost:9091"
                      target="_blank"
                      rel="noreferrer"
                      className="link-button"
                    >
                      Open Prometheus ↗
                    </a>
                    <p className="muted">
                      Throughput is measured from committed order-count changes
                      every five seconds.
                    </p>
                  </div>
                </section>
              </div>
            </>
          )}
          {tab === "Account" && (
            <>
              <div className="stat-grid">
                <Stat
                  label="AVAILABLE USD"
                  value={usd ? money(usd.available) : "—"}
                  green
                />
                <Stat
                  label="RESERVED USD"
                  value={usd ? money(usd.reserved) : "—"}
                />
                <Stat
                  label="TOTAL EXECUTION FEES"
                  value={summary ? money(summary.fees_cents) : "?"}
                />
                <Stat
                  label="TOTAL EXECUTIONS"
                  value={String(summary?.executions ?? "?")}
                />
              </div>
              <section className="panel">
                <PanelTitle
                  title="Simulated asset balances"
                  detail="Available + reserved = total"
                />
                <table>
                  <thead>
                    <tr>
                      <th>Asset</th>
                      <th>Available</th>
                      <th>Reserved</th>
                      <th>Total</th>
                    </tr>
                  </thead>
                  <tbody>
                    {balances.map((b) => (
                      <tr key={b.asset}>
                        <td>
                          <strong>{b.asset}</strong>
                        </td>
                        <td>
                          {fixed(b.available, b.asset === "USD" ? 100 : 1000)}
                        </td>
                        <td>
                          {fixed(b.reserved, b.asset === "USD" ? 100 : 1000)}
                        </td>
                        <td>
                          {fixed(b.total, b.asset === "USD" ? 100 : 1000)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </section>
              <section className="panel orders-panel">
                <PanelTitle
                  title="Your executions"
                  detail="Most recent 100 executions"
                />
                <div className="table-scroll">
                  <table>
                    <thead>
                      <tr>
                        <th>Instrument</th>
                        <th>Side</th>
                        <th>Price</th>
                        <th>Quantity</th>
                        <th>Fee</th>
                        <th>Time</th>
                      </tr>
                    </thead>
                    <tbody>
                      {executions.map((t) => (
                        <tr key={t.id}>
                          <td>{t.instrument}</td>
                          <td>{t.side}</td>
                          <td>{dollars(t.price)}</td>
                          <td>{lots(t.quantity)}</td>
                          <td>{money(t.fee)}</td>
                          <td>
                            {new Date(t.timestamp).toLocaleTimeString("en-GB")}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                  {!executions.length && <Empty text="No executions yet" />}
                </div>
              </section>
              <section className="panel orders-panel">
                <PanelTitle
                  title="Order & execution history"
                  detail="Most recent 200 orders"
                />
                <div className="table-scroll">
                  <table>
                    <thead>
                      <tr>
                        <th>Instrument</th>
                        <th>Side</th>
                        <th>Filled</th>
                        <th>Average price</th>
                        <th>Fees</th>
                        <th>Status</th>
                      </tr>
                    </thead>
                    <tbody>
                      {orders.map((o) => (
                        <tr key={o.id}>
                          <td>{o.instrument}</td>
                          <td className={o.side === "BUY" ? "green" : "red"}>
                            {o.side}
                          </td>
                          <td>{lots(o.filled)}</td>
                          <td>{o.filled ? dollars(o.average_price) : "—"}</td>
                          <td>{money(o.fees)}</td>
                          <td>{o.status.replaceAll("_", " ")}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                  {!orders.length && <Empty text="No order history" />}
                </div>
              </section>
              <section className="panel orders-panel">
                <PanelTitle
                  title="Transaction journal"
                  detail="Most recent 200 balance entries"
                />
                <table>
                  <thead>
                    <tr>
                      <th>Asset</th>
                      <th>Available change</th>
                      <th>Reserved change</th>
                    </tr>
                  </thead>
                  <tbody>
                    {audit.map((a, i) => (
                      <tr key={i}>
                        <td>{a.asset}</td>
                        <td>
                          {fixed(
                            a.available_delta,
                            a.asset === "USD" ? 100 : 1000,
                          )}
                        </td>
                        <td>
                          {fixed(
                            a.reserved_delta,
                            a.asset === "USD" ? 100 : 1000,
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                {!audit.length && <Empty text="No transactions yet" />}
              </section>
            </>
          )}
          <footer>
            <span>◇ MatchCore · deterministic exchange simulation</span>
            <span>Simulated balances · no real assets</span>
          </footer>
        </main>
      </div>
    </div>
  );
}
function Stat({
  label,
  value,
  green = false,
}: {
  label: string;
  value: string;
  green?: boolean;
}) {
  return (
    <div className="stat">
      <small>{label}</small>
      <strong className={green ? "green" : ""}>{value}</strong>
    </div>
  );
}
function PanelTitle({ title, detail }: { title: string; detail: string }) {
  return (
    <div className="panel-title">
      <h2>{title}</h2>
      <span>{detail}</span>
    </div>
  );
}
function Empty({ text }: { text: string }) {
  return <div className="empty">{text}</div>;
}
function BookSide({
  levels,
  side,
}: {
  levels: { price: number; quantity: number }[];
  side: string;
}) {
  let sum = 0;
  const total = levels.reduce((a, v) => a + v.quantity, 0);
  return (
    <div className={"book-side " + side}>
      {levels.map((v) => {
        sum += v.quantity;
        return (
          <div className="table-row" key={v.price}>
            <div
              className="depth-bar"
              style={{
                width:
                  Math.max(2, (v.quantity * 100) / Math.max(1, total)) + "%",
              }}
            />
            <strong>{dollars(v.price)}</strong>
            <span>{lots(v.quantity)}</span>
            <span className="muted">{lots(sum)}</span>
          </div>
        );
      })}
    </div>
  );
}
createRoot(document.getElementById("root")!).render(<App />);
