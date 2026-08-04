import { Workflow, WorkflowEdge, WorkflowNode } from "@/lib/types";

// Prism's real, live x402-global-challenge endpoint — mainnet only (its 402
// challenge quotes mainnet USDC, asset 31566704). Only reachable once the
// backend itself is running with ALGORAND_NETWORK=mainnet and funded
// PLATFORM_WALLET/PLATFORM_SPEND_WALLET (see cmd/server/main.go's
// usdcAssetID/relayNetwork switch) — on testnet this target will 502.
const DEFAULT_MERCHANT_URL =
  process.env.NEXT_PUBLIC_X402_DEMO_MERCHANT_URL ??
  "https://prism-99h2.onrender.com/resume-screen-accurate";

// Builds a workflow with NO agent/provider node at all: a trigger flows
// straight into a standalone tool402 node, which pays and calls the target
// directly (Wallet 1 -> Wallet 2 -> target, via the existing production
// relay path) — the simplest possible workflow that still produces a real
// on-chain x402 settlement, for testing on any account without configuring
// an LLM provider key first.
//
// Trigger is "chat" (not "manual") on purpose, even though nothing about
// this is conversational: it's the only existing run-time mechanism that
// prompts for free-text input before starting a run, and that typed text
// becomes this POST node's request body (RunContexter.Message()) — see the
// tool402 node's description for a paste-ready example.
export function buildX402DemoWorkflow(
  targetURL: string = DEFAULT_MERCHANT_URL,
): Pick<Workflow, "nodes" | "edges"> {
  const nodes: WorkflowNode[] = [
    {
      id: "n1",
      type: "trigger",
      template: "chat",
      x: 80,
      y: 220,
      label: "Paste request JSON, then run",
    },
    {
      id: "n2",
      type: "tool402",
      custom: true,
      x: 420,
      y: 220,
      name: "x402 Resume Screener (Prism, mainnet)",
      description:
        'Pays Prism\'s real, live x402-global-challenge endpoint directly (task_description + files[] in, ranked candidates[] out), settled on-chain via mainnet USDC. No AI agent in this workflow: this node pays and calls the endpoint directly. Paste something like this into the run prompt: {"task_description":"Senior React Frontend Developer","files":[{"filename":"resume.txt","text":"6 years React, TypeScript, Next.js"}]}',
      endpoint: targetURL,
      method: "POST",
      price: "0.05",
      unit: "call",
      priceLive: true,
    },
    { id: "n3", type: "end", template: "done", x: 760, y: 220 },
  ];
  const edges: WorkflowEdge[] = [
    { id: "e1", from: "n1", to: "n2", kind: "flow", toPort: "in" },
    { id: "e2", from: "n2", to: "n3", kind: "flow", toPort: "in" },
  ];
  return { nodes, edges };
}

// CANIX402 — a real, live x402-global-challenge Algorand DeFi API
// (canix402-api.compx.io, run by CompX). Confirmed live 2026-08-01: a real
// mainnet settlement, real DeFi yield data back, through this exact
// production relay path. Its /opportunities route needs no input at all
// (GET, no required query params), so "manual" is the right trigger here —
// unlike the Prism demo above, there's no free-text body to collect first.
const CANIX402_TARGET_URL = "https://canix402-api.compx.io/opportunities";

export function buildCanix402DemoWorkflow(): Pick<
  Workflow,
  "nodes" | "edges"
> {
  const nodes: WorkflowNode[] = [
    {
      id: "n1",
      type: "trigger",
      template: "manual",
      x: 80,
      y: 220,
      label: "Run",
    },
    {
      id: "n2",
      type: "tool402",
      custom: true,
      x: 420,
      y: 220,
      name: "CANIX402 DeFi Opportunities (mainnet)",
      description:
        "Pays CANIX402's real, live x402-global-challenge endpoint directly — top Algorand DeFi yield opportunities ranked by APY across Tinyman, Pact, Folks Finance, and more, settled on-chain via mainnet USDC ($0.01/call). No AI agent in this workflow: this node pays and calls the endpoint directly.",
      endpoint: CANIX402_TARGET_URL,
      method: "GET",
      price: "0.01",
      unit: "call",
      priceLive: true,
    },
    { id: "n3", type: "end", template: "done", x: 760, y: 220 },
  ];
  const edges: WorkflowEdge[] = [
    { id: "e1", from: "n1", to: "n2", kind: "flow", toPort: "in" },
    { id: "e2", from: "n2", to: "n3", kind: "flow", toPort: "in" },
  ];
  return { nodes, edges };
}

// Tendril — an official AgentMesh collaboration. This workflow has no agent
// and no LLM in it at all. It lays out the money flow left to right so the two
// balances are visible as separate steps rather than hidden inside one node:
//
//   trigger -> Topup -> Rent -> Run -> end
//              ^^^^^    ^^^^
//              buys     spends that credit on hours,
//              Tendril  and hands back an SSH command the
//              credit   console turns into a live terminal
//
// Topup is its own node precisely because it is a currency conversion, not a
// purchase: AgentMesh credits become Tendril credits, which only ever buy
// machine time. Delete the Topup node once you already hold enough credit.
export function buildTendrilWorkflow(
  hours: string = "1",
  topupUsd: string = "10",
): Pick<Workflow, "nodes" | "edges"> {
  const nodes: WorkflowNode[] = [
    {
      id: "n1",
      type: "trigger",
      template: "manual",
      x: 60,
      y: 240,
      label: "Run to rent a machine",
    },
    {
      id: "n2",
      type: "tendril",
      template: "tendril_topup",
      x: 340,
      y: 220,
      name: "Buy Tendril Credit",
      icon: "＄",
      tendrilAction: "topup",
      tendrilAmount: topupUsd,
      description:
        "Settles a real mainnet USDC payment into AgentMesh's Tendril pool and converts the same amount of your AgentMesh credits into Tendril credit. Tendril credit is yours alone and can only be spent on machine time.",
    },
    {
      id: "n3",
      type: "tendril",
      template: "tendril_rent",
      x: 640,
      y: 220,
      name: "Rent a Machine",
      icon: "▣",
      tendrilAction: "rent",
      tendrilHours: hours,
      description:
        "Reserves the hours from your Tendril credit, opens a metered lease on the cheapest online machine, and authorizes a freshly generated SSH key. The machine listed today is $6.00/hour. Release early and the unused hours return to your Tendril credit.",
    },
    {
      id: "n4",
      type: "tendril",
      template: "tendril_run",
      x: 940,
      y: 220,
      name: "Run a Job",
      icon: "▶",
      tendrilAction: "run",
      customParams: [
        { name: "payload", kind: "text", value: "print(sum(range(100)))" },
      ],
      description:
        "Executes Python inside the machine the Rent node just opened and returns its stdout. Flat 0.01 USDC per job.",
    },
    { id: "n5", type: "end", template: "done", x: 1240, y: 240 },
  ];
  const edges: WorkflowEdge[] = [
    { id: "e1", from: "n1", to: "n2", kind: "flow", toPort: "in" },
    { id: "e2", from: "n2", to: "n3", kind: "flow", toPort: "in" },
    { id: "e3", from: "n3", to: "n4", kind: "flow", toPort: "in" },
    { id: "e4", from: "n4", to: "n5", kind: "flow", toPort: "in" },
  ];
  return { nodes, edges };
}
