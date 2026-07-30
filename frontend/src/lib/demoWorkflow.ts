import { Workflow, WorkflowEdge, WorkflowNode } from "@/lib/types";

// Our own demo merchant (backend/cmd/x402demo/merchant) — built to Prism's
// exact request/response schema (task_description + files[] in, ranked
// candidates[] out) and wire dialect, but on TESTNET, so it actually
// settles today. Point NEXT_PUBLIC_X402_DEMO_MERCHANT_URL at a public
// HTTPS URL (tunnel or deployed) before running against a real deployed
// backend — AgentMesh's backend refuses to call localhost/private
// addresses (SSRF guard), so the bare localhost default only works when
// the backend itself also runs unsandboxed on the same machine.
//
// Prism's own real endpoint (https://prism-99h2.onrender.com/resume-screen-accurate)
// is mainnet-only (its 402 challenge quotes mainnet USDC, asset 31566704)
// and can't be used from testnet at all — swap targetURL for it once the
// backend is switched to mainnet and its platform wallets are funded (see
// cmd/server/main.go's usdcAssetID/relayNetwork switch).
const DEFAULT_MERCHANT_URL =
  process.env.NEXT_PUBLIC_X402_DEMO_MERCHANT_URL ??
  "http://localhost:4021/resume-screen";

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
      name: "x402 Resume Screener (demo, Prism-schema)",
      description:
        'Same request/response schema as Prism (a real, live x402-global-challenge entry we\'re collaborating with) but on testnet, via our own demo merchant. task_description + files[] in, ranked candidates[] out. No AI agent in this workflow: this node pays and calls the endpoint directly, settled on-chain. Paste something like this into the run prompt: {"task_description":"Senior React Frontend Developer","files":[{"filename":"resume.txt","text":"6 years React, TypeScript, Next.js"}]}',
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
