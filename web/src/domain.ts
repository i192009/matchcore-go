export type Level = { price: number; quantity: number };
export type Depth = {
  instrument: string;
  sequence: number;
  bids: Level[];
  asks: Level[];
};
export type Order = {
  id: string;
  client_id: string;
  instrument: string;
  side: string;
  type: string;
  price: number;
  quantity: number;
  remaining: number;
  filled: number;
  fees: number;
  status: string;
  reserved: number;
  average_price: number;
  created: number;
};
export type Trade = {
  id: string;
  price: number;
  quantity: number;
  timestamp: number;
};
export type Balance = {
  asset: string;
  available: number;
  reserved: number;
  total: number;
};
export type Delta = {
  sequence: number;
  instrument: string;
  levels: { side: string; price: number; delta: number }[];
  trades: Trade[];
  orders: Order[];
};
export function mergeDepth(book: Depth, event: Delta): Depth {
  if (event.instrument !== book.instrument || event.sequence <= book.sequence)
    return book;
  if (event.sequence !== book.sequence + 1)
    throw new Error("Sequence gap: recovering snapshot");
  const bids = new Map(book.bids.map((v) => [v.price, v.quantity]));
  const asks = new Map(book.asks.map((v) => [v.price, v.quantity]));
  for (const level of event.levels) {
    const side = level.side === "BUY" ? bids : asks;
    const quantity = (side.get(level.price) ?? 0) + level.delta;
    if (quantity < 0) throw new Error("Invalid depth: recovering snapshot");
    if (quantity === 0) side.delete(level.price);
    else side.set(level.price, quantity);
  }
  return {
    ...book,
    sequence: event.sequence,
    bids: [...bids]
      .map(([price, quantity]) => ({ price, quantity }))
      .sort((a, b) => b.price - a.price),
    asks: [...asks]
      .map(([price, quantity]) => ({ price, quantity }))
      .sort((a, b) => a.price - b.price),
  };
}
export function decimalToUnits(value: string, scale: number): number {
  if (!/^\d+(\.\d+)?$/.test(value))
    throw new Error("Enter a positive decimal number");
  const [whole, frac = ""] = value.split(".");
  const digits = String(scale).length - 1;
  if (frac.length > digits) throw new Error(`Use at most ${digits} decimals`);
  const units =
    BigInt(whole) * BigInt(scale) + BigInt(frac.padEnd(digits, "0"));
  if (units > BigInt(Number.MAX_SAFE_INTEGER))
    throw new Error("Amount too large");
  return Number(units);
}
export function fixed(
  units: number | bigint,
  scale: number,
  digits?: number,
): string {
  const n = BigInt(units);
  const sign = n < 0n ? "-" : "";
  const v = n < 0n ? -n : n;
  const decimals = String(scale).length - 1;
  const frac = (v % BigInt(scale)).toString().padStart(decimals, "0");
  return (
    sign +
    (v / BigInt(scale)).toLocaleString("en-US") +
    (decimals
      ? "." + (digits === undefined ? frac : frac.slice(0, digits))
      : "")
  );
}
export const dollars = (ticks: number) => "$" + fixed(BigInt(ticks) * 10n, 1);
export const money = (cents: number | bigint) => "$" + fixed(cents, 100);
export const lots = (quantity: number) => fixed(quantity, 1000);
export function newClientID(): string {
  return [...crypto.getRandomValues(new Uint8Array(16))]
    .map((v) => v.toString(16).padStart(2, "0"))
    .join("");
}
