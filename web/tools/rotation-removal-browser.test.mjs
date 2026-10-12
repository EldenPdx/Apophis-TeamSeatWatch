// Exercise the built Owner frontend at its API boundary with isolated records.
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { test } from 'node:test';
const { chromium } = await import(process.env.TSW_PLAYWRIGHT_MODULE ?? '/root/.local/share/playwright-cli/node_modules/playwright/index.mjs');

const ownerURL = process.env.TSW_OWNER_BROWSER_URL ?? 'http://127.0.0.1:5194';
const stamp = '2026-10-12T00:00:00Z';
const future = new Date(Date.now() + 24 * 3600000).toISOString();
const motherId = '10000000-0000-4000-8000-000000000001';
const workspaceId = '20000000-0000-4000-8000-000000000001';
const batchId = '30000000-0000-4000-8000-000000000001';
const bindingId = '40000000-0000-4000-8000-000000000001';
const mother = { id: motherId, displayName: '测试母号', loginIdentifier: 'mother@example.test', status: 'active', version: 1, updatedAt: stamp, materialStatus: 'complete', accessStatus: 'verified' };
const batch = {
  id: batchId, bindingId, motherAccountId: motherId, motherAccountName: '测试母号', workspaceId, workspaceName: '测试空间', sourceBatchName: '测试批次',
  sequenceNo: 1, targetCount: 2, plannedAt: future, status: 'serving', version: 1, createdAt: stamp, updatedAt: stamp,
  execution: { activeTaskCount: 0, invitationConfirmedCount: 2, invitationFailedCount: 0, invitationUncertainCount: 0, invitationWaitingSeatCount: 0, readyCount: 2, cardCount: 2 },
  rotation: { state: 'not_due', joinedCount: 2, removedCount: 0, pendingCount: 0, failedCount: 0, unknownCount: 0 },
};
const members = [1, 2].map(index => ({ membershipId: `50000000-0000-4000-8000-${String(index).padStart(12, '0')}`, targetAccountId: `60000000-0000-4000-8000-${String(index).padStart(12, '0')}`, displayLabel: '', identifier: `account${index}@example.test`, joinedAt: stamp, state: 'present' }));

