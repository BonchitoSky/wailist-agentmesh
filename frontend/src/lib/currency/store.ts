"use client";
import { useCallback, useEffect, useSyncExternalStore } from "react";
import { useAuth } from "@/hooks/useAuth";
import { fx } from "@/lib/api";
import {
  DEFAULT_CURRENCY,
  formatMoney,
  isDefaultCurrency,
  isSupportedCurrency,
  type Rates,
} from "@/lib/currency/format";

// Rate table shared across every component on the page, fetched at most once.
//
// The whole module is inert while the user is on USD: `ensureRates` is never
// called, so there is no request, no listener churn, and no new failure mode for
// the default experience. That is the §0 invariant in CURRENCY_PLAN.md.

type Status = "idle" | "loading" | "ready" | "failed";

let rates: Rates | null = null;
let status: Status = "idle";
const listeners = new Set<() => void>();

// One stable object per state change: useSyncExternalStore compares snapshots by
// identity, so returning a fresh literal each call would loop forever.
let snapshot: { rates: Rates | null; status: Status } = { rates, status };
const SERVER_SNAPSHOT: { rates: Rates | null; status: Status } = {
  rates: null,
  status: "idle",
};

function commit(next: { rates: Rates | null; status: Status }): void {
  rates = next.rates;
  status = next.status;
  snapshot = next;
  listeners.forEach((l) => l());
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

const getSnapshot = () => snapshot;
const getServerSnapshot = () => SERVER_SNAPSHOT;

function ensureRates(): void {
  if (status !== "idle") return;
  commit({ rates, status: "loading" });
  fx.rates()
    .then((table) => commit({ rates: table.rates, status: "ready" }))
    // Leaves rates null, which makes formatMoney fall back to USD. Showing a
    // figure derived from a rate we could not fetch would be worse than showing
    // the underlying one.
    .catch(() => commit({ rates: null, status: "failed" }));
}

// Last-known currency, mirrored so a returning non-USD user's first paint is
// already correct instead of flashing USD while /auth/me resolves. Same
// technique lib/credits/store.ts uses for its own state.
const CURRENCY_KEY = "agentmesh_currency_v1";

function readCachedCurrency(): string | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(CURRENCY_KEY);
    return raw && isSupportedCurrency(raw) ? raw : null;
  } catch {
    return null;
  }
}

function cacheCurrency(code: string): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(CURRENCY_KEY, code);
  } catch {
    /* private mode / quota — the authoritative value still comes from the API */
  }
}

export interface CurrencyView {
  /** The active display currency. `USD` means render exactly as before. */
  currency: string;
  /** True while the user is on the default and nothing should change. */
  isDefault: boolean;
  /** Rates could not be fetched; amounts are falling back to USD. */
  ratesFailed: boolean;
  /** Format a USD amount for display. USD input renders byte-identically. */
  format: (usd: number) => string;
}

export function useCurrency(): CurrencyView {
  const { user } = useAuth();
  const snap = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);

  // /auth/me is authoritative; the cached value only covers the gap before it
  // lands. Both fall back to USD, so an unauthenticated or still-loading render
  // is the default experience rather than a guess.
  const currency =
    user?.displayCurrency ?? readCachedCurrency() ?? DEFAULT_CURRENCY;

  useEffect(() => {
    if (isDefaultCurrency(currency)) return;
    cacheCurrency(currency);
    ensureRates();
  }, [currency]);

  const format = useCallback(
    (usd: number) => formatMoney(usd, currency, snap.rates),
    [currency, snap.rates],
  );

  return {
    currency,
    isDefault: isDefaultCurrency(currency),
    ratesFailed: !isDefaultCurrency(currency) && snap.status === "failed",
    format,
  };
}

/** Test-only: drop the module-level rate cache between cases. */
export function __resetCurrencyStoreForTest(): void {
  commit({ rates: null, status: "idle" });
}
