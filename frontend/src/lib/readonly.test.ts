import { describe, it, expect } from "vitest";
import { READ_ONLY, can, isWriteBlocked } from "./readonly";

describe("read-only capabilities", () => {
  it("ships in read-only mode", () => {
    expect(READ_ONLY).toBe(true);
  });

  it("withholds everything that authors a workflow", () => {
    expect(can("workflow.create")).toBe(false);
    expect(can("workflow.delete")).toBe(false);
    expect(can("workflow.editGraph")).toBe(false);
    expect(can("workflow.deploy")).toBe(false);
    expect(can("workflow.buildFromChat")).toBe(false);
  });

  it("keeps operating an existing workflow available", () => {
    expect(can("workflow.run")).toBe(true);
    expect(can("workflow.stop")).toBe(true);
    expect(can("workflow.chat")).toBe(true);
  });

  it("leaves the account alone", () => {
    expect(can("account.billing")).toBe(true);
    expect(can("account.settings")).toBe(true);
  });
});

describe("isWriteBlocked", () => {
  it("blocks the five graph-mutating endpoints", () => {
    expect(isWriteBlocked("POST", "/workflows")).toBe(true);
    expect(isWriteBlocked("PUT", "/workflows/wf_123")).toBe(true);
    expect(isWriteBlocked("DELETE", "/workflows/wf_123")).toBe(true);
    expect(isWriteBlocked("POST", "/workflows/wf_123/deploy")).toBe(true);
    expect(isWriteBlocked("POST", "/workflows/wf_123/build")).toBe(true);
  });

  it("lets a run, a stop, and every read through", () => {
    expect(isWriteBlocked("POST", "/workflows/wf_123/run")).toBe(false);
    expect(isWriteBlocked("POST", "/workflows/wf_123/stop")).toBe(false);
    expect(isWriteBlocked("GET", "/workflows")).toBe(false);
    expect(isWriteBlocked("GET", "/workflows/wf_123")).toBe(false);
    expect(isWriteBlocked("POST", "/credits/redeem-coupon")).toBe(false);
    expect(isWriteBlocked("PATCH", "/settings")).toBe(false);
    expect(isWriteBlocked("POST", "/auth/password")).toBe(false);
  });

  it("is case-insensitive on the method", () => {
    expect(isWriteBlocked("post", "/workflows")).toBe(true);
    expect(isWriteBlocked("delete", "/workflows/wf_1")).toBe(true);
  });

  // A blocked path must stay blocked however it is dressed up, or the guard is
  // decorative -- these are the shapes a fetch helper can produce by accident.
  it("cannot be slipped past with a query string or trailing slash", () => {
    expect(isWriteBlocked("POST", "/workflows?draft=1")).toBe(true);
    expect(isWriteBlocked("POST", "/workflows/")).toBe(true);
    expect(isWriteBlocked("PUT", "/workflows/wf_1/")).toBe(true);
    expect(isWriteBlocked("POST", "/workflows/wf_1/deploy?force=1")).toBe(true);
  });

  // The id segment is one path element. A deeper path is a different endpoint
  // and must not be swept up by the single-segment rules.
  it("does not over-match nested routes", () => {
    expect(isWriteBlocked("PUT", "/workflows/wf_1/agents/a_1")).toBe(false);
    expect(isWriteBlocked("POST", "/workflows/wf_1/agents/a_1/fund")).toBe(
      false,
    );
  });
});
