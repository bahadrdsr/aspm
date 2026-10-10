import { expect, test } from "./application-fixture";
import { requireProductionUI } from "./network";

test.use({ reducedMotion: "reduce" });

test.beforeEach(() => {
  requireProductionUI();
});

test("M13 release accessibility has named controls and visible keyboard focus", async ({ page, app }) => {
  expect(app.requests).toEqual([]);
  await page.goto("/#/work");
  await expect(page.getByRole("table", { name: "Findings" })).toBeVisible();

  const cdp = await page.context().newCDPSession(page);
  const tree = await cdp.send("Accessibility.getFullAXTree");
  const namedRoles = new Set(["button", "link", "textbox", "combobox", "checkbox", "radio", "heading"]);
  const unnamed = tree.nodes.filter((node) => {
    const role = node.role?.value;
    return !node.ignored && typeof role === "string" && namedRoles.has(role) &&
      !String(node.name?.value ?? "").trim();
  });
  expect(unnamed.map((node) => node.role?.value), "Every exposed interactive control and heading needs a name.").toEqual([]);

  const focused: string[] = [];
  for (let index = 0; index < 18; index++) {
    await page.keyboard.press("Tab");
    const state = await page.evaluate(() => {
      const active = document.activeElement as HTMLElement | null;
      if (!active || active === document.body) return null;
      const style = getComputedStyle(active);
      const visible = style.outlineStyle !== "none" || style.boxShadow !== "none" ||
        style.borderColor !== "rgba(0, 0, 0, 0)";
      return {
        identity: `${active.tagName}:${active.getAttribute("aria-label") ?? active.textContent?.trim() ?? ""}`,
        visible,
      };
    });
    if (state) {
      expect(state.visible, `Focused control ${state.identity} needs a visible indicator.`).toBe(true);
      focused.push(state.identity);
    }
  }
  expect(new Set(focused).size, "Keyboard navigation must reach several distinct controls.").toBeGreaterThanOrEqual(6);
});

test("M13 release accessibility meets automated contrast, target and reflow checks", async ({ page, app }) => {
  expect(app.requests).toEqual([]);
  await page.goto("/#/work");
  await expect(page.getByRole("table", { name: "Findings" })).toBeVisible();

  const audit = await page.evaluate(() => {
    const parse = (value: string): [number, number, number] | null => {
      const match = /^rgba?\((\d+),\s*(\d+),\s*(\d+)/.exec(value);
      return match ? [Number(match[1]), Number(match[2]), Number(match[3])] : null;
    };
    const luminance = ([red, green, blue]: [number, number, number]) => {
      const values = [red, green, blue].map((value) => {
        const channel = value / 255;
        return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
      });
      return values[0] * 0.2126 + values[1] * 0.7152 + values[2] * 0.0722;
    };
    const contrast = (left: [number, number, number], right: [number, number, number]) => {
      const one = luminance(left), two = luminance(right);
      return (Math.max(one, two) + 0.05) / (Math.min(one, two) + 0.05);
    };
    const background = (element: Element) => {
      let current: Element | null = element;
      while (current) {
        const value = getComputedStyle(current).backgroundColor;
        if (value !== "rgba(0, 0, 0, 0)" && value !== "transparent") return parse(value);
        current = current.parentElement;
      }
      return [255, 255, 255] as [number, number, number];
    };
    const contrastFailures: Array<{ text: string; ratio: number; required: number }> = [];
    for (const element of document.querySelectorAll("p,span,td,th,label,button,a,h1,h2,h3,input,select")) {
      const html = element as HTMLElement;
      const rect = html.getBoundingClientRect(), style = getComputedStyle(html);
      const text = (html.innerText || html.getAttribute("placeholder") || "").trim();
      if (!text || rect.width === 0 || rect.height === 0 || style.visibility === "hidden" ||
        style.display === "none" || Number(style.opacity) < 0.8 ||
        html.matches(":disabled,[aria-disabled='true']") ||
        [...html.children].some((child) => (child.textContent ?? "").trim())) continue;
      const foreground = parse(style.color), behind = background(html);
      if (!foreground || !behind) continue;
      const size = Number.parseFloat(style.fontSize), weight = Number.parseInt(style.fontWeight, 10) || 400;
      const required = size >= 24 || (size >= 18.66 && weight >= 700) ? 3 : 4.5;
      const ratio = contrast(foreground, behind);
      if (ratio + 0.01 < required) contrastFailures.push({ text: text.slice(0, 80), ratio, required });
    }
    const targetFailures = [...document.querySelectorAll("button,input,select,textarea")]
      .filter((element) => {
        const html = element as HTMLElement, rect = html.getBoundingClientRect(), style = getComputedStyle(html);
        return !html.matches(":disabled,[aria-disabled='true']") && style.display !== "none" &&
          style.visibility !== "hidden" && rect.width > 0 && rect.height > 0 &&
          (rect.width < 24 || rect.height < 24);
      }).map((element) => ({
        name: element.getAttribute("aria-label") || (element as HTMLElement).innerText || element.tagName,
        width: element.getBoundingClientRect().width, height: element.getBoundingClientRect().height,
      }));
    return { contrastFailures, targetFailures };
  });
  expect(audit.contrastFailures).toEqual([]);
  expect(audit.targetFailures).toEqual([]);

  await page.setViewportSize({ width: 683, height: 768 });
  await expect(page.getByRole("table", { name: "Findings" })).toBeVisible();
  const overflow = await page.evaluate(() => ({
    document: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    body: document.body.scrollWidth - document.body.clientWidth,
  }));
  expect(overflow.document).toBeLessThanOrEqual(1);
  expect(overflow.body).toBeLessThanOrEqual(1);
});

test("M13 release interaction feedback stays within the local 100 ms budget", async ({ page, app }) => {
  expect(app.requests).toEqual([]);
  const start = performance.now();
  await page.goto("/#/work");
  await expect(page.getByRole("table", { name: "Findings" })).toBeVisible();
  expect(performance.now() - start, "Fixture-backed cold interactive shell should settle within 2000 ms.").toBeLessThan(2000);

  const filter = page.getByRole("textbox", { name: "Filter findings" });
  const samples = await filter.evaluate(async (element) => {
    const input = element as HTMLInputElement;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
    if (!setter) throw new Error("Native input value setter is unavailable.");
    const measured: number[] = [];
    for (let index = 0; index < 20; index++) {
      const before = performance.now();
      setter.call(input, index % 2 === 0 ? "synthetic" : "");
      input.dispatchEvent(new Event("input", { bubbles: true }));
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      measured.push(performance.now() - before);
    }
    return measured;
  });
  samples.sort((left, right) => left - right);
  const p95 = samples[Math.ceil(samples.length * 0.95) - 1];
  expect(p95, `Local filter p95 was ${p95.toFixed(2)} ms.`).toBeLessThanOrEqual(100);

  const motion = await page.evaluate(() => ({
    reduced: matchMedia("(prefers-reduced-motion: reduce)").matches,
    active: document.getAnimations().filter((animation) => animation.playState === "running").length,
  }));
  expect(motion.reduced).toBe(true);
  expect(motion.active).toBe(0);
});
