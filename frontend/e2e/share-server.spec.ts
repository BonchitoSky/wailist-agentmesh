import { randomUUID } from "node:crypto";
import { readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { expect, request, test } from "@playwright/test";

const backend = `http://127.0.0.1:${Number(process.env.SMOKE_API_PORT ?? 3218)}`;

test("a newly minted share has anonymous server content and uncached revocation", async ({ page, context }) => {
  const build = join(__dirname, "../.next/BUILD_ID");
  expect(readFileSync(build, "utf8").trim()).not.toBe("");
  const started = Date.now();
  expect(statSync(build).mtimeMs).toBeLessThanOrEqual(started);
  const marker = randomUUID();
  const name = `Fresh server share ${marker}`;
  const description = `Created after the production build ${marker}`;
  const owner = await request.newContext({ baseURL: backend });
  const anonymous = await request.newContext({ baseURL: backend });
  try {
    const signup = await owner.post("/auth/signup", {
      data: { email: `${marker}@example.test`, password: randomUUID(), name: "Smoke owner", org: "Scratch" },
    });
    expect(signup.ok()).toBe(true);
    const created = await owner.post("/workflows", { data: { name } });
    expect(created.status()).toBe(201);
    const workflow = await created.json();
    const saved = await owner.put(`/workflows/${workflow.id}`, {
      data: {
        name, description,
        nodes: [{ id: "manual", type: "trigger", label: "Manual start", triggerType: "manual", x: 0, y: 0 }],
        edges: [],
      },
    });
    expect(saved.status()).toBe(200);
    const minted = await owner.post(`/workflows/${workflow.id}/share`, { data: {} });
    expect(minted.status()).toBe(201);
    const { share } = await minted.json();
    expect(share.token).toMatch(/^[A-Za-z0-9_-]{16,64}$/);
    expect(share.name).toBe(name);
    expect(share.description).toBe(description);
    expect(Date.parse(share.createdAt)).toBeGreaterThanOrEqual(started - 1000);

    expect((await anonymous.get(`/shares/${share.token}`)).status()).toBe(200);
    expect((await anonymous.get("/shares")).status()).toBe(401);
    expect((await owner.get("/shares")).status()).toBe(200);
    expect(await context.cookies()).toEqual([]);
    const response = await page.goto(`/s/${share.token}`);
    expect(response?.status()).toBe(200);
    expect(new URL(page.url()).pathname).toBe(`/s/${share.token}`);
    expect(response?.headers()["cache-control"]).toContain("no-store");
    await expect(page).toHaveTitle(new RegExp(name));
    await expect(page.locator('meta[property="og:title"]')).toHaveAttribute("content", name);
    await expect(page.locator('meta[name="description"]')).toHaveAttribute("content", description);
    await expect(page.getByRole("heading", { name, exact: true })).toHaveCount(1);
    expect(await context.cookies()).toEqual([]);

    const revoked = await owner.delete(`/shares/${share.token}`);
    expect(revoked.ok()).toBe(true);
    expect((await anonymous.get(`/shares/${share.token}`)).status()).toBe(404);
    const refreshed = await page.reload();
    if (!refreshed) throw new Error("Revoked share did not return a document response");
    expect(refreshed?.status()).toBe(200);
    await expect(page).not.toHaveTitle(new RegExp(name));
    expect(await refreshed.text()).not.toContain(marker);
    await expect(page.locator('meta[property="og:title"][content="' + name + '"]')).toHaveCount(0);

    await page.goto("/account/links");
    const redirect = new URL(page.url());
    expect(redirect.pathname).toBe("/signin");
    expect(redirect.searchParams.get("next")).toBe("/account/links");
    const runs = await owner.get(`/workflows/${workflow.id}/runs`);
    expect(runs.status()).toBe(200);
    expect((await runs.json()).runs).toEqual([]);
    expect((await owner.delete(`/workflows/${workflow.id}`)).ok()).toBe(true);
  } finally {
    await owner.dispose();
    await anonymous.dispose();
  }
});
