import createClient from 'openapi-fetch';
import type { components, paths } from '../generated/owner';
import { mutationHeaders, OwnerApiError } from './auth';

const api = createClient<paths>({ baseUrl: '', credentials: 'include' });
export type RemovalPreview = components['schemas']['RemovalPreview'];
export type RemovalOperation = components['schemas']['RemovalOperation'];
export type RotationState = components['schemas']['BatchRotationState'];

function result<T>(response: { data?: T; error?: unknown; response: Response }): T {
  if (response.error || response.data === undefined) throw new OwnerApiError(response.response.status, response.error);
  return response.data;
}
export const batchRotationApi = {
  preview: async (batchId: string, page = 1, pageSize = 20, signal?: AbortSignal, manual = false): Promise<RemovalPreview> => result(await api.GET('/api/owner/v1/batches/{batchId}/remove-preview', { ...(signal ? { signal } : {}), params: { path: { batchId }, query: { target_page: page, target_page_size: pageSize, ...(manual ? { manual } : {}) } } })),
  start: async (batchId: string, idempotencyKey: string, concurrency = 1, manual = false, membershipIds?: string[]): Promise<RemovalOperation> => result(await api.POST('/api/owner/v1/batches/{batchId}/remove', { params: { path: { batchId }, header: await mutationHeaders() }, body: { idempotencyKey, confirm: true, concurrency, ...(manual ? { manual } : {}), ...(membershipIds ? { membershipIds } : {}) } })),
  reconcile: async (batchId: string, idempotencyKey: string): Promise<RemovalOperation> => result(await api.POST('/api/owner/v1/batches/{batchId}/remove-reconcile', { params: { path: { batchId }, header: await mutationHeaders() }, body: { idempotencyKey } })),
  operation: async (batchId: string, targetPage = 1, targetPageSize = 20, signal?: AbortSignal): Promise<RemovalOperation> => result(await api.GET('/api/owner/v1/batches/{batchId}/remove-operation', { ...(signal ? { signal } : {}), params: { path: { batchId }, query: { target_page: targetPage, target_page_size: targetPageSize } } })),
};
