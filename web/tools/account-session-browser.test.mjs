// Real Owner frontend with isolated API fixtures: no live account logins.
import { chromium } from '/root/.local/share/playwright-cli/node_modules/playwright/index.mjs';
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { test } from 'node:test';

const ownerURL = process.env.TSW_OWNER_BROWSER_URL ?? 'http://127.0.0.1:5177';
const id = '373cc445-0e20-419b-b14d-1175abf72f3d';
const identifier = 'fixture-account@example.test';
const ready = () => ({ targetAccountId: id, status: 'ready', checkedAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 7 * 86400000).toISOString() });

test('account actions show their result and reuse saved sessions', async t => {
  const scenario = (name, run) => !process.env.TSW_ACCOUNT_BROWSER_SCENARIO || name.includes(process.env.TSW_ACCOUNT_BROWSER_SCENARIO) ? t.test(name, run) : t.test(name, { skip: 'outside requested scenario' }, run);
  const browser = await chromium.launch({ executablePath: '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
  await mkdir('.scratch/account-session-reuse', { recursive: true });
  const fixture = async ({ access = { targetAccountId: id, status: 'not_verified' }, healthy = 1, mode = 'proxy_required', acquired, loginStatus = 200, pendingReads = 0, probeOutcome = 'available' } = {}) => {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage(); page.setDefaultTimeout(8000);
    const requests = []; const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    let snapshot = access;
    await page.route('**/api/owner/**', route => {
      const request = route.request(); const url = new URL(request.url()); const path = url.pathname;
      requests.push({ method: request.method(), path, pageSize: url.searchParams.get('page_size') });
      const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
      if (path.endsWith('/auth-status')) return json({ authenticated: true, username: 'owner', passwordChangedAt: '2026-10-06T00:00:00Z' });
      if (path.endsWith('/csrf')) return json({ token: 'fixture-csrf' });
      if (path.endsWith('/proxy-pool/settings')) return json({ mode, taskConcurrency: 1, healthyCount: healthy });
      if (path.endsWith('/proxy-pool')) return json({ mode: 'proxy_required', healthyCount: healthy, pendingCount: 0, taskConcurrency: 1, nodes: [], total: 0, page: 1, pageSize: 20, providers: [], sources: [], targetHealthy: 1, isolatedCount: 0, activeLeases: 0 });
      if (path.endsWith('/target-accounts')) return json({ items: [{ id, identifier, status: 'active', version: 1, materialStatus: 'complete', hasPassword: true, hasTotp: true, createdAt: '2026-10-06T00:00:00Z', updatedAt: '2026-10-06T00:00:00Z', tokenStatus: { hasAccessToken: snapshot.status === 'ready', hasRefreshToken: false }, personalAccess: snapshot, latestProbeStatus: 'unknown', latestProbeErrorCode: 'personal_credential_absent', latestProbeOrigin: 'personal' }], total: 1, page: 1, pageSize: 20 });
      if (path.endsWith('/personal-session')) {
        if (request.method() === 'POST') {
          if (loginStatus !== 200) { if (acquired) snapshot = acquired; return json({ code: 'internal_error' }, loginStatus); }
          snapshot = acquired ?? ready();
        }
        if (request.method() === 'GET' && snapshot.status === 'verifying' && pendingReads > 0 && --pendingReads === 0) snapshot = ready();
        return json(snapshot);
      }
      if (path.endsWith('/personal-probes/preview')) return json({ count: 1, scope: 'selected', label: '选中账号', scopeToken: 'fixture-scope' });
      if (path.includes('/personal-probes')) return json({ id: '00000000-0000-4000-8000-000000000001', scope: 'selected', label: '选中账号', total: 1, queued: 0, running: 0, succeeded: probeOutcome === 'available' ? 1 : 0, failed: probeOutcome === 'available' ? 0 : 1, canceled: 0, notSavedOrRetained: 0, createdAt: new Date().toISOString(), items: [{ targetAccountId: id, identifier, status: probeOutcome === 'available' ? 'succeeded' : 'failed', outcome: probeOutcome, verifiedEvidence: probeOutcome === 'available', attemptCount: 1 }] }, request.method() === 'POST' ? 202 : 200);
      return json({ code: 'not_found' }, 404);
    });
    await page.goto(`${ownerURL}/owner/#accounts`, { waitUntil: 'domcontentloaded' });
    const row = page.getByRole('row').filter({ hasText: identifier }); await row.waitFor();
    const posts = () => requests.filter(request => request.method === 'POST');
    const click = name => row.getByRole('button', { name, exact: true }).click();
    const expect = text => row.getByText(text, { exact: true }).waitFor();
    return { page, row, requests, posts, click, expect, close: async () => { assert.deepEqual(errors, []); await context.close(); } };
  };
  try {
    await scenario('missing AT explains prerequisite without submitting probe', async () => {
      const f = await fixture();
      await f.click('探测'); await f.expect('未获取 AT，请先获取 AT 后探测');
      assert.deepEqual(f.posts(), []);
      assert.equal(await f.row.getByText('未获取 AT', { exact: true }).count(), 1);
      await f.close();
    });
    await scenario('direct mode gets AT with an empty proxy pool', async () => {
      const f = await fixture({ mode: 'direct', healthy: 0 });
      await f.click('获取 AT'); await f.expect('AT 已保存，登录态可复用');
      assert.equal(f.posts().filter(request => request.path.endsWith('/personal-session')).length, 1);
      await f.close();
    });
    await scenario('direct mode failed AT acquisition never links to proxy management', async () => {
      const f = await fixture({ mode: 'direct', healthy: 0, acquired: { targetAccountId: id, status: 'refresh_failed' } });
      await f.click('获取 AT'); await f.row.getByText('未获取到 AT，登录流程未完成，请重试', { exact: false }).waitFor();
      assert.equal(f.posts().filter(request => request.path.endsWith('/personal-session')).length, 1);
      assert.equal(await f.row.getByRole('link', { name: '代理管理', exact: true }).count(), 0, 'direct-mode AT failure must not direct users to proxy management');
      await f.close();
    });
    await scenario('direct mode platform challenge reports HTTP status without blaming proxy or credentials', async () => {
      const f = await fixture({ mode: 'direct', healthy: 0, acquired: { targetAccountId: id, status: 'refresh_failed', failure: { code: 'platform_browser_challenge', httpStatus: 403 } } });
      await f.click('获取 AT'); await f.expect('平台要求浏览器验证，当前登录未完成 · HTTP 403');
      assert.equal(f.posts().filter(request => request.path.endsWith('/personal-session')).length, 1);
      assert.equal(await f.row.getByRole('link', { name: '代理管理', exact: true }).count(), 0);
      await f.close();
    });
    await scenario('direct mode failed AT request never links to proxy management', async () => {
      const f = await fixture({ mode: 'direct', healthy: 0, loginStatus: 500 });
      await f.click('获取 AT'); await f.row.getByText('获取 AT 失败，请检查网络连接后重试', { exact: false }).waitFor();
      assert.equal(f.posts().filter(request => request.path.endsWith('/personal-session')).length, 1);
      assert.equal(await f.row.getByRole('link', { name: '代理管理', exact: true }).count(), 0);
      await f.close();
    });
    await scenario('lost login response observes the same queued attempt and final result', async () => {
      const f = await fixture({ mode: 'direct', healthy: 0, loginStatus: 504, pendingReads: 2, acquired: { targetAccountId: id, status: 'verifying', checkedAt: new Date().toISOString() } });
      await f.click('获取 AT'); await f.expect('正在获取 AT，请稍候');
      await f.row.getByRole('button', { name: '获取中', exact: true }).waitFor();
      await f.expect('AT 已保存，登录态可复用');
      await f.page.evaluate(() => document.fonts.ready);
      assert.equal(await f.row.evaluate(row => getComputedStyle(row).fontFamily.includes('DM Sans')), true);
      await f.page.screenshot({ path: '.scratch/account-session-reuse/response-recovery.png' });
      assert.equal(f.posts().filter(request => request.path.endsWith('/personal-session')).length, 1);
      assert.ok(f.requests.filter(request => request.method === 'GET' && request.path.endsWith('/personal-session')).length >= 3);
      await f.close();
    });
    await scenario('direct mode bulk AT failures never link to proxy management', async () => {
      const f = await fixture({ mode: 'direct', healthy: 0, acquired: { targetAccountId: id, status: 'refresh_failed' } });
      await f.page.getByRole('button', { name: '获取全部 AT', exact: true }).click();
      await f.page.getByText('获取 AT 完成 · 成功 0 · 失败 1 · 跳过 0', { exact: true }).waitFor();
      assert.equal(f.posts().filter(request => request.path.endsWith('/personal-session')).length, 1);
      assert.equal(await f.row.getByRole('link', { name: '代理管理', exact: true }).count(), 0);
      await f.close();
    });
    await scenario('direct mode probe network failure never links to proxy management', async () => {
      const f = await fixture({ mode: 'direct', healthy: 0, access: ready(), probeOutcome: 'network_error' });
      await f.click('探测'); await f.row.getByText('探测连接失败，请检查网络连接后重试', { exact: false }).waitFor();
      assert.equal(f.posts().filter(request => request.path.endsWith('/personal-probes')).length, 1);
      assert.equal(await f.row.getByRole('link', { name: '代理管理', exact: true }).count(), 0);
      await f.close();
    });
    await scenario('direct mode probe all network failure never links to proxy management', async () => {
      const f = await fixture({ mode: 'direct', healthy: 0, access: ready(), probeOutcome: 'network_error' });
      await f.page.getByRole('button', { name: '探测全部', exact: true }).click();
      await f.row.getByText('探测连接失败，请检查网络连接后重试', { exact: false }).waitFor();
      assert.equal(f.posts().filter(request => request.path.endsWith('/personal-probes')).length, 1);
      assert.equal(await f.row.getByRole('link', { name: '代理管理', exact: true }).count(), 0);
      await f.close();
    });
    await scenario('no healthy proxy explains the block beside the record', async () => {
      const f = await fixture({ healthy: 0 });
      await f.click('获取 AT'); await f.row.getByText('没有可用代理，请先在代理管理配置并验证节点', { exact: false }).waitFor();
      assert.deepEqual(f.posts(), []);
      assert.equal(await f.row.getByRole('link', { name: '代理管理', exact: true }).getAttribute('href'), '#proxy');
      await f.page.screenshot({ path: '.scratch/account-session-reuse/proxy-feedback.png' }); await f.close();
    });
    await scenario('valid saved AT is reused even when proxy pool is empty', async () => {
      const f = await fixture({ access: ready(), healthy: 0 });
      await f.click('获取 AT'); await f.expect('已复用有效 AT，无需重新登录');
      assert.deepEqual(f.posts(), []);
      await f.close();
    });
    await scenario('successful AT is reused on the next click with one total POST', async () => {
      const f = await fixture();
      await f.click('获取 AT'); await f.expect('AT 已保存，登录态可复用');
      await f.click('获取 AT'); await f.expect('已复用有效 AT，无需重新登录');
      assert.equal(f.posts().filter(request => request.path.endsWith('/personal-session')).length, 1);
      await f.page.screenshot({ path: '.scratch/account-session-reuse/reuse-feedback.png' }); await f.close();
    });
    await scenario('expired AT acquires a replacement and reports the actual result', async () => {
      const f = await fixture({ access: { ...ready(), status: 'session_expired', expiresAt: new Date(Date.now() - 60000).toISOString() } });
      await f.click('获取 AT'); await f.expect('AT 已保存，登录态可复用');
      assert.equal(f.posts().filter(request => request.path.endsWith('/personal-session')).length, 1); await f.close();
    });
    await scenario('login rejection explains password and 2FA rather than generic pending', async () => {
      const f = await fixture({ acquired: { targetAccountId: id, status: 'invalid_login' } });
      await f.click('获取 AT'); await f.expect('登录验证未通过，请核对密码和 2FA');
      assert.equal(f.posts().length, 1); await f.close();
    });
    await scenario('probe network failure displays concrete feedback beside the account', async () => {
      const f = await fixture({ access: ready(), probeOutcome: 'network_error' });
      await f.click('探测'); await f.row.getByText('探测连接失败，请检查网络连接后重试', { exact: false }).waitFor();
      assert.equal(f.posts().filter(request => request.path.endsWith('/personal-probes')).length, 1); await f.close();
    });
    await scenario('bulk action reuses current page session without login POST or extra page', async () => {
      const f = await fixture({ access: ready() });
      await f.page.getByRole('button', { name: '获取全部 AT', exact: true }).click();
      await f.page.getByText('获取 AT 完成 · 成功 1 · 失败 0 · 跳过 0', { exact: true }).waitFor();
      assert.deepEqual(f.posts(), []);
      assert.equal(f.requests.filter(request => request.path.endsWith('/target-accounts') && request.pageSize === '100').length, 1);
      await f.close();
    });
  } finally { await browser.close(); }
});
