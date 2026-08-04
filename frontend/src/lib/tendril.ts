import { BASE } from "@/lib/api";

// Tendril charges a flat 0.01 USDC to open a lease; the hours themselves meter
// against credit at the machine's hourly rate. Kept in sync with the backend's
// tendrilRentGateFeeAtomic.
export const TENDRIL_RENT_GATE_FEE_USD = 0.01;

// The fixed name every "Load Tendril workflow" click creates a fresh row
// under — same pattern the other demo buttons (x402, CANIX402) use for their
// own fixed names. WorkflowRoute matches on this name to open the direct
// Tendril console instead of the canvas editor: there is no node graph to
// show, so any workflow row with this exact name is a shortcut into the
// console, not a distinct workflow definition.
export const TENDRIL_WORKFLOW_NAME = "Tendril — Rent a Machine";

export interface TendrilMachine {
  id: string;
  label: string;
  cpuCores: number;
  ramMb: number;
  gpu: string | null;
  pricePerHourUsd: number;
}

export function estimateLeaseCostUSD(
  pricePerHourUsd: number,
  hours: number,
): number {
  return pricePerHourUsd * hours + TENDRIL_RENT_GATE_FEE_USD;
}

export interface TendrilLeaseSummary {
  id: string;
  tendrilNodeId: string;
  tendrilNodeLabel: string;
  sshHost: string;
  sshPort: number;
  sshUsername: string;
  sshCommand: string;
  rateUsdMicrosPerHour: number;
  status: string;
  startedAt: string;
  // fundedUntil is NOT the renter's own reservation window — it's how long
  // Tendril's shared pool wallet (every AgentMesh user's topups combined)
  // could fund this machine at its rate, which is almost always far more
  // than any one user asked for (confirmed live: a 1-hour rent showed a
  // ~2-hour fundedUntil). Use hoursPurchased for anything shown to the user
  // as "your window" — fundedUntil is a platform-side implementation detail.
  fundedUntil: string;
  hoursPurchased: number;
  // What renting this lease reserved from the user's Tendril credit —
  // gate fee plus the requested hours at this machine's rate. Releasing
  // early refunds whatever of this Tendril never actually billed.
  reservedUsdMicros: number;
}

// What releasing a lease actually settled: real backend fields (usedSeconds,
// charged, refunded), never the shared pool balance — see leases.go's
// ReleaseLease handler doc comment for why that stays server-side only.
export interface TendrilReleaseResult {
  usedSeconds: number;
  charged: number;
  refunded: number;
}

// What a topup actually settled — txId/explorerURL is the real on-chain leg
// (Wallet 1 -> Wallet 2); outboundTxId/outboundExplorerURL is the second
// leg (Wallet 2 -> Tendril's payTo) when the relay's response carried one.
// executeTendrilTopup (backend) only sets whichever of these it actually got
// back, so all four are optional.
export interface TendrilTopupResult {
  toppedUp: string;
  tendrilCreditBalance: string;
  previousBalance: string;
  txId?: string;
  explorerURL?: string;
  outboundTxId?: string;
  outboundExplorerURL?: string;
}

async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error ?? `${path}: ${res.status}`);
  return data as T;
}

export const tendril = {
  async machines(): Promise<TendrilMachine[]> {
    const res = await fetch(`${BASE}/tendril/machines`, {
      credentials: "include",
    });
    if (!res.ok) throw new Error(`machines: ${res.status}`);
    const body = (await res.json()) as { machines: TendrilMachine[] };
    return body.machines ?? [];
  },

  async credit(): Promise<number> {
    const res = await fetch(`${BASE}/tendril/credits`, {
      credentials: "include",
    });
    if (!res.ok) throw new Error(`credits: ${res.status}`);
    const body = (await res.json()) as { tendrilCreditUsdMicros: number };
    return body.tendrilCreditUsdMicros / 1e6;
  },

  async leases(): Promise<TendrilLeaseSummary[]> {
    const res = await fetch(`${BASE}/leases`, { credentials: "include" });
    if (!res.ok) throw new Error(`leases: ${res.status}`);
    const body = (await res.json()) as { leases: TendrilLeaseSummary[] };
    return body.leases ?? [];
  },

  // Direct actions — each is its own call, with no chaining and no
  // "re-buy credit to run a job" side effect: buying credit, renting, and
  // running are three independent button presses, not steps of one pipeline.
  topup(amountUsd: string): Promise<TendrilTopupResult> {
    return postJSON("/tendril/topup", { amountUsd });
  },
  rent(nodeId: string, hours: string): Promise<Record<string, unknown>> {
    return postJSON("/tendril/rent", { nodeId, hours });
  },
  run(payload: string): Promise<Record<string, unknown>> {
    return postJSON("/tendril/run", { payload });
  },
  release(leaseId: string): Promise<TendrilReleaseResult> {
    return postJSON(`/leases/${leaseId}/release`, {});
  },
};
