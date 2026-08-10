"use client";
import { useCallback, useEffect, useSyncExternalStore } from "react";
import { useAuth } from "@/hooks/useAuth";
import { fx } from "@/lib/api";
import {
  DEFAULT_CURRENCY,
  formatBalance,
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
type Snapshot = {
  rates: Rates | null;
  status: Status;
  override: string | null;
};

let rates: Rates | null = null;
let status: Status = "idle";
// Set when the user changes currency in settings. useAuth caches the user it
// fetched at mount and has no refresh, so without this the new currency would
// not appear until a full page reload.
let override: string | null = null;
const listeners = new Set<() => void>();

// One stable object per state change: useSyncExternalStore compares snapshots by
// identity, so returning a fresh literal each call would loop forever.
let snapshot: Snapshot = { rates, status, override };
const SERVER_SNAPSHOT: Snapshot = {
  rates: null,
  status: "idle",
  override: null,
};

function commit(next: Snapshot): void {
  rates = next.rates;
  status = next.status;
  override = next.override;
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
  commit({ rates, status: "loading", override });
  fx.rates()
    .then((table) => commit({ rates: table.rates, status: "ready", override }))
    // Leaves rates null, which makes formatMoney fall back to USD. Showing a
    // figure derived from a rate we could not fetch would be worse than showing
    // the underlying one.
    .catch(() => commit({ rates: null, status: "failed", override }));
}

/**
 * Apply a currency the user just chose, without waiting for a reload.
 *
 * Call only after PATCH /settings has succeeded — this is a reflection of
 * persisted state, not a substitute for persisting it.
 */
export function applyDisplayCurrency(code: string): void {
  if (!isSupportedCurrency(code)) return;
  cacheCurrency(code);
  commit({ rates, status, override: code });
  if (!isDefaultCurrency(code)) ensureRates();
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
  /** Format a credit balance — credits lead in non-USD mode. See §3. */
  formatBalance: (usd: number) => string;
}

export function useCurrency(): CurrencyView {
  const { user } = useAuth();
  const snap = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);

  // /auth/me is authoritative; the cached value only covers the gap before it
  // lands. Both fall back to USD, so an unauthenticated or still-loading render
  // is the default experience rather than a guess.
  const currency =
    snap.override ??
    user?.displayCurrency ??
    readCachedCurrency() ??
    DEFAULT_CURRENCY;

  useEffect(() => {
    // Cached unconditionally, including USD: switching back to the default has
    // to clear a previously cached non-USD code, or the next first paint would
    // briefly render the currency the user just left.
    cacheCurrency(currency);
    if (isDefaultCurrency(currency)) return;
    ensureRates();
  }, [currency]);

  const format = useCallback(
    (usd: number) => formatMoney(usd, currency, snap.rates),
    [currency, snap.rates],
  );

  const balance = useCallback(
    (usd: number) => formatBalance(usd, currency, snap.rates),
    [currency, snap.rates],
  );

  return {
    currency,
    isDefault: isDefaultCurrency(currency),
    ratesFailed: !isDefaultCurrency(currency) && snap.status === "failed",
    format,
    formatBalance: balance,
  };
}

/** Test-only: drop the module-level rate cache between cases. */
export function __resetCurrencyStoreForTest(): void {
  commit({ rates: null, status: "idle", override: null });
}
