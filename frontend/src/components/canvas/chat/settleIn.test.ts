import { describe, it, expect } from "vitest";
import { settleIn, type ChatMessage } from "./useChatSession";

function msg(p: Partial<ChatMessage>): ChatMessage {
  return {
    id: "a-1",
    sender: "assistant",
    text: "",
    ts: "2026-08-07T09:31:02.100Z",
    ...p,
  };
}

describe("settleIn", () => {
  it("settles the turn bound to the given run, not the earliest pending", () => {
    const out = settleIn(
      [
        msg({ id: "a-old", pending: true, runId: "r-1" }),
        msg({ id: "a-new", pending: true, runId: "r-2" }),
      ],
      (m) => m.runId === "r-2",
      { text: "second answer" },
    );
    expect(out[0]).toMatchObject({ id: "a-old", pending: true, text: "" });
    expect(out[1]).toMatchObject({
      id: "a-new",
      pending: false,
      text: "second answer",
    });
  });

  it("settles one exact turn by id", () => {
    const out = settleIn(
      [
        msg({ id: "a-stranded", pending: true }),
        msg({ id: "a-fresh", pending: true }),
      ],
      (m) => m.id === "a-stranded",
      { text: "recovered", interrupted: true },
    );
    expect(out[0]).toMatchObject({ pending: false, interrupted: true });
    expect(out[1].pending).toBe(true);
  });

  it("never settles an already-settled turn", () => {
    const out = settleIn(
      [msg({ id: "a-done", pending: false, runId: "r-1", text: "kept" })],
      (m) => m.runId === "r-1",
      { text: "overwritten" },
    );
    expect(out[0].text).toBe("kept");
  });

  it("returns the same array when nothing matches", () => {
    const input = [msg({ id: "a-1", pending: true, runId: "r-1" })];
    expect(settleIn(input, (m) => m.runId === "r-999", { text: "x" })).toBe(
      input,
    );
  });
});
