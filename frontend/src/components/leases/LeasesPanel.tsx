"use client";
import { useCallback, useEffect, useState } from "react";
import { BASE } from "@/lib/api";

interface TendrilLease {
  id: string;
  tendrilNodeId: string;
  tendrilNodeLabel: string;
  sshHost: string;
  sshPort: number;
  sshUsername: string;
  sshCommand: string;
  rateUsdMicrosPerHour: number;
  status: string;
  fundedUntil: string;
}

function formatCountdown(fundedUntil: string, now: number): string {
  const remainingMs = new Date(fundedUntil).getTime() - now;
  if (remainingMs <= 0) return "expired";
  const totalSeconds = Math.floor(remainingMs / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  return `${hours}h ${minutes}m ${seconds}s`;
}

export function LeasesPanel() {
  const [leases, setLeases] = useState<TendrilLease[]>([]);
  const [now, setNow] = useState(() => Date.now());

  const refresh = useCallback(async () => {
    if (!BASE) return;
    const res = await fetch(`${BASE}/leases`, { credentials: "include" });
    if (!res.ok) return;
    const body = (await res.json()) as { leases: TendrilLease[] };
    setLeases(body.leases ?? []);
  }, []);

  useEffect(() => {
    // Polling an external resource (the lease list) on mount and on an
    // interval — the "subscribe to an external system" case the rule's own
    // docs carve out.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    refresh();
    const poll = setInterval(refresh, 15_000);
    return () => clearInterval(poll);
  }, [refresh]);

  useEffect(() => {
    const tick = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(tick);
  }, []);

  const release = useCallback(
    async (id: string) => {
      if (!BASE) return;
      await fetch(`${BASE}/leases/${id}/release`, {
        method: "POST",
        credentials: "include",
      });
      refresh();
    },
    [refresh],
  );

  if (leases.length === 0) return null;

  return (
    <div
      style={{
        border: "1px solid var(--border)",
        borderRadius: 8,
        padding: 10,
        marginBottom: 10,
        display: "flex",
        flexDirection: "column",
        gap: 8,
      }}
    >
      <div style={{ fontSize: 12, fontWeight: 600, color: "var(--fg-dim)" }}>
        Active Tendril leases
      </div>
      {leases.map((lease) => (
        <div
          key={lease.id}
          style={{
            display: "flex",
            alignItems: "center",
            gap: 10,
            fontSize: 12,
            flexWrap: "wrap",
          }}
        >
          <span style={{ fontWeight: 600 }}>
            {lease.tendrilNodeLabel || lease.tendrilNodeId}
          </span>
          <span style={{ color: "var(--fg-dim)" }}>
            ${(lease.rateUsdMicrosPerHour / 1e6).toFixed(2)}/hr
          </span>
          <span style={{ color: "var(--fg-dim)" }}>
            {formatCountdown(lease.fundedUntil, now)}
          </span>
          <code
            style={{
              background: "var(--bg)",
              border: "1px solid var(--border)",
              padding: "2px 6px",
              borderRadius: 4,
              fontSize: 11,
              fontFamily: "var(--font-mono)",
            }}
          >
            {lease.sshCommand}
          </code>
          <button
            onClick={() => navigator.clipboard?.writeText(lease.sshCommand)}
            style={{ fontSize: 11 }}
          >
            copy
          </button>
          <a
            href={`${BASE}/leases/${lease.id}/key`}
            style={{ fontSize: 11 }}
          >
            download key
          </a>
          <button onClick={() => release(lease.id)} style={{ fontSize: 11 }}>
            release
          </button>
        </div>
      ))}
    </div>
  );
}
