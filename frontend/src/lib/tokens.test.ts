import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { STATUS, SURFACE, TYPE } from "./tokens";

// tokens.ts duplicates values globals.css declares. A duplicated constant
// nothing checks is drift waiting to happen: if this fails, one of the two
// was changed alone.

const css = readFileSync(join(__dirname, "../app/globals.css"), "utf8");

function cssVar(name: string): string | undefined {
  return new RegExp(`--${name}:\\s*(#[0-9a-fA-F]{6})\\s*;`).exec(css)?.[1];
}

describe("design tokens", () => {
  it.each(Object.entries(STATUS))("--%s matches globals.css", (name, hex) => {
    expect(cssVar(name)).toBe(hex);
  });

  // camelCase key -> kebab-case custom property (x402Fg -> --type-x402-fg).
  it.each(Object.entries(TYPE))(
    "--type-%s matches globals.css",
    (name, hex) => {
      const prop = name.replace(/([a-z0-9])([A-Z])/g, "$1-$2").toLowerCase();
      expect(cssVar(`type-${prop}`)).toBe(hex);
    },
  );

  // Values something outside CSS is handed: xterm's canvas, and the
  // theme-color meta tag.
  it("--bg-elev-1 matches the surface xterm is handed", () => {
    expect(cssVar("bg-elev-1")).toBe(SURFACE.elev1);
  });

  it("--bg matches the theme-color the browser paints its chrome with", () => {
    expect(cssVar("bg")).toBe(SURFACE.bg);
  });
});