async function fixture(browser, memberCount = 2) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, timezoneId: 'Asia/Shanghai' });
  const page = await context.newPage();
  page.setDefaultTimeout(4000);
  const records = Array.from({ length: memberCount }, (_, index) => ({ ...members[0], membershipId: `50000000-0000-4000-8000-${String(index + 1).padStart(12, '0')}`, targetAccountId: `60000000-0000-4000-8000-${String(index + 1).padStart(12, '0')}`, identifier: `account${index + 1}@example.test` }));
  const state = { batch: structuredClone(batch), members: records, operation: null, writes: [], previewQueries: [], operationQueries: [], errors: [], queued: false, blockers: [], failure: null, previewFailure: false, staleCompletion: false, stalePreview: null };
  state.batch.targetCount = memberCount; state.batch.rotation.joinedCount = memberCount;
  page.on('pageerror', error => state.errors.push(error.message));
  await page.route('**/api/owner/**', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname;
    const json = (body, status = 200) => route.fulfill({ status, json: body });
    if (path.endsWith('/auth-status')) return json({ authenticated: true, username: 'owner' });
    if (path.endsWith('/csrf')) return json({ token: 'fixture-csrf' });
    if (path.endsWith('/mother-accounts')) return json({ items: [mother], total: 1, page: 1, pageSize: 20 });
    if (path === `/api/owner/v1/mother-accounts/${motherId}`) return json(mother);
    if (path.endsWith('/discovery')) return json({ motherAccountId: motherId, status: 'discovered', workspaces: [{ id: workspaceId, displayName: '测试空间', accessStatus: 'readable' }] });
    if (path === `/api/owner/v1/workspaces/${workspaceId}`) return json({ binding: { id: bindingId, motherAccountId: motherId } });
    if (path.endsWith('/proxy-pool/settings')) return json({ taskConcurrency: 3 });
    if (path.endsWith('/batches')) return json({ items: [state.batch], total: 1, page: 1, pageSize: 20 });
    if (path.endsWith('/remove-preview')) {
      state.previewQueries.push(Object.fromEntries(url.searchParams));
      if (state.previewFailure) return json({ code: 'removal_unavailable' }, 503);
      const targetPage = Number(url.searchParams.get('target_page')), targetPageSize = Number(url.searchParams.get('target_page_size'));
      if (state.stalePreview) {
        const stale = state.stalePreview; state.stalePreview = null;
        return json({ batch: { ...stale.batch, rotation: { ...stale.batch.rotation, state: 'removing' } }, targets: stale.members, targetTotal: memberCount, targetPage, targetPageSize, differences: [], differenceTotal: 0, snapshotCompleteness: 'complete', canProceed: false, blockers: [] });
      }
      return json({ batch: state.batch, targets: state.members.slice((targetPage - 1) * targetPageSize, targetPage * targetPageSize), targetTotal: memberCount, targetPage, targetPageSize, differences: [], differenceTotal: 0, snapshotCompleteness: 'complete', canProceed: state.batch.rotation.removedCount < memberCount && !state.queued && !state.blockers.length, blockers: state.blockers });
    }
    if (path.endsWith('/remove-operation')) {
      state.operationQueries.push(Object.fromEntries(url.searchParams));
      if (!state.operation) return json({ code: 'not_found' }, 404);
      const targetPage = Number(url.searchParams.get('target_page')), targetPageSize = Number(url.searchParams.get('target_page_size'));
      return json({ ...state.operation, targets: state.operation.targets.slice((targetPage - 1) * targetPageSize, targetPage * targetPageSize), targetPage, targetPageSize });
    }
    if (path.endsWith('/remove') && request.method() === 'POST') {
      const body = request.postDataJSON();
      state.writes.push({ path, body, csrf: request.headers()['x-csrf-token'] });
      if (state.failure) return json({ code: 'removal_unavailable' }, state.failure);
      if (state.staleCompletion) state.stalePreview = structuredClone({ batch: state.batch, members: state.members });
      const selected = state.members.filter(member => member.state !== 'removed' && (!body.membershipIds || body.membershipIds.includes(member.membershipId)));
      if (!state.queued) for (const member of selected) { member.state = 'removed'; member.removedAt = stamp; }
      const removedCount = state.members.filter(member => member.state === 'removed').length;
      state.batch.rotation = { ...state.batch.rotation, removedCount, state: state.queued ? 'removing' : removedCount === memberCount ? 'completed' : 'not_due' };
      state.operation = { id: `70000000-0000-4000-8000-${String(state.writes.length).padStart(12, '0')}`, batchId, workspaceId, status: state.queued ? 'queued' : 'succeeded', authorizedAt: stamp, completedAt: stamp, correlationId: 'fixture-removal', targets: selected.map(member => ({ ...member, id: member.membershipId, status: state.queued ? 'queued' : 'succeeded', attemptCount: 1, outcomeCode: state.queued ? undefined : 'remove_confirmed' })), targetTotal: selected.length, targetPage: 1, targetPageSize: 20, succeededCount: state.queued ? 0 : selected.length, pendingCount: state.queued ? selected.length : 0, blockedCount: 0 };
      return json(state.operation, 202);
    }
    if (request.method() !== 'GET') state.writes.push({ path, body: request.postDataJSON(), csrf: request.headers()['x-csrf-token'] });
    return json({ code: 'not_found' }, 404);
  });
  await page.goto(`${ownerURL}/owner/#rotation`);
  await page.getByRole('combobox', { name: '母号', exact: true }).click();
  await page.getByRole('option', { name: 'mother@example.test', exact: true }).click();
  await page.getByRole('combobox', { name: '空间', exact: true }).click();
  await page.getByRole('option', { name: '测试空间', exact: true }).click();
  await page.getByRole('button', { name: '查看详情', exact: true }).waitFor();
  return { context, page, state };
}

