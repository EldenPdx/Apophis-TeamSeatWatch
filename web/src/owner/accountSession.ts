import type { components } from '../generated/owner';

export type PersonalAccess = components['schemas']['TargetPersonalAccess'];
export type AccountFeedback = { message: string; tone: 'success' | 'warning' | 'error'; proxy?: boolean; updatedAt?: number };

export function hasUsablePersonalAccess(access?: PersonalAccess, now = Date.now()) {
  return access?.status === 'ready' && !!access.expiresAt && Date.parse(access.expiresAt) > now + 60000;
}

export function personalAccessFeedback(access: PersonalAccess): AccountFeedback {
  if (access.failure) {
    const messages: Record<NonNullable<PersonalAccess['failure']>['code'], string> = {
      platform_browser_challenge: '平台要求浏览器验证，当前登录未完成',
      platform_access_denied: '平台拒绝登录请求',
      platform_rate_limited: '平台请求过于频繁，请稍后重试',
      platform_unavailable: '平台登录服务暂不可用，请稍后重试',
      network_timeout: '登录连接超时，请稍后重试',
      network_error: '登录连接失败，请检查网络连接后重试',
      login_request_failed: '平台登录流程未完成，请稍后重试',
      login_task_interrupted: '上次登录任务未完成，请重新获取 AT',
      login_task_timeout: '本次登录执行超时，请重新获取 AT',
      login_result_not_saved: '登录结果未能保存，请重新获取 AT',
    };
    return { message: `${messages[access.failure.code]}${access.failure.httpStatus ? ` · HTTP ${access.failure.httpStatus}` : ''}`, tone: 'error' };
  }
  switch (access.status) {
    case 'ready': return { message: 'AT 已保存，登录态可复用', tone: 'success' };
    case 'verifying': return { message: '正在获取 AT，请稍候', tone: 'warning' };
    case 'missing_credentials': return { message: '请补全账号密码和有效 2FA 后获取 AT', tone: 'warning' };
    case 'invalid_login': return { message: '登录验证未通过，请核对密码和 2FA', tone: 'error' };
    case 'unavailable': return { message: '登录服务暂不可用，请稍后重试', tone: 'error' };
    case 'session_expired': return { message: 'AT 已到期，请先获取 AT', tone: 'warning' };
    default: return { message: '未获取到 AT，登录流程未完成，请重试', tone: 'error' };
  }
}

export function latestAccountFeedback(login?: AccountFeedback, probe?: AccountFeedback) {
  return (probe?.updatedAt ?? 0) > (login?.updatedAt ?? 0) ? probe : login ?? probe;
}
