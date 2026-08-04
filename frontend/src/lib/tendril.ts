import { BASE } from "@/lib/api";

// Tendril charges a flat 0.01 USDC to open a lease; the hours themselves meter
// against credit at the machine's hourly rate. Kept in sync with the backend's
// tendrilRentGateFeeAtomic.
export const TENDRIL_RENT_GATE_FEE_USD = 0.01;

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
};
