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

for (const region of [
  { name: "ordinary vertical scroller", className: "", shouldOverflow: true },
  { name: "chat table", className: "chat-table-wrap", shouldOverflow: false },
  { name: "display math", className: "katex-display", shouldOverflow: false },
  { name: "code block", className: "chat-code-block__body", shouldOverflow: false },
]) {
  test(`overflow collection distinguishes ${region.name}`, async ({ page }) => {
    await openRoute(page, "/signin");
    await settle(page);
    expect(await horizontalOffenders(page)).toEqual([]);

    const geometry = await page.evaluate(({ className }) => {
      const host = document.createElement("div");
      host.id = "smoke-scroll-control";
      host.className = "chat-markdown";
      Object.assign(host.style, {
        position: "fixed",
        left: "0",
        top: "0",
        width: `${window.innerWidth}px`,
      });
      const scroller = document.createElement("div");
      scroller.className = className;
      scroller.style.height = "40px";
      if (!className) scroller.style.overflow = "auto";
      const child = document.createElement("div");
      child.textContent = "oversized scrolling child";
      child.style.width = `${window.innerWidth + 80}px`;
      child.style.height = "80px";
      scroller.append(child);
      host.append(scroller);
      document.body.append(host);
      return {
        viewport: document.documentElement.clientWidth,
        left: scroller.getBoundingClientRect().left,
        right: scroller.getBoundingClientRect().right,
        childRight: child.getBoundingClientRect().right,
        overflowX: getComputedStyle(scroller).overflowX,
        clientHeight: scroller.clientHeight,
        scrollHeight: scroller.scrollHeight,
      };
    }, region);

    try {
      expect(geometry.left).toBe(0);
      expect(geometry.right).toBeLessThanOrEqual(geometry.viewport);
      expect(geometry.childRight).toBeGreaterThan(geometry.viewport + 1);
      expect(geometry.overflowX).toBe("auto");
      expect(geometry.scrollHeight).toBeGreaterThan(geometry.clientHeight);
      const offenders = await horizontalOffenders(page);
      if (region.shouldOverflow) {
        expect(offenders).toEqual(
          expect.arrayContaining([
            expect.objectContaining({
              element: expect.stringContaining("oversized scrolling child"),
            }),
          ]),
        );
      } else {
        expect(offenders).toEqual([]);
      }
    } finally {
      await page.evaluate(() => {
        document.getElementById("smoke-scroll-control")?.remove();
      });
      expect(await horizontalOffenders(page)).toEqual([]);
    }
  });
}

test("an intentional horizontal scroller must itself fit the viewport", async ({
  page,
}) => {
  await openRoute(page, "/signin");
  await settle(page);
  expect(await horizontalOffenders(page)).toEqual([]);
  await page.evaluate(() => {
    const scroller = document.createElement("div");
    scroller.id = "smoke-wide-scroller";
    scroller.className = "chat-code-block__body";
    scroller.textContent = "wide horizontal scroller";
    Object.assign(scroller.style, {
      position: "fixed",
      left: "0",
      top: "0",
      width: `${window.innerWidth + 80}px`,
      height: "20px",
    });
    document.body.append(scroller);
  });
  try {
    expect(await horizontalOffenders(page)).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          element: expect.stringContaining("wide horizontal scroller"),
        }),
      ]),
    );
  } finally {
    await page.evaluate(() => {
      document.getElementById("smoke-wide-scroller")?.remove();
    });
    expect(await horizontalOffenders(page)).toEqual([]);
  }
});
