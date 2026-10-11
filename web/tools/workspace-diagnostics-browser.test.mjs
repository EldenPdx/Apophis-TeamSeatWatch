// Opt-in browser acceptance against a running Owner frontend and isolated API fixtures.
const { chromium } = process.env.TSW_OWNER_BROWSER_URL ? await import(process.env.TSW_PLAYWRIGHT_MODULE ?? 'playwright') : {};
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdir } from 'node:fs/promises';

const ownerURL = process.env.TSW_OWNER_BROWSER_URL ?? 'http://127.0.0.1:5177';
const mother = '00000000-0000-4000-8000-000000000001';
const workspace = '00000000-0000-4000-8000-000000000002';
const email = 'mother@example.test';
const future = new Date(Date.now() + 86400000).toISOString();

test('space connection failures show their saved cause, actual route and successful retry', { skip: !process.env.TSW_OWNER_BROWSER_URL }, async t => {
  const browser = await chromium.launch({ ...(process.env.TSW_BROWSER_EXECUTABLE ? { executablePath: process.env.TSW_BROWSER_EXECUTABLE } : {}), headless: true, args: ['--no-sandbox'] });
  await mkdir('.scratch/space-diagnostics', { recursive: true });
  const fixture = async failure => {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage(); page.setDefaultTimeout(6000);
    await page.clock.install();
    await page.addInitScript(() => { Object.defineProperty(navigator, 'clipboard', { value: { writeText: async text => { window.fixtureClipboard = text; } } }); });
    let access = { motherAccountId: mother, workspaceId: workspace, status: 'required' };
    let succeed = false; let verificationPosts = 0; let personalStatus = 'ready'; let opened = false;
    const errors = []; const posts = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/api/owner/**', route => {
      const request = route.request(); const path = new URL(request.url()).pathname;
      const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
      if (request.method() === 'POST') posts.push({ path, body: request.postDataJSON() });
      if (path.endsWith('/auth-status')) return json({ authenticated: true, username: 'owner', passwordChangedAt: '2026-10-06T00:00:00Z' });
      if (path.endsWith('/csrf')) return json({ token: 'fixture-csrf' });
      if (path.endsWith('/proxy-pool/settings')) return json({ mode: 'proxy_required', taskConcurrency: 3, healthyCount: 1 });
      if (path.endsWith('/mother-accounts/export')) return route.fulfill({ status: 200, contentType: 'text/plain', body: `${email}----fixture-password----fixture-totp\n` });
      if (path.endsWith('/mother-accounts')) return json({ items: [{ id: mother, loginIdentifier: email, displayName: email, status: 'active', materialStatus: 'complete', hasPassword: true, hasTotp: true, version: 1, createdAt: future, updatedAt: future }], total: 1, page: 1, pageSize: 20 });
      if (path.endsWith('/personal-session')) { if (request.method() === 'POST') { personalStatus = 'ready'; succeed = true; access = { motherAccountId: mother, workspaceId: workspace, status: 'required' }; } return json({ motherAccountId: mother, status: personalStatus }); }
      if (path.endsWith('/discovery')) return json({ motherAccountId: mother, status: 'discovered', observedAt: future, workspaces: [{ id: workspace, displayName: '测试 Team', accessStatus: 'readable' }] });
      if (path.endsWith('/access')) {
        if (request.method() === 'POST') { if (failure.code === 'workspace_token_revoked') personalStatus = 'session_expired'; access = { motherAccountId: mother, workspaceId: workspace, status: succeed ? 'ready' : 'failed', checkedAt: new Date().toISOString(), ...(succeed ? { expiresAt: future, exchangeId: 'exchange-new' } : { failure }) }; }
        return json(access);
      }
      if (path.endsWith('/verification')) {
        if (request.method() === 'POST') verificationPosts++;
        return json({ motherAccountId: mother, workspaceId: workspace, workspaceName: '测试 Team', accessStatus: 'readable', status: succeed ? 'verified' : 'pending', permission: succeed ? 'read' : 'unknown', completeness: succeed ? 'complete' : 'unknown', canManage: false, members: [], ...(succeed ? { expiresAt: future, exchangeId: 'exchange-new' } : {}) });
      }
      return json({ code: 'not_found' }, 404);
    });
    const open = async () => {
      if (opened) await page.reload(); else await page.goto(`${ownerURL}/owner/#workspaces`); opened = true;
      await page.getByRole('row').filter({ hasText: email }).getByRole('button', { name: '查看空间', exact: true }).click();
      await page.getByRole('combobox', { name: '空间', exact: true }).click();
      await page.getByRole('option', { name: '测试 Team', exact: true }).click();
      await page.getByRole('button', { name: '同步空间', exact: true }).waitFor();
    };
    await open();
    return { page, open, posts, retry: () => { succeed = true; }, verificationPosts: () => verificationPosts, close: async () => { assert.deepEqual(errors, []); await context.close(); } };
  };
  try {
    for (const scenario of [
      { name: 'revoked mother login has a direct full login recovery', failure: { code: 'workspace_token_revoked', upstreamCode: 'token_revoked', httpStatus: 401, routeMode: 'required', proxyScheme: 'socks5h' }, expected: '母号登录凭据已被 ChatGPT 撤销', route: 'token_revoked' },
      { name: 'Team switching fails through a real proxy without blaming the password', failure: { code: 'workspace_personal_returned', httpStatus: 200, routeMode: 'required', proxyScheme: 'socks5h' }, expected: '具体原因尚未确认', route: '本次已使用 SOCKS5H 代理' },
      { name: 'platform browser verification has its status and route', failure: { code: 'platform_browser_challenge', httpStatus: 403, routeMode: 'required', proxyScheme: 'socks5h' }, expected: 'ChatGPT 要求浏览器验证', route: 'HTTP 403' },
      { name: 'direct timeout keeps direct route even when pool settings require proxies', failure: { code: 'network_timeout', routeMode: 'direct' }, expected: '请求超时', route: '本次为直连' },
      { name: 'capacity failure says no platform request was sent', failure: { code: 'proxy_capacity_exhausted', routeMode: 'required' }, expected: '尚未向 ChatGPT 发出空间请求', route: '未取得代理连接' },
    ]) await t.test(scenario.name, async () => {
      const f = await fixture(scenario.failure);
      if (scenario.failure.code === 'proxy_capacity_exhausted') {
        const row = f.page.getByRole('row').filter({ hasText: email });
        await f.page.keyboard.press('Escape');
        assert.equal(await row.getByRole('button').first().innerText(), '复制');
        assert.equal(await row.getByRole('button', { name: '修正资料' }).count(), 0);
        await row.getByRole('button', { name: '复制', exact: true }).click();
        await row.getByRole('button', { name: '已复制', exact: true }).waitFor();
        assert.equal(await f.page.evaluate(() => window.fixtureClipboard), `${email}----fixture-password----fixture-totp`);
        assert.deepEqual(f.posts[0].body, { accountIds: [mother], expectedCount: 1, confirmed: true });
        f.posts.length = 0; await f.open();
      }
      await f.page.getByRole('button', { name: '同步空间', exact: true }).click();
      if (scenario.failure.code === 'workspace_token_revoked') {
        await f.page.getByText('空间信息已同步。', { exact: true }).waitFor();
        assert.equal(f.verificationPosts(), 1);
        assert.deepEqual(f.posts.map(item => item.path), [`/api/owner/v1/workspaces/${workspace}/access`, `/api/owner/v1/mother-accounts/${mother}/personal-session`, `/api/owner/v1/mother-accounts/${mother}/discovery`, `/api/owner/v1/workspaces/${workspace}/access`, `/api/owner/v1/workspaces/${workspace}/verification`]);
        assert.equal(await f.page.getByRole('button', { name: '重新登录母号并发现空间' }).count(), 0);
        await f.open();
        assert.equal(f.posts.length, 5, 'opening details never logs in automatically');
        await f.close(); return;
      }
      const failure = f.page.getByRole('alert').filter({ hasText: scenario.expected }); await failure.waitFor();
      assert.match(await failure.innerText(), new RegExp(scenario.route));
      assert.equal(f.verificationPosts(), 0, 'failed connection never reads members');
      assert.deepEqual(f.posts[0].body, { motherAccountId: mother, confirmed: true });
      await f.page.clock.runFor(3500); await failure.waitFor();
      await f.open(); await f.page.getByRole('alert').filter({ hasText: scenario.expected }).waitFor();
      await f.page.setViewportSize({ width: 390, height: 844 });
      assert.equal(await f.page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      await f.page.screenshot({ path: `.scratch/space-diagnostics/${scenario.failure.code}.png` });
      f.retry(); await f.page.getByRole('button', { name: '同步空间', exact: true }).click();
      await f.page.getByText('空间信息已同步。', { exact: true }).waitFor();
      assert.equal(f.verificationPosts(), 1);
      assert.equal(await f.page.getByText(scenario.expected, { exact: false }).count(), 0);
      await f.close();
    });
  } finally { await browser.close(); }
});
