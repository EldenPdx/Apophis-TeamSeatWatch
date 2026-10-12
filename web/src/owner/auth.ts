import createClient from 'openapi-fetch';
import type { components, paths } from '../generated/owner';

const ownerApi = createClient<paths>({
  baseUrl: '',
  credentials: 'include',
});

type LoginCredentials = components['schemas']['LoginRequest'];
type AuthStatus = components['schemas']['AuthStatus'];

type OwnerApiResponse = {
  status: number;
  body: unknown;
};

export class OwnerApiError extends Error implements OwnerApiResponse {
  readonly status: number;
  readonly body: unknown;

  constructor(status: number, body: unknown) {
    super('owner_api_request_failed');
    this.name = 'OwnerApiError';
    this.status = status;
    this.body = body;
  }
}

let csrfToken = '';
let csrfRequest: Promise<string> | undefined;

async function ensureCsrf(): Promise<string> {
  if (csrfToken) return csrfToken;
  if (!csrfRequest) {
    csrfRequest = (async () => {
      const response = await ownerApi.GET('/api/owner/v1/csrf');
      if (response.error || !response.data) {
        throw new OwnerApiError(response.response.status, response.error);
      }
      csrfToken = response.data.token;
      return csrfToken;
    })().finally(() => {
      csrfRequest = undefined;
    });
  }
  return csrfRequest;
}

export function mutationHeaders() {
  return ensureCsrf().then((token) => ({ 'X-CSRF-Token': token }));
}

function throwIfFailed(response: { error?: unknown; response: Response }): void {
  if (response.error) {
    throw new OwnerApiError(response.response.status, response.error);
  }
}

export async function getAuthStatus(): Promise<AuthStatus> {
  const response = await ownerApi.GET('/api/owner/v1/auth-status');
  throwIfFailed(response);
  if (!response.data) {
    throw new OwnerApiError(response.response.status, undefined);
  }
  return response.data;
}

export async function loginOwner(credentials: LoginCredentials): Promise<void> {
  const response = await ownerApi.POST('/api/owner/v1/login', {
    params: { header: await mutationHeaders() },
    body: credentials,
  });
  throwIfFailed(response);
}

export async function refreshOwnerSession(): Promise<void> {
  const response = await ownerApi.POST('/api/owner/v1/session/refresh', {
    params: { header: await mutationHeaders() },
  });
  throwIfFailed(response);
}

export type AccountProbeFilter = components['parameters']['TargetProbeStatusFilter'];
export type AccountTokenFilter = components['parameters']['TargetTokenStatusFilter'];

