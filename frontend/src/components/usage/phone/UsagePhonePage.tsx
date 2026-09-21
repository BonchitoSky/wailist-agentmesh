"use client";
import { useMemo, useState } from "react";
import Link from "next/link";
import { Skeleton } from "@/components/ui/Skeleton";
import { PullToRefresh } from "@/components/PullToRefresh";
import { ExternalLink } from "@/components/ExternalLink";
import { workflowHref } from "@/lib/routes";
import type { UsageCategory, UsagePayload, UsageRange } from "@/lib/types";
import { AreaChart } from "../AreaChart";
import { Donut } from "../Donut";
import {
  ALGO_USD,
  CAT_COLOR,
  CAT_LABEL,
  TYPE_PILL,
  relTime,
  usd,
} from "../format";

// Usage on a phone.
//
// The desktop page is built around two wide tables -- endpoints at 984px and
// settlements at 720px -- which on a phone scroll sideways inside a card a
// third that wide. Here each table becomes a short list of rows with the one
// figure that matters on the right, and "See all" where there is more.
//
// Same data and the same single fetch as the desktop page: UsagePage owns the
// state and hands it down, so a range switch or a retry behaves identically.

const RANGES: UsageRange[] = ["24h", "7d", "30d"];
const CATS: UsageCategory[] = ["x402", "llm", "action"];
// How many rows a list shows before "See all".
const TOP = 5;

const count = new Intl.NumberFormat("en");

export interface UsagePhoneProps {
  range: UsageRange;
  onRange: (r: UsageRange) => void;
  data: UsagePayload | null;
  loading: boolean;
  error: Error | null;
  onRetry: () => void;
}

export function UsagePhonePage(p: UsagePhoneProps) {
  const [allEndpoints, setAllEndpoints] = useState(false);

  const body = !p.data ? (
    p.loading ? (
      <UsageSkeleton />
    ) : (
      <p className="bilp-note" data-tone="error" role="alert">
        Couldn&rsquo;t load usage.{" "}
        <button type="button" className="bilp-link" onClick={p.onRetry}>
          Retry
        </button>
      </p>
    )
  ) : (
    <UsageBody
      data={p.data}
      range={p.range}
      allEndpoints={allEndpoints}
      onAllEndpoints={() => setAllEndpoints(true)}
    />
  );

  return (
    <PullToRefresh
      onRefresh={async () => p.onRetry()}
      style={{ flex: 1, minHeight: 0, background: "var(--bg)" }}
    >
      <main className="bilp-page usgp-page" data-loading={p.loading}>
        <h1 className="bilp-title">Usage</h1>
        <p className="bilp-sub">What your agents spent, and on what.</p>

        <div className="bilp-seg usgp-seg" role="radiogroup" aria-label="Range">
          {RANGES.map((r) => (
            <button
              key={r}
              type="button"
              role="radio"
              aria-checked={p.range === r}
              className="bilp-seg__item"
              onClick={() => p.range !== r && p.onRange(r)}
            >
              {r}
            </button>
          ))}
        </div>

        {/* A failed refresh keeps the last good figures on screen. */}
        {p.data && p.error && (
          <p className="bilp-note" data-tone="error" role="alert">
            Couldn&rsquo;t refresh; showing the last loaded figures.{" "}
            <button type="button" className="bilp-link" onClick={p.onRetry}>
              Retry
            </button>
          </p>
        )}

        {body}
      </main>
    </PullToRefresh>
  );
}

