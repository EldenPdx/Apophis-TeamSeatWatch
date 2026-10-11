import type { components } from '../generated/owner';

type Access = components['schemas']['SelectedWorkspaceAccessStatus'];
type Facts = components['schemas']['SelectedWorkspaceVerification'];

const connectionMessages: Record<string, string> = {
  workspace_token_revoked: '母号登录凭据已被 ChatGPT 撤销，当前无法读取或同步空间。同步时将自动重新登录母号并发现空间。',
  workspace_session_expired: '母号登录会话已失效，无法获取空间凭据。同步时将自动重新登录母号并发现空间。',
  workspace_session_invalid: '已保存的母号会话缺少有效凭据，无法切换空间。同步时将自动重新登录母号并发现空间。',
  proxy_capacity_exhausted: '未取得可用代理，本次尚未向 ChatGPT 发出空间请求。请在代理管理确认健康节点和空闲名额后重试。',
  connection_capacity_exhausted: '当前任务并发名额已用满，本次尚未向 ChatGPT 发出空间请求。请等待正在执行的任务完成后再同步。',
  connection_acquisition_timeout: '取得连接资源前已超时，本次尚未向 ChatGPT 发出空间请求。请等待正在执行的任务完成后再同步。',
  proxy_egress_drift: '代理出口与验证时不一致，本次空间请求已停止。请重新验证代理节点后重试。',
  proxy_session_expired: '本次代理会话已到期，空间请求已停止。请补齐并验证新的代理节点后重试。',
  proxy_auth_rejected: '代理服务器拒绝了代理身份验证。请检查代理管理中的用户名和密码，再验证节点。',
  network_timeout: '获取空间凭据的请求超时，未取得 ChatGPT 的有效响应。请检查下方记录的连接方式后重试。',
  network_dns_failed: '获取空间凭据时域名解析失败。请检查服务器或代理的 DNS 配置后重试。',
  network_tls_failed: '获取空间凭据时 HTTPS 证书验证失败。请检查服务器或代理的证书配置后重试。',
  network_error: '获取空间凭据时网络连接中断，未取得有效响应。请检查下方记录的连接方式后重试。',
  request_cancelled: '获取空间凭据的请求被取消，尚未完成空间连接。请重新同步。',
  platform_browser_challenge: 'ChatGPT 要求浏览器验证，当前空间请求被拦截。请在 ChatGPT 完成验证，并重新验证当前连接后同步。',
  platform_access_denied: 'ChatGPT 拒绝了本次空间凭据请求。此响应无法区分空间权限限制和平台访问限制，请在 ChatGPT 确认能进入所选空间。',
  platform_rate_limited: 'ChatGPT 限制了请求频率。请稍后再同步，避免连续重试。',
  platform_unavailable: 'ChatGPT 服务返回错误，当前无法获取空间凭据。请稍后再同步。',
  platform_http_rejected: 'ChatGPT 返回了非成功状态，未提供空间凭据。请根据下方 HTTP 状态核对平台访问情况。',
  platform_redirected: 'ChatGPT 将空间凭据请求重定向，未完成空间连接。请重新登录母号并发现空间后重试。',
  platform_response_invalid: 'ChatGPT 返回的内容缺少有效空间会话信息，无法继续同步。请重新同步；仍失败时保留此结果排查。',
  workspace_personal_returned: 'ChatGPT 未完成所选空间的切换，具体原因尚未确认。请保留本次结果排查；此响应无法证明密码错误或空间不存在。',
  workspace_no_longer_visible: 'ChatGPT 当前返回的空间列表中已没有所选空间，页面显示的是此前发现的记录。请重新发现空间；若仍缺失，请在 ChatGPT 核对母号是否仍在此空间中。',
  workspace_mismatch: 'ChatGPT 返回的凭据属于其他空间，与所选空间不一致。请重新发现空间并选择正确的目标后同步。',
  workspace_plan_unsupported: 'ChatGPT 返回的凭据未能确认属于 Team 或 Business 空间。请在 ChatGPT 核对目标空间的订阅类型后重新发现空间。',
  workspace_read_scope_missing: '返回的空间凭据缺少读取空间所需的权限，无法读取成员和订阅信息。请核对母号在此空间的权限后重试。',
  workspace_token_invalid: 'ChatGPT 返回的空间凭据未通过有效性核验，未保存或使用。请重新同步；仍失败时保留此结果排查。',
  workspace_token_expired: 'ChatGPT 返回的空间凭据已到期或即将到期，无法继续同步。同步时将自动重新登录母号并发现空间。',
  workspace_exchange_unavailable: '获取空间凭据未完成，当前记录没有更具体的诊断信息。请重新同步以获取新的结果。',
};

