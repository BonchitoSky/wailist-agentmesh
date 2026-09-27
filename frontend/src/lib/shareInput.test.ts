import { describe, expect, it } from "vitest";
import { classifyShareInput } from "./shareInput";

describe("classifyShareInput", () => {
  it("reads a link from whatever the sender's clipboard actually held", () => {
    // Every one of these is a real shape a link arrives in: with and without
    // a scheme, with www, with a trailing slash, with the tracking query a
    // chat client staples on, and with the whitespace a paste drags along.
    for (const text of [
      "https://www.agent-mesh.app/s/k3Jm9xQpLr2sTvWyZa1b",
      "https://agent-mesh.app/s/k3Jm9xQpLr2sTvWyZa1b",
      "agent-mesh.app/s/k3Jm9xQpLr2sTvWyZa1b",
      "https://www.agent-mesh.app/s/k3Jm9xQpLr2sTvWyZa1b/",
      "https://www.agent-mesh.app/s/k3Jm9xQpLr2sTvWyZa1b?utm_source=chat",
      "https://www.agent-mesh.app/s/k3Jm9xQpLr2sTvWyZa1b#top",
      "  https://www.agent-mesh.app/s/k3Jm9xQpLr2sTvWyZa1b  ",
    ]) {
      expect(classifyShareInput(text)).toEqual({
        kind: "token",
        token: "k3Jm9xQpLr2sTvWyZa1b",
      });
    }
  });

  it("accepts a link from a preview or a local build", () => {
    // Refusing a non-production host would be pointless theatre: the token is
    // looked up against whichever backend this app talks to, whatever host
    // the pasted string happens to name.
    expect(
      classifyShareInput("http://localhost:3000/s/k3Jm9xQpLr2sTvWyZa1b"),
    ).toEqual({ kind: "token", token: "k3Jm9xQpLr2sTvWyZa1b" });
  });

  it("reads a bare token", () => {
    expect(classifyShareInput("k3Jm9xQpLr2sTvWyZa1b")).toEqual({
      kind: "token",
      token: "k3Jm9xQpLr2sTvWyZa1b",
    });
  });

  it("reads a code by its prefix, however long it is", () => {
    const code = "am1." + "H4sIAAAAAAAA".repeat(30);
    expect(classifyShareInput(code)).toEqual({ kind: "code", code });
  });

  it("treats a long base64 blob as a code, not a token", () => {
    // A legacy code has no prefix, so length is the only thing separating it
    // from a token. One node gzipped is already well past 64 characters.
    const legacy =
      "H4sIAAAAAAAAA6tWKkotLsnMS1WyUjI0MjYxNTO3sLSytlGqBQCz".repeat(4);
    expect(classifyShareInput(legacy)).toEqual({ kind: "code", code: legacy });
  });

  it("treats standard-base64 characters as a code even when short", () => {
    // "+" and "/" cannot occur in a token, so their presence settles it.
    expect(classifyShareInput("H4sIAA+AAA/AAA=")).toEqual({
      kind: "code",
      code: "H4sIAA+AAA/AAA=",
    });
  });

  it("returns null for nothing at all", () => {
    expect(classifyShareInput("")).toBeNull();
    expect(classifyShareInput("   \n ")).toBeNull();
  });
});