export async function listChildMaterials(page: number, search: string, probeStatus?: AccountProbeFilter, pageSize = 20, domain = '', signal?: AbortSignal, tokenStatus?: AccountTokenFilter, batchFilters?: { membershipStatus?: components['parameters']['StandbyMembershipFilter']; batchId?: string; excludeBatchId?: string }): Promise<components['schemas']['TargetAccountList']> {
  const response = await ownerApi.GET('/api/owner/v1/target-accounts', {
    ...(signal ? { signal } : {}),
    params: { query: { page, page_size: pageSize, sort: 'created_desc', ...(search ? { search } : {}), ...(domain ? { domain } : {}), ...(probeStatus ? { probe_status: probeStatus } : {}), ...(tokenStatus ? { token_status: tokenStatus } : {}), ...(batchFilters?.membershipStatus ? { membership_status: batchFilters.membershipStatus } : {}), ...(batchFilters?.batchId ? { standby_batch_id: batchFilters.batchId } : {}), ...(batchFilters?.excludeBatchId ? { exclude_standby_batch_id: batchFilters.excludeBatchId } : {}) } },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function resolveFilteredChildMaterials(search: string, probeStatus?: AccountProbeFilter, domain = '', tokenStatus?: AccountTokenFilter): Promise<components['schemas']['TargetAccount'][]> {
  const accounts: components['schemas']['TargetAccount'][] = [];
  for (let page = 1; ; page++) {
    // The Owner API caps page_size at 100. Filtered actions still walk every
    // page, so use the largest accepted page rather than failing with a 400.
    const result = await listChildMaterials(page, search, probeStatus, 100, domain, undefined, tokenStatus);
    accounts.push(...result.items);
    if (result.items.length === 0 || page * result.pageSize >= result.total) return accounts;
  }
}

export async function importChildMaterials(content: string): Promise<components['schemas']['ChildMaterialsImportResult']> {
  const response = await ownerApi.POST('/api/owner/v1/child-materials/import', {
    params: { header: await mutationHeaders() }, body: { content },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function updateChildMaterial(account: components['schemas']['TargetAccount'], password: string, totpSecret: string): Promise<void> {
  const response = await ownerApi.PATCH('/api/owner/v1/target-accounts/{targetAccountId}', {
    params: { path: { targetAccountId: account.id }, header: { ...(await mutationHeaders()), 'If-Match': `"${account.version}"` } },
    body: { displayLabel: account.displayLabel, status: account.status, ...(password ? { password } : {}), ...(totpSecret.trim() ? { totpSecret: totpSecret.trim() } : {}) },
  });
  throwIfFailed(response);
}

export async function exportChildMaterials(scope: 'selected' | 'filtered', accountIds: string[], search: string, expectedCount: number): Promise<string> {
  const body = scope === 'selected'
    ? { scope, accountIds, expectedCount, confirmed: true }
    : { scope, search, expectedCount, confirmed: true };
  const response = await ownerApi.POST('/api/owner/v1/child-materials/export', {
    parseAs: 'text',
    params: { header: await mutationHeaders() }, body,
  });
  throwIfFailed(response);
  if (typeof response.data !== 'string') throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function getTargetPersonalAccess(targetAccountId: string): Promise<components['schemas']['TargetPersonalAccess']> {
  const response = await ownerApi.GET('/api/owner/v1/target-accounts/{targetAccountId}/personal-session', { params: { path: { targetAccountId } } });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function refreshTargetPersonalAccess(targetAccountId: string): Promise<components['schemas']['TargetPersonalAccess']> {
  const response = await ownerApi.POST('/api/owner/v1/target-accounts/{targetAccountId}/personal-session', { params: { path: { targetAccountId }, header: await mutationHeaders() } });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

type PersonalScope = components['schemas']['PersonalProbeScope'];

export async function previewPersonalProbes(scope: PersonalScope): Promise<components['schemas']['PersonalProbePreview']> {
  const response = await ownerApi.POST('/api/owner/v1/personal-probes/preview', {
    params: { header: await mutationHeaders() }, body: scope,
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function createPersonalProbes(scope: PersonalScope, count: number, scopeToken: string, requestKey: string, concurrency = 1): Promise<components['schemas']['PersonalProbeBatch']> {
  const response = await ownerApi.POST('/api/owner/v1/personal-probes', {
    params: { header: await mutationHeaders() },
    body: { ...scope, expectedCount: count, confirmed: true, requestKey, scopeToken, concurrency },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function getPersonalProbes(batchId: string): Promise<components['schemas']['PersonalProbeBatch']> {
  const response = await ownerApi.GET('/api/owner/v1/personal-probes/{batchId}', { params: { path: { batchId } } });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function getPersonalProbesByRequest(requestKey: string): Promise<components['schemas']['PersonalProbeBatch']> {
  const response = await ownerApi.GET('/api/owner/v1/personal-probes/request/{requestKey}', { params: { path: { requestKey } } });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function cancelPersonalProbes(batchId: string): Promise<components['schemas']['PersonalProbeBatch']> {
  const response = await ownerApi.DELETE('/api/owner/v1/personal-probes/{batchId}', {
    params: { path: { batchId }, header: await mutationHeaders() },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function listMotherAccounts(search = '', page = 1, pageSize = 20, signal?: AbortSignal): Promise<components['schemas']['MotherAccountList']> {
  const query = search ? { page, page_size: pageSize, search } : { page, page_size: pageSize };
  const response = await ownerApi.GET('/api/owner/v1/mother-accounts', {
    ...(signal ? { signal } : {}),
    params: { query },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function getMotherAccount(accountId: string, signal?: AbortSignal): Promise<components['schemas']['MotherAccount']> {
 const response = await ownerApi.GET('/api/owner/v1/mother-accounts/{accountId}', { ...(signal ? { signal } : {}), params: { path: { accountId } } });
 throwIfFailed(response);
 if (!response.data) throw new OwnerApiError(response.response.status, undefined);
 return response.data;
}

export async function deleteMotherAccount(accountId: string): Promise<void> {
  const response = await ownerApi.DELETE('/api/owner/v1/mother-accounts/{accountId}', {
    params: { path: { accountId }, header: await mutationHeaders() },
    body: { confirmed: true },
  });
  throwIfFailed(response);
}

export async function listAllMotherAccounts(): Promise<components['schemas']['MotherAccount'][]> {
  const items: components['schemas']['MotherAccount'][] = [];
  for (let page = 1; ; page++) {
    const response = await ownerApi.GET('/api/owner/v1/mother-accounts', { params: { query: { page, page_size: 100 } } });
    throwIfFailed(response);
    if (!response.data) throw new OwnerApiError(response.response.status, undefined);
    items.push(...response.data.items);
    if (items.length >= response.data.total || response.data.items.length === 0) return items;
  }
}

export async function getMotherPersonalAccess(accountId: string, signal?: AbortSignal): Promise<components['schemas']['MotherPersonalAccess']> {
  const response = await ownerApi.GET('/api/owner/v1/mother-accounts/{accountId}/personal-session', { params: { path: { accountId } }, ...(signal ? { signal } : {}) });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function refreshMotherPersonalAccess(accountId: string): Promise<components['schemas']['MotherPersonalAccess']> {
  const response = await ownerApi.POST('/api/owner/v1/mother-accounts/{accountId}/personal-session', { params: { path: { accountId }, header: await mutationHeaders() } });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function getSelectedWorkspaceAccess(workspaceId: string, motherAccountId: string, signal?: AbortSignal): Promise<components['schemas']['SelectedWorkspaceAccessStatus']> {
  const response = await ownerApi.GET('/api/owner/v1/workspaces/{workspaceId}/access', {
    ...(signal ? { signal } : {}),
    params: { path: { workspaceId }, query: { motherAccountId } },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function exchangeSelectedWorkspaceToken(workspaceId: string, motherAccountId: string): Promise<components['schemas']['SelectedWorkspaceAccessStatus']> {
  const response = await ownerApi.POST('/api/owner/v1/workspaces/{workspaceId}/access', {
    params: { path: { workspaceId }, header: await mutationHeaders() },
    body: { motherAccountId, confirmed: true },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function getSelectedWorkspaceVerification(workspaceId: string, motherAccountId: string, signal?: AbortSignal, page: { page: number; pageSize: number; kind: 'member' | 'pending_invite' } = { page: 1, pageSize: 20, kind: 'member' }): Promise<components['schemas']['SelectedWorkspaceVerification']> {
  const response = await ownerApi.GET('/api/owner/v1/workspaces/{workspaceId}/verification', {
    ...(signal ? { signal } : {}),
    params: { path: { workspaceId }, query: { motherAccountId, page: page.page, page_size: page.pageSize, entry_kind: page.kind } },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function verifySelectedWorkspace(workspaceId: string, motherAccountId: string): Promise<components['schemas']['SelectedWorkspaceVerification']> {
  const response = await ownerApi.POST('/api/owner/v1/workspaces/{workspaceId}/verification', {
    params: { path: { workspaceId }, header: await mutationHeaders() },
    body: { motherAccountId, confirmed: true },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function getMotherDiscovery(accountId: string, signal?: AbortSignal): Promise<components['schemas']['MotherDiscovery']> {
  const response = await ownerApi.GET('/api/owner/v1/mother-accounts/{accountId}/discovery', { params: { path: { accountId } }, ...(signal ? { signal } : {}) });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function runMotherDiscovery(accountId: string): Promise<components['schemas']['MotherDiscovery']> {
  const response = await ownerApi.POST('/api/owner/v1/mother-accounts/{accountId}/discovery', { params: { path: { accountId }, header: await mutationHeaders() } });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function updateMotherAccountMaterial(account: components['schemas']['MotherAccount'], password: string, totpSecret: string): Promise<components['schemas']['MotherAccount']> {
  const response = await ownerApi.PATCH('/api/owner/v1/mother-accounts/{accountId}', {
    params: { path: { accountId: account.id }, header: { ...(await mutationHeaders()), 'If-Match': `"${account.version}"` } },
    body: { displayName: account.displayName, status: account.status, ...(password ? { password } : {}), ...(totpSecret.trim() ? { totpSecret: totpSecret.trim() } : {}) },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function importMotherAccounts(content: string): Promise<components['schemas']['MotherAccountImportResult']> {
  const response = await ownerApi.POST('/api/owner/v1/mother-accounts/import', {
    params: { header: await mutationHeaders() },
    body: { content },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function exportMotherAccounts(accountIds: string[] | undefined, expectedCount: number): Promise<string> {
  const body = accountIds ? { accountIds, expectedCount, confirmed: true } : { expectedCount, confirmed: true };
  const response = await ownerApi.POST('/api/owner/v1/mother-accounts/export', {
    parseAs: 'text',
    params: { header: await mutationHeaders() },
    body,
  });
  throwIfFailed(response);
  if (typeof response.data !== 'string') throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function logoutOwner(): Promise<void> {
  const response = await ownerApi.POST('/api/owner/v1/logout', {
    params: { header: await mutationHeaders() },
  });
  throwIfFailed(response);
}

export function clearCsrf(): void {
  csrfToken = '';
  csrfRequest = undefined;
}

export function ownerProblem(error: unknown): {
  status: number | undefined;
  code: string | undefined;
  retryAfterSeconds: number | undefined;
  actualCount: number | undefined;
} {
  if (!(error instanceof OwnerApiError)) {
    return { status: undefined, code: undefined, retryAfterSeconds: undefined, actualCount: undefined };
  }

  const body = typeof error.body === 'object' && error.body !== null
    ? error.body as { code?: unknown; retryAfterSeconds?: unknown; actualCount?: unknown }
    : {};

  return {
    status: error.status,
    code: typeof body.code === 'string' ? body.code : undefined,
    retryAfterSeconds: typeof body.retryAfterSeconds === 'number'
      ? body.retryAfterSeconds
      : undefined,
    actualCount: typeof body.actualCount === 'number' ? body.actualCount : undefined,
  };
}
