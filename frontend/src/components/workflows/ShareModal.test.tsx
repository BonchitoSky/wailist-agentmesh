import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

// The dialog is tested against a stubbed API, the way WorkflowsPage is. What
// matters here is which calls it makes and what it says about them, not the
// network.
const state = vi.hoisted(() => ({ create: vi.fn(), listFor: vi.fn() }));

vi.mock("@/lib/api", () => ({
  shares: { create: state.create, listFor: state.listFor },
}));
vi.mock("@/hooks/useModalDismissal", () => ({ useModalDismissal: () => {} }));

import { ShareModal } from "./ShareModal";

const SHARE = {
  token: "tok-current",
  name: "Resume screener",
  graph: { nodes: [], edges: [] },
  nodeCount: 4,
  edgeCount: 3,
  importCount: 0,
  createdAt: new Date().toISOString(),
};

const REDACTIONS = {
  apiKeys: 2,
  secrets: 1,
  webhookSecrets: 1,
  uploadedFiles: 0,
  agentWallets: 0,
  emailAddresses: 0,
  connectedAccounts: 0,
  leasedMachines: 0,
};

beforeEach(() => {
  state.create.mockResolvedValue({ share: SHARE, redactions: REDACTIONS });
  state.listFor.mockResolvedValue([SHARE]);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("ShareModal", () => {
  it("asks for a link that matches the workflow as it is now", async () => {
    render(<ShareModal workflowId="wf-1" onClose={() => {}} />);
    await screen.findByText(/Not included:/);

    // reuseIfUnchanged, rather than the dialog picking a live link itself:
    // only the server holds the sanitised graph, so only it can tell whether
    // an existing link still describes this workflow. Choosing here is how a
    // link to an OLDER version used to get handed back.
    expect(state.create).toHaveBeenCalledWith("wf-1", 0, true);
  });

  it("says what was left out even when the link is one it already had", async () => {
    // The regression this exists for. The dialog used to reuse a live link by
    // itself, and that branch had no redaction counts to set -- so the
    // summary went silent at the one moment the sharer could still change
    // their mind about handing the link over.
    render(<ShareModal workflowId="wf-1" onClose={() => {}} />);

    const summary = await screen.findByText(/2 API keys/);
    expect(summary.textContent).toMatch(/webhook secret/);
  });

  it("keeps the link on offer out of the revoke list", async () => {
    state.listFor.mockResolvedValue([SHARE, { ...SHARE, token: "tok-older" }]);

    render(<ShareModal workflowId="wf-1" onClose={() => {}} />);
    await screen.findByText(/Not included:/);
    await screen.findByText(/Other live links/);

    // Two links exist, but the one shown above is not also something to
    // revoke underneath -- listed twice it reads as two different links.
    expect(screen.getAllByRole("button", { name: "Revoke" })).toHaveLength(1);
  });
});
