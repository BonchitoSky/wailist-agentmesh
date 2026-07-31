"use client";
import { useSyncExternalStore } from "react";
import type { PaymentMethod } from "@/components/checkout/types";
import type { AutoRecharge, CreditsState, Purchase } from "@/lib/credits/types";
import { creditsForTopup } from "@/lib/credits/fx";
import { credits as creditsApi, hasBackend } from "@/lib/api";

// Credit wallet shared across routes via useSyncExternalStore.
//
// Real mode (a backend is configured): balance and purchase history come from
// the DB through the API; only auto-recharge — a client-side preference with no
// backend — is kept in localStorage.
//
// Mock mode (no backend): the whole wallet is localStorage-backed, per browser.

const STORAGE_KEY = "agentmesh_credits_v1";

const DEFAULT_AUTO_RECHARGE: AutoRecharge = {
  enabled: false,
  thresholdUSD: 5,
  amountINR: 1000,
  monthlyCapINR: null,
};

const DEFAULT_STATE: CreditsState = {
  balanceUSD: 0,
  purchases: [],
  autoRecharge: DEFAULT_AUTO_RECHARGE,
};

let state: CreditsState = DEFAULT_STATE;
let loaded = false; // ensureLoaded has run
let ready = false; // data is populated — drives `hydrated` for consumers
let fetching = false; // a backend refresh is in flight
let loadError: string | null = null;
const listeners = new Set<() => void>();

function notify(): void {
  listeners.forEach((l) => l());
}

// Read persisted state, tolerating missing/corrupt data (best-effort).
function loadPersisted(): CreditsState {
  if (typeof window === "undefined") return DEFAULT_STATE;
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return DEFAULT_STATE;
    const parsed = JSON.parse(raw) as Partial<CreditsState>;
    return {
      balanceUSD: typeof parsed.balanceUSD === "number" ? parsed.balanceUSD : 0,
      purchases: Array.isArray(parsed.purchases) ? parsed.purchases : [],
      autoRecharge: {
        ...DEFAULT_AUTO_RECHARGE,
        ...(parsed.autoRecharge ?? {}),
      },
    };
  } catch {
    return DEFAULT_STATE;
  }
}

// Pull the real balance + history from the DB. Auto-recharge is left untouched
// (it's a local preference). `ready` flips true once this settles so consumers
// don't flash a stale/empty wallet.
async function refreshFromBackend(): Promise<void> {
  if (fetching) return;
  fetching = true;
  try {
    const [balanceUSD, purchases] = await Promise.all([
      creditsApi.balance(),
      creditsApi.history(),
    ]);
    state = { ...state, balanceUSD, purchases };
    loadError = null;
  } catch (e) {
    // Surface, don't swallow — a failed load must not masquerade as $0.
    loadError = e instanceof Error ? e.message : "failed to load credits";
  } finally {
    fetching = false;
    ready = true;
    notify();
  }
}

// Hydrate lazily on the first client snapshot so the server render (defaults)
// and the initial client render match, then re-render with real values.
function ensureLoaded(): void {
  if (loaded || typeof window === "undefined") return;
  loaded = true;
  if (hasBackend) {
    // Real mode: seed the local preference, fetch the rest from the DB.
    state = { ...DEFAULT_STATE, autoRecharge: loadPersisted().autoRecharge };
    void refreshFromBackend();
  } else {
    state = loadPersisted();
    ready = true;
  }
}

function persist(): void {
  if (typeof window === "undefined") return;
  try {
    // In real mode only the auto-recharge preference is local; balance/history
    // live in the DB and must never be written back to localStorage.
    const toStore = hasBackend ? { autoRecharge: state.autoRecharge } : state;
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(toStore));
  } catch {
    /* ignore storage quota/availability errors */
  }
}

function commit(next: CreditsState): void {
  state = next;
  persist();
  notify();
}

function subscribe(onChange: () => void): () => void {
  listeners.add(onChange);
  return () => {
    listeners.delete(onChange);
  };
}

function getSnapshot(): CreditsState {
  ensureLoaded();
  return state;
}

function getServerSnapshot(): CreditsState {
  return DEFAULT_STATE;
}

function newId(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  return `txn_${Date.now()}_${Math.floor(Math.random() * 1e6)}`;
}

// Record a successful top-up.
//
// Real mode: the DB was already credited server-side by payment verification,
// so this triggers a refresh from the backend (the source of truth) rather than
// mutating a local balance. It still returns a display-only Purchase so the
// checkout success screen can show the credited amount immediately.
//
// Mock mode: grants credits locally and prepends the purchase to history.
export function addPurchase(input: {
  amountINR: number;
  method: PaymentMethod;
  creditsUSDOverride?: number;
}): Purchase {
  const creditsUSD =
    input.creditsUSDOverride ?? creditsForTopup(input.amountINR);
  const purchase: Purchase = {
    id: newId(),
    createdAt: new Date().toISOString(),
    amountINR: input.amountINR,
    creditsUSD,
    method: input.method,
    status: "paid",
  };
  if (hasBackend) {
    void refreshFromBackend();
    return purchase;
  }
  commit({
    ...state,
    balanceUSD: state.balanceUSD + creditsUSD,
    purchases: [purchase, ...state.purchases],
  });
  return purchase;
}

export function setAutoRecharge(cfg: AutoRecharge): void {
  commit({ ...state, autoRecharge: cfg });
}

// Re-pull balance + history from the DB (no-op in mock mode). Call after any
// action that changes the server-side balance.
export function refreshCredits(): void {
  if (hasBackend) void refreshFromBackend();
}

export interface CreditsSnapshot extends CreditsState {
  hydrated: boolean;
  loadError: string | null;
  lastPurchase: Purchase | undefined;
  addPurchase: typeof addPurchase;
  setAutoRecharge: typeof setAutoRecharge;
  refresh: typeof refreshCredits;
}

export function useCredits(): CreditsSnapshot {
  const snapshot = useSyncExternalStore(
    subscribe,
    getSnapshot,
    getServerSnapshot,
  );
  const hydrated = useSyncExternalStore(
    subscribe,
    () => ready,
    () => false,
  );
  const err = useSyncExternalStore(
    subscribe,
    () => loadError,
    () => null,
  );
  return {
    ...snapshot,
    hydrated,
    loadError: err,
    lastPurchase: snapshot.purchases[0],
    addPurchase,
    setAutoRecharge,
    refresh: refreshCredits,
  };
}
