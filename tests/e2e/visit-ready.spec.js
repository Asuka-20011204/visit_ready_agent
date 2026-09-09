const { test, expect } = require("@playwright/test");

const initialDescription = "最近三天每天晚上静坐时都会心悸，每次持续十分钟，程度明显，休息后缓解，没有胸痛或呼吸困难，目前没有服药，也没有已知过敏。";
const clarification = "没有其他不适，没有慢性病或近期检查；是否有其他诱因我不清楚，请按现有信息继续整理。";

async function waitForAgent(page) {
  await expect(page.locator("#session-view")).toBeVisible({ timeout: 30000 });
  await expect(page.locator("#working-state")).toBeHidden();
}

async function reachReview(page) {
  await page.locator("#health-input").fill(initialDescription);
  await page.locator("#start-button").click();
  await waitForAgent(page);

  for (let round = 0; round < 4 && await page.locator("#clarification-panel").isVisible(); round += 1) {
    await expect(page.locator("#clarification-input")).toBeEditable();
    await page.locator("#clarification-input").fill(clarification);
    await page.locator("#clarify-button").click();
    await waitForAgent(page);
  }
  await expect(page.locator("#review-panel")).toBeVisible();
}

test("creates, restores, and permanently deletes an owned session", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByTestId("visit-ready-workspace")).toBeVisible();
  await expect(page.locator("#health-input")).toBeEditable();

  const deviceKey = await page.evaluate(() => localStorage.getItem("visitready.device_key.v1"));
  expect(deviceKey).toMatch(/^[A-Za-z0-9_-]{43}$/);

  await reachReview(page);
  await expect(page.locator("#facts-list")).toContainText("心悸");
  await expect(page.locator("#clarification-form")).toBeHidden();
  await expect(page.locator("#input-form")).toBeHidden();
  await expect(page.locator("#history-list .history-item")).toHaveCount(1);

  await page.reload();
  await expect(page.locator("#session-view")).toBeVisible();
  await expect(page.locator("#recovery-notice")).toBeVisible();
  await expect(page.locator("#history-list .history-item")).toHaveCount(1);

  await page.locator("#delete-session-button").click();
  await expect(page.locator("#delete-session-dialog")).toBeVisible();
  await page.locator("#confirm-delete-session-button").click();
  await expect(page.locator("#empty-state")).toBeVisible();
  await expect(page.locator("#history-list .history-item")).toHaveCount(0);
});

test("keeps primary controls readable without horizontal overflow", async ({ page }) => {
  await page.goto("/");
  const layout = await page.evaluate(() => ({
    bodyFont: Number.parseFloat(getComputedStyle(document.body).fontSize),
    helpFont: Number.parseFloat(getComputedStyle(document.querySelector("#composer-help")).fontSize),
    viewportWidth: document.documentElement.clientWidth,
    contentWidth: document.documentElement.scrollWidth
  }));
  expect(layout.bodyFont).toBeGreaterThanOrEqual(16);
  expect(layout.helpFont).toBeGreaterThanOrEqual(14);
  expect(layout.contentWidth).toBeLessThanOrEqual(layout.viewportWidth + 1);

  await page.keyboard.press("Tab");
  await expect(page.locator(".skip-link")).toBeFocused();
});