export function workspaceConnectionFeedback(access: Access | null): string {
  if (access?.status === 'permission_denied') return '当前母号没有读取所选空间的权限。请核对空间成员身份后重新发现空间。';
  if (access?.status === 'exchanging') return '空间凭据正在获取中。请等待当前请求完成后再同步。';
  if (access?.status === 'required') return '尚未取得有效空间凭据。请同步空间以建立连接。';
  const failure = access?.failure;
  if (!failure) return '上次获取空间凭据未完成，历史记录未保存失败原因。请重新同步以获取详细结果。';
  const message = connectionMessages[failure.code] ?? connectionMessages.workspace_exchange_unavailable;
  const status = failure.httpStatus ? `HTTP ${failure.httpStatus}` : '';
  const upstream = failure.upstreamCode === 'token_revoked' ? 'token_revoked' : '';
  const acquisitionFailed = ['proxy_capacity_exhausted', 'connection_capacity_exhausted', 'connection_acquisition_timeout'].includes(failure.code);
  const route = failure.routeMode === 'direct' ? acquisitionFailed ? '当前配置为直连，尚未建立连接' : '本次为直连' : failure.proxyScheme ? `本次已使用 ${failure.proxyScheme.toUpperCase()} 代理` : failure.routeMode === 'required' ? '本次要求使用代理，未取得代理连接' : '';
  return [message, status, upstream, route].filter(Boolean).join(' · ');
}

export function workspaceLoginRequired(access: Access | null): boolean {
  return access?.status === 'failed' && ['workspace_token_revoked', 'workspace_session_expired', 'workspace_session_invalid', 'workspace_token_expired'].includes(access.failure?.code ?? '');
}

export function workspaceVerificationFeedback(facts: Facts): string {
  if (facts.status === 'verified') return '空间信息已同步。';
  const sources: Record<string, string> = { subscriptions: '订阅与账单', seat_type_counts: '席位数量', workspace_members: '成员列表', outbound_invites: '待接受邀请', confirmed_member_identities: '成员身份' };
  const outcomes: Record<string, string> = { unauthorized: '空间凭据已失效', forbidden: '平台拒绝读取', timeout: '请求超时', network_error: '网络连接失败', rate_limited: '平台限制请求频率', incomplete: '未返回完整有效信息' };
  const failures = facts.readSources?.filter(source => source.completeness !== 'complete' || source.permission !== 'read').map(source => `${sources[source.source] ?? '空间信息'}：${outcomes[source.outcome] ?? '未取得有效读取结果'}`);
  if (failures?.length) return `${failures.join('；')}。请处理对应原因后重新同步。`;
  if (facts.status === 'permission_denied') return '平台拒绝读取所选空间的信息。请核对母号在此空间的权限。';
  return '空间凭据已取得，但信息读取未完成；当前结果没有记录具体失败的接口。请重新同步以获取新的结果。';
}

export function workspaceRequestFeedback(problem: { status?: number | undefined; code?: string | undefined }, action: '读取' | '同步'): string {
  if (problem.status === 401) return '管理端登录已失效，请重新登录管理端。';
  if (problem.code === 'workspace_token_required') return '此空间的有效凭据已失效，请重新同步空间。';
  if (problem.code === 'workspace_permission_denied') return '当前母号没有读取所选空间的权限，请重新发现空间并核对成员身份。';
  if (problem.code === 'workspace_exchange_unavailable') return '服务器未配置空间凭据接入功能，当前无法同步。请检查服务配置。';
  if (problem.status === 409) return '母号会话或空间入口已变化，本次请求未生效。请重新发现空间后同步。';
  if (problem.status === undefined) return `${action}空间的请求未取得服务器响应，结果尚未确认。请检查管理端网络连接，再刷新空间状态。`;
  return `服务器未完成空间${action}请求（HTTP ${problem.status}），尚未取得本次平台诊断结果。请刷新空间状态后重试。`;
}


type PersonalAccess = components['schemas']['MotherPersonalAccess'];
type Discovery = components['schemas']['MotherDiscovery'];
export type WorkspaceSyncStage = 'exchange' | 'login' | 'discovery' | 'reading';
export const workspaceSyncProgress: Record<WorkspaceSyncStage, string> = {
  exchange: '正在连接所选空间', login: '母号登录已失效，正在自动重新登录',
  discovery: '已重新登录，正在重新发现所选空间', reading: '正在同步空间信息',
};

export class WorkspaceRecoveryError extends Error {}

