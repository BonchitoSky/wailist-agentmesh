import { readdirSync } from "node:fs";
import { join, relative, sep } from "node:path";
import { expect, test } from "@playwright/test";
import {
  collectPageErrors,
  horizontalOffenders,
  openRoute,
  settle,
} from "./helpers/page";
import { ALL_ROUTES, WORKFLOW_ID } from "./routes";

test("the route inventory covers current page modules", () => {
  const app = join(__dirname, "../src/app");
  const routes: string[] = [];
  const visit = (directory: string) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name);
      if (entry.isDirectory()) visit(path);
      else if (entry.name === "page.tsx") {
        const segments = relative(app, directory)
          .split(sep)
          .filter((segment) => !segment.startsWith("("))
          .map((segment) => (segment === "[id]" ? WORKFLOW_ID : segment));
        routes.push("/" + segments.join("/"));
      }
    }
  };
  visit(app);

  expect(new Set(ALL_ROUTES).size).toBe(ALL_ROUTES.length);
  expect([...ALL_ROUTES].sort()).toEqual(
    routes.filter((route) => route !== "/auth/callback").sort(),
  );
});

test("page-error collection detects an uncaught error after a clean load", async ({
  page,
}) => {
  const errors = collectPageErrors(page);
  await openRoute(page, "/signin");
  await settle(page);
  expect(errors).toEqual([]);

  const message = "smoke page-error control";
  const nextError = page.waitForEvent("pageerror");
  await page.evaluate((text) => {
    setTimeout(() => {
      throw new Error(text);
    }, 0);
  }, message);

  expect((await nextError).message).toBe(message);
  expect(errors).toEqual([message]);
});

test("overflow collection detects an added element and clears after removal", async ({
  page,
}) => {
  await openRoute(page, "/signin");
  await settle(page);
  expect(await horizontalOffenders(page)).toEqual([]);

  await page.evaluate(() => {
    const element = document.createElement("div");
    element.id = "smoke-overflow-control";
    element.textContent = "smoke overflow control";
    Object.assign(element.style, {
      position: "absolute",
      left: "0",
      top: "0",
      width: `${window.innerWidth + 80}px`,
      height: "20px",
    });
    document.body.append(element);
  });
  expect(await horizontalOffenders(page)).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        element: expect.stringContaining("smoke overflow control"),
      }),
    ]),
  );

  await page.evaluate(() => {
    document.getElementById("smoke-overflow-control")?.remove();
  });
  expect(await horizontalOffenders(page)).toEqual([]);
});
