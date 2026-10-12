// Built Owner frontend with isolated API records; backend retention has its own integration gate.
import { chromium } from '/root/.local/share/playwright-cli/node_modules/playwright/index.mjs';
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { test } from 'node:test';

const ownerURL = process.env.TSW_OWNER_BROWSER_URL ?? 'http://127.0.0.1:5189';
const makeMother = (index, accessStatus = 'session_expired') => ({
  id: `30000000-0000-4000-8000-${String(index).padStart(12, '0')}`,
  loginIdentifier: `mother${index}@expired.test`, displayName: `mother${index}@expired.test`,
  status: 'active', version: 4, updatedAt: '2026-10-11T17:02:00Z',
  materialStatus: 'complete', accessStatus: 'not_verified', personalStatus: accessStatus,
});

test('mother deletion confirms the account and works after expiry or failed login', async (t) => {
  const browser = await chromium.launch({ executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  page.setDefaultTimeout(4000);
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  let mothers, writes, lists, failure, release, slow;
  await page.route('**/api/owner/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    if (url.pathname.endsWith('/auth-status')) return json({ authenticated: true, username: 'owner' });
    if (url.pathname.endsWith('/csrf')) return json({ token: 'fixture-csrf' });
    if (url.pathname.endsWith('/mother-accounts')) {
      assert.equal(request.method(), 'GET');
      const query = Object.fromEntries(url.searchParams);
      lists.push(query);
      const filtered = mothers.filter((mother) => !query.search || mother.loginIdentifier.includes(query.search));
      const pageNumber = Number(query.page), pageSize = Number(query.page_size);
      return json({ items: filtered.slice((pageNumber - 1) * pageSize, pageNumber * pageSize), total: filtered.length, page: pageNumber, pageSize });
    }
    if (url.pathname.endsWith('/personal-session')) {
      const id = url.pathname.split('/').at(-2);
      return json({ motherAccountId: id, status: mothers.find((mother) => mother.id === id)?.personalStatus ?? 'not_verified' });
    }
    if (url.pathname.endsWith('/discovery')) return json({ motherAccountId: url.pathname.split('/').at(-2), status: 'not_verified', workspaces: [] });
    if (request.method() === 'DELETE' && url.pathname.includes('/mother-accounts/')) {
      const id = url.pathname.split('/').at(-1);
      writes.push({ id, body: request.postDataJSON(), csrf: request.headers()['x-csrf-token'] });
      if (slow) await new Promise((resolve) => { release = resolve; });
      if (failure) return json({ code: 'internal_error' }, failure);
      mothers = mothers.filter((mother) => mother.id !== id);
      return route.fulfill({ status: 204 });
    }
    return json({ code: 'not_found' }, 404);
  });
  const row = (mother) => page.getByRole('table', { name: '母号列表' }).locator('tbody tr').filter({ has: page.getByText(mother.loginIdentifier, { exact: true }) });
  const dialog = () => page.getByRole('dialog', { name: '删除母号', exact: true });
  const button = (name) => page.getByRole('button', { name, exact: true });
  async function open(records = [makeMother(1)]) {
    mothers = records; writes = []; lists = []; failure = null; slow = false; release = null;
    await page.goto(`${ownerURL}/owner/#workspaces`);
    await page.reload();
    await row(records[0]).getByRole('button', { name: '删除', exact: true }).waitFor();
    const label = records[0].status === 'disabled' ? '已停用' : ({ session_expired: '登录已失效', refresh_failed: '登录失败', verifying: '登录中', missing_credentials: '资料待补' })[records[0].personalStatus];
    await row(records[0]).locator('td').nth(2).getByText(label, { exact: true }).waitFor();
  }
  try {
    await t.test('cancel sends no mutation', async () => {
      const mother = makeMother(1);
      await open([mother]);
      await row(mother).getByRole('button', { name: '删除', exact: true }).click();
      await dialog().getByText(`确认删除母号「${mother.loginIdentifier}」？`, { exact: true }).waitFor();
      assert.equal(writes.length, 0);
      await dialog().getByRole('button', { name: '取消', exact: true }).click();
      await dialog().waitFor({ state: 'hidden' });
      assert.equal(writes.length, 0);
      assert.equal(await row(mother).count(), 1);
    });
    await t.test('failed login can be deleted with one confirmed request', async () => {
      const mother = makeMother(1, 'refresh_failed');
      await open([mother]); slow = true;
      await page.getByRole('checkbox', { name: `选择 ${mother.loginIdentifier}`, exact: true }).check();
      await row(mother).getByRole('button', { name: '删除', exact: true }).click();
      await dialog().getByRole('button', { name: '确认删除', exact: true }).click();
      await row(mother).getByText('正在删除母号', { exact: true }).waitFor();
      assert.equal(writes.length, 1);
      assert.equal(await dialog().getByRole('button', { name: '取消', exact: true }).isDisabled(), true);
      await page.keyboard.press('Escape');
      assert.equal(await dialog().isVisible(), true);
      release();
      await page.getByText('暂无母号', { exact: true }).waitFor();
      assert.deepEqual(writes, [{ id: mother.id, body: { confirmed: true }, csrf: 'fixture-csrf' }]);
      assert.equal(await button('导出母号').isDisabled(), true);
      const success = page.getByText(`已删除母号 ${mother.loginIdentifier}`, { exact: true });
      await success.waitFor();
      await page.waitForTimeout(300);
      assert.equal(await success.isVisible(), true);
      await success.waitFor({ state: 'hidden' });
    });
    await t.test('failed requests retain the account and allow retry', async () => {
      const mother = makeMother(1);
      await open([mother]); failure = 500;
      await row(mother).getByRole('button', { name: '删除', exact: true }).click();
      await dialog().getByRole('button', { name: '确认删除', exact: true }).click();
      await dialog().waitFor({ state: 'hidden' });
      await row(mother).getByText('删除未完成，请重试。', { exact: true }).waitFor();
      assert.equal(await row(mother).getByRole('button', { name: '删除', exact: true }).isEnabled(), true);
      failure = null;
      await row(mother).getByRole('button', { name: '删除', exact: true }).click();
      await dialog().getByRole('button', { name: '确认删除', exact: true }).click();
      await page.getByText('暂无母号', { exact: true }).waitFor();
      assert.equal(writes.length, 2);
    });
    await t.test('login in progress, disabled status and incomplete materials never block deletion', async () => {
      for (const mother of [makeMother(1, 'verifying'), { ...makeMother(1), status: 'disabled' }, { ...makeMother(1, 'missing_credentials'), materialStatus: 'needs_totp' }]) {
        await open([mother]);
        assert.equal(await row(mother).getByRole('button', { name: '删除', exact: true }).isEnabled(), true);
        await row(mother).getByRole('button', { name: '删除', exact: true }).click();
        await dialog().getByRole('button', { name: '确认删除', exact: true }).click();
        await page.getByText('暂无母号', { exact: true }).waitFor();
        assert.equal(writes.length, 1);
        assert.deepEqual(writes[0].body, { confirmed: true });
      }
    });
    await t.test('last row deletion preserves search and returns to the previous page', async () => {
      const records = Array.from({ length: 21 }, (_, index) => makeMother(index + 1));
      await open(records);
      await page.getByRole('textbox', { name: '搜索母号', exact: true }).fill('expired.test');
      await button('2').click();
      await row(records[20]).getByRole('button', { name: '删除', exact: true }).click();
      await dialog().getByRole('button', { name: '确认删除', exact: true }).click();
      await row(records[0]).waitFor();
      assert.equal(lists.at(-1).page, '1');
      assert.equal(lists.at(-1).search, 'expired.test');
      assert.equal(await page.getByRole('table', { name: '母号列表' }).locator('tbody tr').count(), 20);
    });
    await t.test('confirmation remains usable in dark and narrow layouts', async () => {
      const mother = makeMother(1);
      await open([mother]);
      const evidence = process.env.TSW_BROWSER_EVIDENCE;
      if (evidence) await mkdir(evidence, { recursive: true });
      for (const [scheme, width] of [['light', 1440], ['dark', 1440], ['light', 390]]) {
        await page.emulateMedia({ colorScheme: scheme });
        await page.setViewportSize({ width, height: 900 });
        await row(mother).getByRole('button', { name: '删除', exact: true }).click();
        await dialog().waitFor();
        assert.equal(await dialog().getByRole('button', { name: '确认删除', exact: true }).isEnabled(), true);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
        if (evidence) await page.screenshot({ path: `${evidence}/mother-delete-${scheme}-${width}.png`, fullPage: true });
        await dialog().getByRole('button', { name: '取消', exact: true }).click();
      }
    });
    assert.deepEqual(errors, []);
  } finally { release?.(); await browser.close(); }
});