export function motherLoginFailure(access: PersonalAccess): string {
  if (access.status === 'missing_credentials') return '自动登录未执行：母号缺少有效的密码或 2FA 资料。请修正母号资料后同步。';
  if (access.status === 'invalid_login') return '自动登录失败：ChatGPT 未接受保存的登录资料，返回结果未区分密码和 2FA。请核对母号资料。';
  if (access.status === 'verifying') return '母号正在登录中，请等待当前登录完成后同步。';
  if (access.status === 'unavailable') return '自动登录无法执行：服务器未配置母号登录功能。';
  const failure = access.failure;
  const messages: Record<string, string> = {
    platform_browser_challenge: 'ChatGPT 要求浏览器验证，自动登录被拦截',
    platform_access_denied: 'ChatGPT 拒绝了自动登录请求',
    platform_rate_limited: 'ChatGPT 限制了登录频率，请稍后同步',
    platform_unavailable: 'ChatGPT 登录服务返回错误，请稍后同步',
    network_timeout: '自动登录请求超时，未收到平台的有效响应',
    network_error: '自动登录时网络连接失败',
    login_request_failed: '自动登录未完成，平台未返回可确认的登录结果',
  };
  return `母号登录已失效；${failure ? messages[failure.code] ?? '自动登录未完成，服务端未保存可确认的原因' : '自动登录未成功，服务端未保存具体原因'}${failure?.httpStatus ? `（HTTP ${failure.httpStatus}）` : ''}。`;
}

// Recover only during an explicit sync, once, and keep the selected mother and
// canonical workspace across the new Personal/discovery generations.
export async function connectSelectedWorkspace(options: {
  motherId: string; workspaceId: string; access: Access | null;
  exchange: (workspaceId: string, motherId: string) => Promise<Access>;
  login: (motherId: string) => Promise<PersonalAccess>;
  personal?: (motherId: string) => Promise<PersonalAccess>;
  discover: (motherId: string) => Promise<Discovery>;
  onStage: (stage: WorkspaceSyncStage) => void;
  isCurrent: () => boolean;
}): Promise<Access> {
  const { motherId, workspaceId, exchange, login, discover, onStage, isCurrent } = options;
  const check = () => { if (!isCurrent()) throw new WorkspaceRecoveryError('空间选择已变化，本次同步已停止。'); };
  const scoped = (access: Access) => {
    check();
    if (access.motherAccountId !== motherId || access.workspaceId !== workspaceId) throw new WorkspaceRecoveryError('服务器返回的母号或空间与本次选择不一致，本次同步已停止。');
    return access;
  };
  check();
  let access = options.access;
  if (access?.status === 'ready' && access.expiresAt && Date.parse(access.expiresAt) > Date.now() + 30_000) return scoped(access);
  if (!workspaceLoginRequired(access) && options.personal) {
    const personal = await options.personal(motherId); check();
    if (personal.motherAccountId !== motherId) throw new WorkspaceRecoveryError('登录状态属于其他母号，本次同步已停止。');
    if (['session_expired', 'not_verified'].includes(personal.status)) access = { motherAccountId: motherId, workspaceId, status: 'failed', failure: { code: 'workspace_session_expired' } };
  }
  if (!workspaceLoginRequired(access)) {
    onStage('exchange'); access = scoped(await exchange(workspaceId, motherId));
  }
  if (!workspaceLoginRequired(access)) return access!;
  onStage('login');
  const personal = await login(motherId); check();
  if (personal.motherAccountId !== motherId) throw new WorkspaceRecoveryError('自动登录返回了其他母号，本次同步已停止。');
  if (personal.status !== 'ready') throw new WorkspaceRecoveryError(motherLoginFailure(personal));
  onStage('discovery');
  const discovery = await discover(motherId); check();
  if (discovery.motherAccountId !== motherId) throw new WorkspaceRecoveryError('重新发现返回了其他母号，本次同步已停止。');
  if (discovery.status !== 'discovered' && discovery.status !== 'empty') {
    const discoveryMessages: Record<string, string> = { session_expired: 'ChatGPT 仍拒绝当前母号会话', permission_denied: 'ChatGPT 拒绝读取母号的空间列表', unavailable: '服务器未配置空间发现功能', discovery_failed: '空间列表读取未完成，服务端未保存具体原因', missing_credentials: '母号缺少有效登录凭据', discovering: '空间发现仍在进行中', not_verified: '尚未取得空间发现结果' };
    const reason = discovery.failure ? workspaceConnectionFeedback({ motherAccountId: motherId, workspaceId, status: 'failed', failure: discovery.failure }) : discoveryMessages[discovery.status] ?? '平台未返回有效空间列表';
    throw new WorkspaceRecoveryError(`自动重新登录后的空间发现失败：${reason}`);
  }
  const selected = discovery.workspaces.find(workspace => workspace.id === workspaceId);
  if (!selected) throw new WorkspaceRecoveryError('已自动重新登录，但 ChatGPT 当前空间列表中没有原来选中的空间。请核对母号在此空间的成员身份。');
  if (selected.accessStatus !== 'readable') throw new WorkspaceRecoveryError('已自动重新登录，但母号当前没有读取所选空间的权限。');
  onStage('exchange');
  return scoped(await exchange(workspaceId, motherId));
}
