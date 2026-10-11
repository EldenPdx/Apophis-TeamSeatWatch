import test from 'node:test';
import assert from 'node:assert/strict';
import { connectSelectedWorkspace, workspaceConnectionFeedback, workspaceVerificationFeedback } from '../src/owner/workspaceConnection.ts';

const motherId = 'mother'; const workspaceId = 'chosen-team';
const revoked = { motherAccountId: motherId, workspaceId, status: 'failed', failure: { code: 'workspace_token_revoked', upstreamCode: 'token_revoked', httpStatus: 401, routeMode: 'required', proxyScheme: 'socks5h' } };
const ready = { motherAccountId: motherId, workspaceId, status: 'ready', expiresAt: new Date(Date.now() + 3600000).toISOString() };
const discovered = { motherAccountId: motherId, status: 'discovered', workspaces: [{ id: workspaceId, accessStatus: 'readable' }] };

function fixture(overrides = {}) {
  const calls = []; let exchanges = 0;
  const options = {
    motherId, workspaceId, access: null,
    exchange: async (space, mother) => { calls.push(['exchange', space, mother]); return exchanges++ ? ready : revoked; },
    login: async mother => { calls.push(['login', mother]); return { motherAccountId: mother, status: 'ready' }; },
    discover: async mother => { calls.push(['discover', mother]); return discovered; },
    onStage: () => {}, isCurrent: () => true, ...overrides,
  };
  return { options, calls };
}

test('revocation automatically logs in, discovers and reconnects the originally selected Team once', async () => {
  const f = fixture();
  assert.deepEqual(await connectSelectedWorkspace(f.options), ready);
  assert.deepEqual(f.calls, [['exchange', workspaceId, motherId], ['login', motherId], ['discover', motherId], ['exchange', workspaceId, motherId]]);
});

test('saved revoked failure starts with full login and a second revocation never loops', async () => {
  const f = fixture({ access: revoked });
  assert.deepEqual(await connectSelectedWorkspace(f.options), revoked);
  assert.deepEqual(f.calls, [['login', motherId], ['discover', motherId], ['exchange', workspaceId, motherId]]);
});

test('proxy, permission and unknown switch failures do not submit passwords', async () => {
  for (const code of ['network_timeout', 'platform_browser_challenge', 'platform_access_denied', 'workspace_personal_returned']) {
    const failed = { ...revoked, failure: { code } };
    const f = fixture({ exchange: async () => failed });
    assert.deepEqual(await connectSelectedWorkspace(f.options), failed);
    assert.deepEqual(f.calls, []);
  }
});

test('automatic login challenge preserves its exact cause and stops discovery', async () => {
  const f = fixture({ access: revoked, login: async () => ({ motherAccountId: motherId, status: 'refresh_failed', failure: { code: 'platform_browser_challenge', httpStatus: 403 } }) });
  await assert.rejects(connectSelectedWorkspace(f.options), /ChatGPT 要求浏览器验证.*HTTP 403/);
  assert.deepEqual(f.calls, []);
});

test('fresh visibility never silently selects another workspace or accepts another mother', async () => {
  for (const discovery of [{ ...discovered, workspaces: [{ id: 'other-team', accessStatus: 'readable' }] }, { ...discovered, motherAccountId: 'other-mother' }]) {
    const f = fixture({ access: revoked, discover: async () => discovery });
    await assert.rejects(connectSelectedWorkspace(f.options), /没有原来选中的空间|其他母号/);
    assert.deepEqual(f.calls, [['login', motherId]]);
  }
});

test('changing scope during full login stops remaining mutations', async () => {
  let current = true;
  const f = fixture({ access: revoked, isCurrent: () => current, login: async () => { current = false; return { motherAccountId: motherId, status: 'ready' }; } });
  await assert.rejects(connectSelectedWorkspace(f.options), /选择已变化/);
  assert.deepEqual(f.calls, []);
});

test('messages distinguish platform revocation, actual route and incomplete historical evidence', () => {
  assert.match(workspaceConnectionFeedback(revoked), /登录凭据已被 ChatGPT 撤销.*HTTP 401.*token_revoked.*SOCKS5H 代理/);
  assert.match(workspaceConnectionFeedback({ ...revoked, failure: { code: 'network_timeout', routeMode: 'direct' } }), /请求超时.*本次为直连/);
  assert.match(workspaceConnectionFeedback({ ...revoked, failure: undefined }), /历史记录未保存失败原因/);
  assert.match(workspaceVerificationFeedback({ status: 'partial', readSources: [{ source: 'subscriptions', permission: 'read', completeness: 'complete', outcome: 'operational' }, { source: 'workspace_members', permission: 'denied', completeness: 'unknown', outcome: 'unauthorized' }] }), /成员列表：空间凭据已失效/);
});

test('direct capacity and acquisition timeouts explain that no space request was sent', async () => {
  for (const [code, reason] of [['connection_capacity_exhausted', /并发名额已用满/], ['connection_acquisition_timeout', /连接资源.*超时/]]) {
    const failed = { ...revoked, failure: { code, routeMode: 'direct' } };
    const message = workspaceConnectionFeedback(failed);
    assert.match(message, reason);
    assert.match(message, /尚未向 ChatGPT 发出空间请求/);
    assert.match(message, /当前配置为直连/);
    assert.doesNotMatch(message, /可用代理|本次为直连/);
    const f = fixture({ exchange: async () => failed });
    assert.deepEqual(await connectSelectedWorkspace(f.options), failed);
    assert.deepEqual(f.calls, []);
  }
});
