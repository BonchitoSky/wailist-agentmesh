import { defineConfig } from "@playwright/test";

const port = Number(process.env.SMOKE_SERVER_PORT ?? 3227);
const backend = `http://127.0.0.1:${Number(process.env.SMOKE_API_PORT ?? 3218)}`;
const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
  testDir: ".",
  testMatch: "share-server.spec.ts",
  workers: 1,
  retries: 0,
  forbidOnly: !!process.env.CI,
  reporter: "list",
  use: { baseURL, javaScriptEnabled: false, trace: "retain-on-failure" },
  webServer: [
    {
      command: "node support/start-share-backend.mjs",
      url: `${backend}/health`,
      reuseExistingServer: false,
      timeout: 120_000,
    },
    {
      command: `npm run start -- --hostname 127.0.0.1 --port ${port}`,
      cwd: "..",
      url: baseURL,
      env: { NEXT_PUBLIC_API_URL: "", BACKEND_URL: backend },
      reuseExistingServer: false,
      timeout: 120_000,
    },
  ],
});
