import { describe, expect, it } from "vitest";
import { buildCreditCart, computeTotals } from "./mockData";

describe("buildCreditCart", () => {
  it("builds a single credit-topup line priced at the chosen amount", () => {
    const items = buildCreditCart(1500);
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({
      id: "credit-topup",
      unitPrice: 1500,
      quantity: 1,
    });
  });

  it("reflects a 0 amount as-is (caller decides whether that's payable)", () => {
    const items = buildCreditCart(0);
    expect(items[0].unitPrice).toBe(0);
  });
});

describe("computeTotals", () => {
  it("sums unitPrice * quantity across multiple lines", () => {
    const totals = computeTotals([
      { id: "a", title: "A", detail: "", unitPrice: 100, quantity: 2 },
      { id: "b", title: "B", detail: "", unitPrice: 50, quantity: 3 },
    ]);
    // 100*2 + 50*3 = 350
    expect(totals.subtotal).toBe(350);
    expect(totals.total).toBe(350);
  });

  it("total equals subtotal (no shipping/discount lines for digital credits)", () => {
    const totals = computeTotals([
      { id: "a", title: "A", detail: "", unitPrice: 999, quantity: 1 },
    ]);
    expect(totals.total).toBe(totals.subtotal);
  });

  it("returns 0/0 for an empty cart", () => {
    const totals = computeTotals([]);
    expect(totals.subtotal).toBe(0);
    expect(totals.total).toBe(0);
  });
});