function UsageBody({
  data,
  range,
  allEndpoints,
  onAllEndpoints,
}: {
  data: UsagePayload;
  range: UsageRange;
  allEndpoints: boolean;
  onAllEndpoints: () => void;
}) {
  const { summary, timeseries, byWorkflow, byEndpoint, settlements } = data;

  // The same category split the desktop donut draws, from endpoint totals.
  const cats = useMemo(() => {
    const t: Record<UsageCategory, number> = { x402: 0, llm: 0, action: 0 };
    for (const e of byEndpoint) t[e.type] += e.totalAlgo;
    return t;
  }, [byEndpoint]);
  const spent = cats.x402 + cats.llm + cats.action;
  const calls = byEndpoint.reduce((n, e) => n + e.calls, 0);
  const delta = summary.deltas.totalAlgoPct;

  const names = useMemo(
    () => new Map(byWorkflow.map((w) => [w.workflowId, w.name])),
    [byWorkflow],
  );
  const workflows = [...byWorkflow].sort((a, b) => b.algo - a.algo);
  const endpoints = [...byEndpoint].sort((a, b) => b.totalAlgo - a.totalAlgo);
  const shownEndpoints = allEndpoints ? endpoints : endpoints.slice(0, TOP);

  if (spent === 0 && calls === 0 && settlements.length === 0) {
    return (
      <p className="bilp-note usgp-empty">
        No usage in the last {range}. Spend shows here once a workflow runs.
      </p>
    );
  }

  return (
    <>
      <section className="bilp-stats">
        <div className="bilp-stat">
          <span className="bilp-stat__label">Spent · {range}</span>
          <span className="bilp-stat__row">
            <span className="bilp-stat__value">${usd(spent)}</span>
            {delta !== 0 && (
              <span className="usgp-delta" data-up={delta > 0}>
                {delta > 0 ? "▲" : "▼"} {Math.abs(delta)}%
              </span>
            )}
          </span>
        </div>
        <div className="bilp-stat">
          <span className="bilp-stat__label">Calls · {range}</span>
          <span className="bilp-stat__value">{count.format(calls)}</span>
        </div>
      </section>

      <section className="bilp-section">
        <h2 className="bilp-heading">Spend over time</h2>
        <div className="usgp-legend">
          <span>
            <i style={{ background: "var(--accent)" }} /> Spend (USD)
          </span>
          <span>
            <i style={{ background: "var(--warm)" }} /> Calls
          </span>
        </div>
        <div className="usgp-chart">
          <AreaChart data={timeseries} algoUsd={ALGO_USD} />
        </div>
      </section>

      <section className="bilp-section">
        <h2 className="bilp-heading">By category</h2>
        <div className="usgp-cats">
          <div className="usgp-donut">
            <Donut
              size={120}
              thickness={16}
              segments={CATS.map((k) => ({
                label: CAT_LABEL[k],
                value: cats[k],
                color: CAT_COLOR[k],
              }))}
              centerLabel={`$${usd(spent)}`}
              centerSub={range}
            />
          </div>
          <ul className="usgp-list">
            {CATS.map((k) => (
              <li key={k} className="usgp-row">
                <span className="usgp-row__main usgp-row__main--inline">
                  <i
                    className="usgp-dot"
                    style={{ background: CAT_COLOR[k] }}
                  />
                  {CAT_LABEL[k]}
                  {k === "llm" && "*"}
                </span>
                <span className="usgp-row__fig">
                  ${usd(cats[k])}
                  <small>
                    {spent > 0 ? Math.round((cats[k] / spent) * 100) : 0}%
                  </small>
                </span>
              </li>
            ))}
          </ul>
        </div>
      </section>

      {workflows.length > 0 && (
        <section className="bilp-section">
          <h2 className="bilp-heading">Workflows by spend</h2>
          <ul className="usgp-list">
            {workflows.slice(0, TOP).map((w) => (
              <li key={w.workflowId}>
                <Link href={workflowHref(w.workflowId)} className="usgp-row">
                  <span className="usgp-row__main">
                    <span className="usgp-row__name">{w.name}</span>
                    <span className="usgp-row__sub">
                      {count.format(w.calls)} calls
                    </span>
                  </span>
                  <span className="usgp-row__fig">${usd(w.algo)}</span>
                </Link>
              </li>
            ))}
          </ul>
        </section>
      )}

      {endpoints.length > 0 && (
        <section className="bilp-section">
          <h2 className="bilp-heading">Endpoints</h2>
          <ul className="usgp-list">
            {shownEndpoints.map((e) => (
              <li key={`${e.host}${e.endpoint}`} className="usgp-row">
                <span className="usgp-row__main">
                  <span className="usgp-row__name">{e.endpoint}</span>
                  <span className="usgp-row__sub">
                    <span
                      className="usgp-pill"
                      style={{ color: TYPE_PILL[e.type] }}
                    >
                      {CAT_LABEL[e.type]}
                    </span>
                    {e.provider} · {count.format(e.calls)} calls
                  </span>
                </span>
                <span className="usgp-row__fig">${usd(e.totalAlgo)}</span>
              </li>
            ))}
          </ul>
          {!allEndpoints && endpoints.length > TOP && (
            <button
              type="button"
              className="bilp-link bilp-link--block"
              onClick={onAllEndpoints}
            >
              See all endpoints ({endpoints.length})
            </button>
          )}
        </section>
      )}

      {settlements.length > 0 && (
        <section className="bilp-section">
          <h2 className="bilp-heading">Recent settlements</h2>
          <ul className="usgp-list">
            {settlements.slice(0, TOP).map((s) => (
              <li key={s.txId} className="usgp-row">
                <span className="usgp-row__main">
                  <span className="usgp-row__name">{s.endpoint}</span>
                  <span className="usgp-row__sub">
                    {names.get(s.workflowId) ?? "—"} · {relTime(s.ts)} ·{" "}
                    <ExternalLink
                      href={s.explorerURL}
                      style={{ color: "var(--accent)" }}
                    >
                      tx
                    </ExternalLink>
                  </span>
                </span>
                <span className="usgp-row__fig">${usd(s.amountAlgo, 4)}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      <p className="bilp-note usgp-foot">
        Settled on Algorand testnet. LLM prices (*) are estimated.
      </p>
    </>
  );
}

function UsageSkeleton() {
  return (
    <div className="usgp-skeleton" aria-busy="true" aria-label="Loading usage">
      <Skeleton height={56} />
      <Skeleton height={150} />
      <Skeleton height={120} />
    </div>
  );
}
