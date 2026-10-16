// Production Owner frontend and isolated API fixtures; no real mother login.
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { test } from 'node:test';

const { chromium } = process.env.TSW_OWNER_BROWSER_URL ? await import(process.env.TSW_PLAYWRIGHT_MODULE ?? 'playwright') : {};
const ownerURL = process.env.TSW_OWNER_BROWSER_URL;
const mother = '00000000-0000-4000-8000-000000000001';
const workspace = '00000000-0000-4000-8000-000000000002';
const email = 'mother-response@example.test';
const future = new Date(Date.now() + 86400000).toISOString();

test('both mother login entry points recover a lost response without repeating the mutation', { skip: !ownerURL }, async t => {
  const browser = await chromium.launch({ ...(process.env.TSW_BROWSER_EXECUTABLE ? { executablePath: process.env.TSW_BROWSER_EXECUTABLE } : {}), headless: true, args: ['--no-sandbox'] });
  await mkdir('.scratch/mother-login-response', { recursive: true });
  const fixture = async entry => {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage(); page.setDefaultTimeout(5000);
    await page.clock.install();
    const requests = []; const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    let personal = entry === 'row' ? 'not_verified' : 'session_expired';
    let access = { motherAccountId: mother, workspaceId: workspace, status: 'failed', failure: { code: 'workspace_session_expired' } };
    await page.route('**/api/owner/**', route => {
      const request = route.request(); const path = new URL(request.url()).pathname;
      requests.push({ method: request.method(), path });
      const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
      if (path.endsWith('/auth-status')) return json({ authenticated: true, username: 'owner' });
      if (path.endsWith('/csrf')) return json({ token: 'fixture-csrf' });
      if (path.endsWith('/mother-accounts')) return json({ items: [{ id: mother, loginIdentifier: email, displayName: email, status: 'active', materialStatus: 'complete', hasPassword: true, hasTotp: true, version: 1, createdAt: future, updatedAt: future }], total: 1, page: 1, pageSize: 20 });
      if (path.endsWith('/personal-session')) {
        if (request.method() === 'POST') { personal = 'verifying'; return route.abort('failed'); }
        return json({ motherAccountId: mother, status: personal, ...(personal === 'ready' ? { checkedAt: new Date().toISOString(), expiresAt: future } : {}) });
      }
      if (path.endsWith('/discovery')) return json({ motherAccountId: mother, status: entry === 'row' && personal !== 'ready' ? 'not_verified' : 'discovered', workspaces: entry === 'row' && personal !== 'ready' ? [] : [{ id: workspace, displayName: '原来选中的 Team', accessStatus: 'readable' }] });
      if (path.endsWith('/access')) {
        if (request.method() === 'POST') access = { motherAccountId: mother, workspaceId: workspace, status: 'ready', expiresAt: future, exchangeId: 'fixture-exchange' };
        return json(access);
      }
      if (path.endsWith('/verification')) return json({ motherAccountId: mother, workspaceId: workspace, workspaceName: '原来选中的 Team', accessStatus: 'readable', status: request.method() === 'POST' || access.status === 'ready' ? 'verified' : 'pending', permission: access.status === 'ready' ? 'read' : 'unknown', completeness: access.status === 'ready' ? 'complete' : 'unknown', canManage: false, members: [], ...(access.status === 'ready' ? { expiresAt: future, exchangeId: 'fixture-exchange' } : {}) });
      return json({ code: 'not_found' }, 404);
    });
    await page.goto(`${ownerURL}/owner/#workspaces`);
    const row = page.getByRole('row').filter({ hasText: email }); await row.waitFor();
    if (entry === 'row') await row.getByRole('button', { name: '登录并发现空间', exact: true }).click();
    else {
      await row.getByRole('button', { name: '查看空间', exact: true }).click();
      await page.getByRole('combobox', { name: '空间', exact: true }).click();
      await page.getByRole('option', { name: '原来选中的 Team', exact: true }).click();
      await page.getByRole('button', { name: '同步空间', exact: true }).click();
    }
    const loginPosts = () => requests.filter(request => request.method === 'POST' && request.path.endsWith('/personal-session'));
    return { context, page, row, requests, loginPosts, complete: () => { personal = 'ready'; }, close: async () => { assert.deepEqual(errors, []); await context.close(); } };
  };
  try {
    for (const entry of ['row', 'workspace']) await t.test(`${entry} waits for the admitted mother and continues its original operation`, async () => {
      const f = await fixture(entry);
      try {
        if (entry === 'row') await f.row.getByText('登录中', { exact: true }).first().waitFor();
        else await f.page.getByText('母号登录已失效，正在自动重新登录', { exact: true }).waitFor();
        // Let the lost POST settle and assert that its replacement is a GET.
        await assertEventually(() => f.loginPosts().length === 1 && f.requests.some((request, index) => request.method === 'GET' && request.path.endsWith('/personal-session') && index > f.requests.findIndex(item => item.method === 'POST' && item.path.endsWith('/personal-session'))));
        assert.equal(f.loginPosts().length, 1);
        assert.equal(await f.page.getByText('母号登录失败，请重试。', { exact: true }).count(), 0);
        if (entry === 'row') assert.equal(await f.row.getByRole('button', { name: '登录中', exact: true }).isDisabled(), true);
        f.complete(); await f.page.clock.runFor(2500);
        if (entry === 'row') {
          await f.row.getByText('已登录', { exact: true }).waitFor();
          await assertEventually(() => f.row.getByRole('button', { name: '重新发现', exact: true }).isEnabled());
        } else await f.page.getByText('空间信息已同步。', { exact: true }).waitFor();
        const posts = f.requests.filter(request => request.method === 'POST').map(request => request.path);
        assert.deepEqual(posts, [`/api/owner/v1/mother-accounts/${mother}/personal-session`, `/api/owner/v1/mother-accounts/${mother}/discovery`, ...(entry === 'workspace' ? [`/api/owner/v1/workspaces/${workspace}/access`, `/api/owner/v1/workspaces/${workspace}/verification`] : [])]);
        assert.equal(f.loginPosts().length, 1);
        await f.page.clock.runFor(300);
        await f.page.screenshot({ path: `.scratch/mother-login-response/${entry}.png` });
      } finally { await f.close(); }
    });
    await t.test('closing the selected workspace during recovery stops discovery and connection', async () => {
      const f = await fixture('workspace');
      try {
        await assertEventually(() => f.loginPosts().length === 1 && f.requests.filter(request => request.method === 'GET' && request.path.endsWith('/personal-session')).length >= 2);
        await f.page.keyboard.press('Escape');
        f.complete(); await f.page.clock.runFor(2500);
        assert.equal(await f.page.getByRole('dialog').count(), 0);
        assert.deepEqual(f.requests.filter(request => request.method === 'POST').map(request => request.path), [`/api/owner/v1/mother-accounts/${mother}/personal-session`]);
      } finally { await f.close(); }
    });
  } finally { await browser.close(); }
});

async function assertEventually(predicate) {
  for (let attempt = 0; attempt < 50; attempt++) {
    if (await predicate()) return;
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  assert.ok(await predicate(), 'lost POST must be followed by a read of the same mother reservation');
}
