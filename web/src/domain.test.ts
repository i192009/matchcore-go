import { describe, it, expect } from "vitest";
import {
  mergeDepth,
  decimalToUnits,
  fixed,
  type Depth,
  type Delta,
} from "./domain";
describe("committed market stream", () => {
  const book: Depth = {
    instrument: "BTC-USD",
    sequence: 4,
    bids: [{ price: 10, quantity: 5 }],
    asks: [{ price: 20, quantity: 8 }],
  };
  const delta: Delta = {
    instrument: "BTC-USD",
    sequence: 5,
    levels: [{ side: "SELL", price: 20, delta: -3 }],
    trades: [],
    orders: [],
  };
  it("applies contiguous deltas and ignores repeated delivery", () => {
    const next = mergeDepth(book, delta);
    expect(next.asks[0].quantity).toBe(5);
    expect(mergeDepth(next, delta)).toBe(next);
    expect(book.asks[0].quantity).toBe(8);
  });
  it("requires resynchronization after missing events", () =>
    expect(() => mergeDepth(book, { ...delta, sequence: 6 })).toThrow(
      "Sequence gap",
    ));
  it("rejects negative aggregate depth", () =>
    expect(() =>
      mergeDepth(book, {
        ...delta,
        levels: [{ side: "SELL", price: 20, delta: -9 }],
      }),
    ).toThrow("Invalid depth"));
});
describe("exact display and input", () => {
  it("parses fixed units without float arithmetic", () => {
    expect(decimalToUnits("1.001", 1000)).toBe(1001);
    expect(() => decimalToUnits("0.0001", 1000)).toThrow();
    expect(fixed(123456n, 100)).toBe("1,234.56");
  });
});
