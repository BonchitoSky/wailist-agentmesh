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
