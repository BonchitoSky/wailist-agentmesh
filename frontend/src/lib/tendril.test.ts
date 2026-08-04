import { describe, expect, it } from "vitest";
import { estimateLeaseCostUSD } from "./tendril";

describe("estimateLeaseCostUSD", () => {
  // Mirrors the backend's RequiredCreditAtomic: hourly rate x hours, plus the
  // flat $0.01 gate fee Tendril charges to open a lease.
  it("adds the flat rent gate fee to the metered hours", () => {
    expect(estimateLeaseCostUSD(6, 2)).toBeCloseTo(12.01, 6);
    expect(estimateLeaseCostUSD(6, 1)).toBeCloseTo(6.01, 6);
    expect(estimateLeaseCostUSD(1.5, 0.5)).toBeCloseTo(0.76, 6);
  });

  it("returns the bare gate fee for zero hours rather than NaN", () => {
    expect(estimateLeaseCostUSD(6, 0)).toBeCloseTo(0.01, 6);
  });
});
