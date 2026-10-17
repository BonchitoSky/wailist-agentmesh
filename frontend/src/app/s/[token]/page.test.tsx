// @vitest-environment node
import { afterEach, beforeEach, expect, it, vi } from "vitest";

vi.mock("@/components/share/SharePreview", () => ({
  SharePreview: () => null,
}));
vi.mock("next/og", () => ({
  ImageResponse: class {
    constructor(public element: unknown) {}
  },
}));

const fetchMock = vi.fn();
const params = Promise.resolve({ token: "new-token-after-build" });

beforeEach(() => {
  vi.resetModules();
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  vi.stubEnv("BACKEND_URL", "http://backend.test");
  vi.stubEnv("MOBILE_BUILD", "0");
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

it("renders web shares without opting new tokens into static generation", async () => {
  const page = await import("./page");
  const image = await import("./opengraph-image");
  expect(page.generateStaticParams).toBeUndefined();
  expect(image.generateStaticParams).toBeUndefined();
});

it("refreshes metadata after revocation without retaining the shared name", async () => {
  fetchMock
    .mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        share: { name: "Private snapshot", nodeCount: 1, edgeCount: 0 },
      }),
    })
    .mockResolvedValueOnce({ ok: false });
  const { generateMetadata } = await import("./page");
  expect((await generateMetadata({ params })).title).toBe(
    "Private snapshot, shared on AgentMesh",
  );
  const revoked = await generateMetadata({ params });
  expect(revoked.title).toBe("Shared workflow");
  expect(JSON.stringify(revoked)).not.toContain("Private snapshot");
  expect(fetchMock).toHaveBeenCalledTimes(2);
  for (const call of fetchMock.mock.calls) {
    expect(call).toEqual([
      "http://backend.test/shares/new-token-after-build",
      { cache: "no-store" },
    ]);
  }
});

it("refreshes the preview image after revocation", async () => {
  fetchMock
    .mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        share: { name: "Private snapshot", nodeCount: 1, edgeCount: 0 },
      }),
    })
    .mockResolvedValueOnce({ ok: false });
  const { default: image } = await import("./opengraph-image");
  expect(JSON.stringify(await image({ params }))).toContain("Private snapshot");
  expect(JSON.stringify(await image({ params }))).not.toContain("Private snapshot");
  expect(fetchMock).toHaveBeenCalledTimes(2);
  expect(fetchMock.mock.calls.every((call) => call[1].cache === "no-store")).toBe(true);
});

it("exports the mobile shell without a server lookup", async () => {
  vi.stubEnv("MOBILE_BUILD", "1");
  const page = await import("./page");
  const image = await import("./opengraph-image");
  expect(page.generateStaticParams?.()).toEqual([{ token: "app" }]);
  expect(image.generateStaticParams?.()).toEqual([{ token: "app" }]);
  expect((await page.generateMetadata({ params })).title).toBe("Shared workflow");
  await page.default({ params });
  await image.default({ params });
  expect(fetchMock).not.toHaveBeenCalled();
});
