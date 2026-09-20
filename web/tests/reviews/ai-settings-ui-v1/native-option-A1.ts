import type { Locator } from "@playwright/test";
import { expect } from "@playwright/test";

export async function expectIneligibleOption(option: Locator) {
  await expect.poll(async () => {
    if (await option.count() === 0) return "omitted";
    return option.evaluate(
      (element) => element instanceof HTMLOptionElement && element.matches(":disabled") ? "disabled" : "invalid",
      undefined, { timeout: 5_000 },
    );
  }, "Ineligible profiles must be omitted or natively disabled options.").toMatch(/^(omitted|disabled)$/);
}