test('manual batch removal is available before its planned time and requires confirmation', async () => {
  const browser = await chromium.launch({ executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
  let f;
  try {
    f = await fixture(browser);
    await f.page.getByRole('button', { name: '清退批次', exact: true }).click();
    const dialog = f.page.getByRole('dialog', { name: '确认清退批次', exact: true });
    await dialog.getByText('测试母号 · 测试空间 · 第 1 批', { exact: true }).waitFor();
    await dialog.getByText(/尚未到计划清退时间/).waitFor();
    assert.equal(f.state.writes.length, 0);
    await dialog.getByRole('button', { name: '取消', exact: true }).click();
    await dialog.waitFor({ state: 'hidden' });
    assert.equal(f.state.writes.length, 0);
    assert.deepEqual(f.state.errors, []);
  } finally { await f?.context.close(); await browser.close(); }
});

test('completion refreshes stale batch facts when parallel reads straddle worker success', async () => {
  const browser = await chromium.launch({ executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
  let f;
  try {
    f = await fixture(browser);
    const { page, state } = f;
    state.staleCompletion = true;
    await page.getByRole('button', { name: '清退批次', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '确认清退', exact: true }).click();
    await page.getByRole('button', { name: '开始下一批次', exact: true }).waitFor();
    assert.equal(state.writes.length, 1);
    assert.equal(await page.locator('.rotation-detail-panel tbody').getByText('已清退', { exact: true }).count(), 2);
    assert.deepEqual(state.errors, []);
  } finally { await f?.context.close(); await browser.close(); }
});

test('manual removal controls and confirmation fit desktop, dark and narrow layouts', async () => {
  const browser = await chromium.launch({ executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
  let f;
  try {
    f = await fixture(browser);
    const { page, state } = f;
    const evidence = process.env.TSW_BROWSER_EVIDENCE;
    if (evidence) await mkdir(evidence, { recursive: true });
    await page.getByRole('button', { name: '查看详情', exact: true }).click();
    for (const [scheme, width] of [['light', 1440], ['dark', 1440], ['light', 390]]) {
      await page.emulateMedia({ colorScheme: scheme });
      await page.setViewportSize({ width, height: 900 });
      const accountButton = page.getByRole('button', { name: '清退账号 account1@example.test', exact: true });
      await accountButton.click();
      const dialog = page.getByRole('dialog', { name: '确认清退账号', exact: true });
      await dialog.getByRole('button', { name: '确认清退', exact: true }).waitFor();
      assert.equal(await dialog.getByRole('button', { name: '确认清退', exact: true }).isVisible(), true);
      const bounds = await dialog.boundingBox();
      assert.ok(bounds && bounds.x >= 0 && bounds.x + bounds.width <= width);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      if (evidence) await page.screenshot({ path: `${evidence}/rotation-account-${scheme}-${width}.png`, fullPage: true, animations: 'disabled' });
      await dialog.getByRole('button', { name: '取消', exact: true }).click();
      await dialog.waitFor({ state: 'hidden' });
    }
    assert.equal(state.writes.length, 0);
    assert.deepEqual(state.errors, []);
  } finally { await f?.context.close(); await browser.close(); }
});

test('large queued batch removal loads progress for visible members beyond the first operation page', async () => {
  const browser = await chromium.launch({ executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
  let f;
  try {
    f = await fixture(browser, 121);
    const { page, state } = f;
    await page.getByRole('button', { name: '查看详情', exact: true }).click();
    state.queued = true;
    await page.getByRole('button', { name: '清退批次', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '确认清退', exact: true }).click();
    await page.locator('.rotation-detail-panel tbody tr').filter({ hasText: 'account1@example.test' }).getByText('等待处理', { exact: true }).waitFor();
    await page.getByRole('button', { name: '7', exact: true }).click();
    await page.locator('.rotation-detail-panel tbody tr').filter({ hasText: 'account121@example.test' }).getByText('等待处理', { exact: true }).waitFor();
    assert.ok(state.operationQueries.some(query => query.target_page === '2' && query.target_page_size === '100'));
    assert.equal(await page.locator('.rotation-detail-panel tbody tr').count(), 1);
    assert.deepEqual(state.errors, []);
  } finally { await f?.context.close(); await browser.close(); }
});

test('unknown request results keep the same account intent on retry and preserve the error long enough to read', async () => {
  const browser = await chromium.launch({ executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
  let f;
  try {
    f = await fixture(browser);
    const { page, state } = f;
    await page.getByRole('button', { name: '查看详情', exact: true }).click();
    state.failure = 503;
    await page.getByRole('button', { name: '清退账号 account1@example.test', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '确认清退', exact: true }).click();
    await page.getByText('请求结果待核验，请刷新原轮次继续。', { exact: true }).waitFor();
    assert.equal(await page.getByRole('button', { name: '清退账号 account1@example.test', exact: true }).isEnabled(), true);
    state.failure = null;
    await page.getByRole('button', { name: '清退账号 account1@example.test', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '确认清退', exact: true }).click();
    await page.locator('.rotation-detail-panel tbody tr').filter({ hasText: 'account1@example.test' }).getByText('已清退', { exact: true }).waitFor();
    assert.equal(state.writes[0].body.idempotencyKey, state.writes[1].body.idempotencyKey);
    assert.equal(await page.getByRole('button', { name: '开始下一批次', exact: true }).count(), 0);
    assert.deepEqual(state.errors, []);
  } finally { await f?.context.close(); await browser.close(); }
});

test('the detail refresh action recovers a failed member read', async () => {
  const browser = await chromium.launch({ executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
  let f;
  try {
    f = await fixture(browser);
    const { page, state } = f;
    state.previewFailure = true;
    await page.getByRole('button', { name: '查看详情', exact: true }).click();
    await page.getByText('轮转详情读取失败，请刷新重试。', { exact: true }).waitFor();
    state.previewFailure = false;
    await page.getByRole('button', { name: '刷新', exact: true }).click();
    await page.getByRole('button', { name: '清退账号 account1@example.test', exact: true }).waitFor();
    assert.deepEqual(state.errors, []);
  } finally { await f?.context.close(); await browser.close(); }
});

test('batch removal after a single account completes the remaining scope and updates the next-batch action', async () => {
  const browser = await chromium.launch({ executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
  let f;
  try {
    f = await fixture(browser);
    const { page, state } = f;
    await page.getByRole('button', { name: '查看详情', exact: true }).click();
    await page.getByRole('button', { name: '清退账号 account1@example.test', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '确认清退', exact: true }).click();
    await page.locator('.rotation-detail-panel tbody tr').filter({ hasText: 'account1@example.test' }).getByText('已清退', { exact: true }).waitFor();
    await page.getByRole('button', { name: '清退批次', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: '确认清退批次', exact: true });
    await dialog.getByText('本批次尚有 1 个账号未清退，确认后将逐个处理并保留每个结果。', { exact: true }).waitFor();
    await dialog.getByRole('button', { name: '确认清退', exact: true }).click();
    await page.getByRole('button', { name: '开始下一批次', exact: true }).waitFor();
    state.blockers = [{ code: 'batch_not_serving', message: '只有当前服务中的批次可以确认移除' }];
    await page.getByRole('button', { name: '返回轮转列表', exact: true }).click();
    await page.getByRole('button', { name: '查看详情', exact: true }).click();
    await page.locator('.rotation-detail-panel tbody tr').filter({ hasText: 'account1@example.test' }).getByText('已清退', { exact: true }).waitFor();
    assert.equal(await page.getByText('只有当前服务中的批次可以确认移除', { exact: true }).count(), 0);
    assert.equal(await page.locator('.rotation-detail-panel tbody').getByText('已清退', { exact: true }).count(), 2);
    assert.equal(state.writes.length, 2);
    assert.equal(state.writes[1].body.membershipIds, undefined);
    assert.equal(state.writes[1].body.manual, true);
    assert.notEqual(state.writes[0].body.idempotencyKey, state.writes[1].body.idempotencyKey);
    await page.getByRole('button', { name: '返回轮转列表', exact: true }).click();
    const row = page.locator('.rotation-page tbody tr');
    await row.getByText('已完成', { exact: true }).waitFor();
    assert.equal(await row.getByRole('button', { name: '清退批次', exact: true }).isDisabled(), true);
    assert.deepEqual(state.errors, []);
  } finally { await f?.context.close(); await browser.close(); }
});

test('safety blockers stay visible after an earlier account removal succeeds', async () => {
  const browser = await chromium.launch({ executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
  let f;
  try {
    f = await fixture(browser);
    const { page, state } = f;
    await page.getByRole('button', { name: '查看详情', exact: true }).click();
    await page.getByRole('button', { name: '清退账号 account1@example.test', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '确认清退', exact: true }).click();
    await page.locator('.rotation-detail-panel tbody tr').filter({ hasText: 'account1@example.test' }).getByText('已清退', { exact: true }).waitFor();
    state.blockers = [{ code: 'workspace_owner_missing', message: '当前管理员身份需要重新核验' }];
    await page.getByRole('button', { name: '重新核验', exact: true }).click();
    await page.getByText('当前管理员身份需要重新核验', { exact: true }).waitFor();
    assert.equal(await page.getByRole('button', { name: '清退账号 account2@example.test', exact: true }).isDisabled(), true);
    assert.equal(await page.getByRole('button', { name: '清退批次', exact: true }).count(), 0);
    assert.equal(state.writes.length, 1);
    assert.deepEqual(state.errors, []);
  } finally { await f?.context.close(); await browser.close(); }
});

test('selective removal on a later member page retains that page and shows its exact queued result', async () => {
  const browser = await chromium.launch({ executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
  let f;
  try {
    f = await fixture(browser, 21);
    const { page, state } = f;
    await page.getByRole('button', { name: '查看详情', exact: true }).click();
    await page.getByRole('button', { name: '2', exact: true }).click();
    const last = page.locator('.rotation-detail-panel tbody tr').filter({ hasText: 'account21@example.test' });
    state.queued = true;
    await last.getByRole('button', { name: '清退账号 account21@example.test', exact: true }).click();
    await page.getByRole('dialog', { name: '确认清退账号', exact: true }).getByRole('button', { name: '确认清退', exact: true }).click();
    await last.getByText('等待处理', { exact: true }).waitFor();
    assert.equal(await page.locator('.rotation-detail-panel tbody tr').count(), 1);
    assert.equal(await last.getByRole('button', { name: '清退账号 account21@example.test', exact: true }).isDisabled(), true);
    assert.equal(await page.getByRole('button', { name: '开始下一批次', exact: true }).count(), 0);
    assert.deepEqual(state.writes[0].body.membershipIds, [state.members[20].membershipId]);
    assert.deepEqual(state.errors, []);
  } finally { await f?.context.close(); await browser.close(); }
});

test('manual account removal keeps the remaining accounts available and does not complete the batch', async () => {
  const browser = await chromium.launch({ executablePath: process.env.TSW_CHROMIUM_PATH ?? '/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome', headless: true, args: ['--no-sandbox'] });
  let f;
  try {
    f = await fixture(browser);
    const { page, state } = f;
    await page.getByRole('button', { name: '查看详情', exact: true }).click();
    const first = page.locator('.rotation-detail-panel tbody tr').filter({ hasText: 'account1@example.test' });
    const second = page.locator('.rotation-detail-panel tbody tr').filter({ hasText: 'account2@example.test' });
    await first.getByRole('button', { name: '清退账号 account1@example.test', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: '确认清退账号', exact: true });
    await dialog.getByText('account1@example.test', { exact: true }).waitFor();
    await dialog.getByRole('button', { name: '确认清退', exact: true }).click();
    await first.getByText('已清退', { exact: true }).waitFor();
    assert.equal(await second.getByRole('button', { name: '清退账号 account2@example.test', exact: true }).isEnabled(), true);
    assert.equal(await page.getByRole('button', { name: '开始下一批次', exact: true }).count(), 0);
    assert.equal(await page.locator('.rotation-detail-panel tbody tr').count(), 2);
    assert.equal(state.writes.length, 1);
    assert.deepEqual(state.writes[0].body.membershipIds, [members[0].membershipId]);
    assert.equal(state.writes[0].body.manual, true);
    assert.equal(state.writes[0].body.confirm, true);
    assert.equal(state.writes[0].csrf, 'fixture-csrf');
    assert.ok(state.previewQueries.every(query => query.manual === 'true'));
    assert.deepEqual(state.errors, []);
  } finally { await f?.context.close(); await browser.close(); }
});
