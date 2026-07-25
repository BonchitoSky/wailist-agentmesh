import { describe, expect, it } from "vitest";
import {
  BONUS_RATE,
  BONUS_THRESHOLD_INR,
  bonusRate,
  bonusUSD,
  creditsForTopup,
  GST_RATE,
  gstBreakdown,
  inrToCreditsUSD,
  USD_PER_INR,
} from "./fx";

describe("inrToCreditsUSD", () => {
  it("converts INR to USD at the fixed mock rate", () => {
    expect(inrToCreditsUSD(83)).toBeCloseTo(1, 10);
    expect(inrToCreditsUSD(830)).toBeCloseTo(10, 10);
    expect(inrToCreditsUSD(0)).toBe(0);
  });

  it("is a straight linear scale of USD_PER_INR", () => {
    expect(inrToCreditsUSD(1000)).toBeCloseTo(1000 * USD_PER_INR, 10);
  });
});

describe("bonusRate", () => {
  it("is 0 just below the threshold", () => {
    expect(bonusRate(BONUS_THRESHOLD_INR - 1)).toBe(0);
    expect(bonusRate(999)).toBe(0);
  });

  it("applies at exactly the threshold", () => {
    expect(bonusRate(BONUS_THRESHOLD_INR)).toBe(BONUS_RATE);
    expect(bonusRate(1000)).toBe(0.05);
  });

  it("applies above the threshold", () => {
    expect(bonusRate(5000)).toBe(BONUS_RATE);
  });

  it("is 0 for 0", () => {
    expect(bonusRate(0)).toBe(0);
  });
});

describe("bonusUSD", () => {
  it("is 0 below the threshold", () => {
    expect(bonusUSD(999)).toBe(0);
  });

  it("is base credits times the bonus rate at/above the threshold", () => {
    const amount = 1000;
    expect(bonusUSD(amount)).toBeCloseTo(
      inrToCreditsUSD(amount) * BONUS_RATE,
      10,
    );
  });
});

describe("creditsForTopup", () => {
  it("equals base credits with no bonus below the threshold", () => {
    const amount = 500;
    expect(creditsForTopup(amount)).toBeCloseTo(inrToCreditsUSD(amount), 10);
  });

  it("equals base + bonus at/above the threshold", () => {
    const amount = 2000;
    expect(creditsForTopup(amount)).toBeCloseTo(
      inrToCreditsUSD(amount) + bonusUSD(amount),
      10,
    );
  });
});

describe("gstBreakdown", () => {
  it("splits a tax-inclusive total into base + GST that reconstruct the total", () => {
    for (const total of [0, 1, 118, 1000, 999999.99]) {
      const { base, gst } = gstBreakdown(total);
      expect(base + gst).toBeCloseTo(total, 6);
    }
  });

  it("computes GST at the stated rate of the base, not the total", () => {
    const total = 118;
    const { base, gst } = gstBreakdown(total);
    expect(gst).toBeCloseTo(base * GST_RATE, 6);
    // For an inclusive total with GST_RATE = 0.18, base should be total / 1.18.
    expect(base).toBeCloseTo(total / 1.18, 6);
  });

  it("returns 0/0 for a 0 total", () => {
    const { base, gst } = gstBreakdown(0);
    expect(base).toBe(0);
    expect(gst).toBe(0);
  });
});
