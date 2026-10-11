import { useActionFeedback } from '../shared/useActionFeedback';
import { useDebouncedValue } from '@mantine/hooks';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../generated/owner';
import {
  getMotherDiscovery, getMotherPersonalAccess, listMotherAccounts, ownerProblem,
  refreshMotherPersonalAccess, runMotherDiscovery,
} from './auth';
import { motherLoginFailure } from './workspaceConnection';
import { canDiscover } from './workspaceSelection';

export type MotherAccount = components['schemas']['MotherAccount'];
type Access = components['schemas']['MotherPersonalAccess'];
type Discovery = components['schemas']['MotherDiscovery'];
export type MotherWorkspaceRecord = {
  access: Access | null;
  discovery: Discovery | null;
  phase: 'reading' | 'login' | 'discovering' | null;
  notice: string;
};
const emptyRecord = (): MotherWorkspaceRecord => ({ access: null, discovery: null, phase: null, notice: '' });

export function useMotherWorkspaces(active: boolean) {
  const [items, setItems] = useState<MotherAccount[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [search, setSearch] = useState('');
  const [debouncedSearch] = useDebouncedValue(search, 300);
  const [revision, setRevision] = useState(0);
  const [loading, setLoading] = useState(true);
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [readFailed, setReadFailed] = useState(false);
  const [records, setRecords] = useState<Record<string, MotherWorkspaceRecord>>({});
  const sequences = useRef(new Map<string, number>());
  const locks = useRef(new Set<string>());
  const live = useRef(true);

  useEffect(() => { live.current = true; return () => { live.current = false; }; }, []);
  useEffect(() => {
    if (!active) return;
    setLoading(true); setReadFailed(false); setNotice('');
    if (search !== debouncedSearch) return;
    const controller = new AbortController();
    void listMotherAccounts(debouncedSearch.trim(), page, pageSize, controller.signal).then((result) => {
      if (controller.signal.aborted) return;
      const lastPage = Math.max(1, Math.ceil(result.total / pageSize));
      if (page > lastPage) { setPage(lastPage); return; }
      setItems(result.items); setTotal(result.total); setLoading(false);
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return;
      setNotice(ownerProblem(error).status === 401 ? '登录已失效，请重新登录。' : '母号列表读取失败，请刷新重试。');
      setItems([]); setTotal(0); setLoading(false); setReadFailed(true);
    });
    return () => controller.abort();
  }, [active, page, pageSize, search, debouncedSearch, revision]);

  function begin(id: string) {
    const sequence = (sequences.current.get(id) ?? 0) + 1;
    sequences.current.set(id, sequence);
    return sequence;
  }
  function publish(id: string, sequence: number, record: MotherWorkspaceRecord) {
    if (live.current && sequences.current.get(id) === sequence) setRecords((current) => ({ ...current, [id]: record }));
  }
  async function read(account: MotherAccount, signal?: AbortSignal) {
    if (account.status !== 'active' || locks.current.has(account.id)) return;
    const sequence = begin(account.id);
    publish(account.id, sequence, { ...emptyRecord(), phase: 'reading' });
    try {
      const [accessResult, discoveryResult] = await Promise.allSettled([getMotherPersonalAccess(account.id, signal), getMotherDiscovery(account.id, signal)]);
      if (signal?.aborted) return;
      let access = accessResult.status === 'fulfilled' ? accessResult.value : null;
      const discovery = discoveryResult.status === 'fulfilled' ? discoveryResult.value : null;
      if ((access && access.motherAccountId !== account.id) || (discovery && discovery.motherAccountId !== account.id)) throw new Error('mother_scope_changed');
      if (discovery && ['session_expired', 'missing_credentials', 'unavailable'].includes(discovery.status)) access = null;
      const failures = [accessResult, discoveryResult].filter((result) => result.status === 'rejected');
      const unauthorized = failures.some((result) => result.status === 'rejected' && ownerProblem(result.reason).status === 401);
      publish(account.id, sequence, { access, discovery, phase: null, notice: unauthorized ? '登录已失效，请重新登录。' : failures.length ? '部分状态读取失败，请重试。' : access && ['invalid_login', 'missing_credentials', 'refresh_failed', 'unavailable'].includes(access.status) ? motherLoginFailure(access) : '' });
    } catch (error: unknown) {
      if (!signal?.aborted) publish(account.id, sequence, { ...emptyRecord(), notice: ownerProblem(error).status === 401 ? '登录已失效，请重新登录。' : '状态读取失败，请重试。' });
    }
  }

  useEffect(() => {
    if (!active || loading) return;
    const controller = new AbortController();
    const pageIds = new Set(items.map((item) => item.id));
    setRecords((current) => Object.fromEntries(Object.entries(current).filter(([id]) => pageIds.has(id) || locks.current.has(id))));
    let cursor = 0;
    // Only enrich the visible server page. Three records at a time use at most
    // six reads, and changing the page cancels every outstanding page read.
    const worker = async () => {
      while (!controller.signal.aborted) {
        const account = items[cursor++];
        if (!account) return;
        await read(account, controller.signal);
      }
    };
    void Promise.all(Array.from({ length: Math.min(3, items.length) }, worker));
    return () => controller.abort();
  }, [active, items, loading]);

  async function discover(account: MotherAccount, login: boolean) {
    if (account.status !== 'active' || account.materialStatus !== 'complete' || locks.current.has(account.id)) return;
    locks.current.add(account.id);
    const sequence = begin(account.id);
    publish(account.id, sequence, { ...emptyRecord(), phase: login ? 'login' : 'discovering' });
    let access: Access | null = null;
    try {
      access = login ? await refreshMotherPersonalAccess(account.id) : await getMotherPersonalAccess(account.id);
      if (!live.current) return;
      if (access.motherAccountId !== account.id) throw new Error('mother_scope_changed');
      if (!canDiscover(access)) {
        publish(account.id, sequence, { access, discovery: null, phase: null, notice: '' });
        return;
      }
      publish(account.id, sequence, { access, discovery: null, phase: 'discovering', notice: '' });
      const discovery = await runMotherDiscovery(account.id);
      if (discovery.motherAccountId !== account.id) throw new Error('mother_scope_changed');
      if (['session_expired', 'missing_credentials', 'unavailable'].includes(discovery.status)) access = null;
      publish(account.id, sequence, { access, discovery, phase: null, notice: '' });
    } catch (error: unknown) {
      const problem = ownerProblem(error);
      publish(account.id, sequence, { access, discovery: null, phase: null, notice: problem.status === 401 ? '登录已失效，请重新登录。' : problem.status === 409 ? '母号资料已变化，请刷新后重试。' : access ? '空间发现失败，请重试。' : '母号登录失败，请重试。' });
    } finally { locks.current.delete(account.id); }
  }

  return {
    items, total, page, pageSize, search, loading, readFailed, notice, dismissNotice, records, read,
    connect: (account: MotherAccount) => discover(account, true),
    rediscover: (account: MotherAccount) => discover(account, false),
    setSearch: (value: string) => { setSearch(value); setPage(1); },
    setPage, setPageSize: (value: number) => { setPageSize(value); setPage(1); },
    refresh: () => setRevision((value) => value + 1),
    imported: () => { setSearch(''); setPage(1); setRevision((value) => value + 1); },
  };
}
