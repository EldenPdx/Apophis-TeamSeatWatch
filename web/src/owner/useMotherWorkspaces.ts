import { useActionFeedback } from '../shared/useActionFeedback';
import { useDebouncedValue } from '@mantine/hooks';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../generated/owner';
import {
  deleteMotherAccount, getMotherDiscovery, getMotherPersonalAccess, listMotherAccounts, ownerProblem,
  refreshMotherPersonalAccess, runMotherDiscovery,
} from './auth';
import { loginMotherAndObserve, motherLoginFailure } from './workspaceConnection';
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
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [deletionFailures, setDeletionFailures, dismissDeletionFailures] = useActionFeedback<Record<string, string>>({});
  const deletionLock = useRef(false);
  const recordsRef = useRef(records);
  recordsRef.current = records;
  const sequences = useRef(new Map<string, number>());
  const locks = useRef(new Set<string>());
  const live = useRef(true);

  useEffect(() => { live.current = true; return () => { live.current = false; }; }, []);
  useEffect(() => {
    if (!active) return;
    setLoading(true); setReadFailed(false);
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
    publish(account.id, sequence, { ...(recordsRef.current[account.id] ?? emptyRecord()), phase: 'reading' });
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
    // Only enrich the visible server page. Three records at a time use at most
    // six reads, and changing the page cancels every outstanding page read.
    const enrich = async (accounts: MotherAccount[]) => {
      let cursor = 0;
      const worker = async () => {
        while (!controller.signal.aborted) {
          const account = accounts[cursor++];
          if (!account) return;
          await read(account, controller.signal);
        }
      };
      await Promise.all(Array.from({ length: Math.min(3, accounts.length) }, worker));
    };
    let polling = true;
    void enrich(items).finally(() => { polling = false; });
    const timer = window.setInterval(() => {
      if (polling || controller.signal.aborted) return;
      const pending = items.filter(account => {
        const record = recordsRef.current[account.id];
        return !locks.current.has(account.id) && !record?.phase && (record?.access?.status === 'verifying' || record?.discovery?.status === 'discovering');
      });
      if (!pending.length) return;
      polling = true;
      void enrich(pending).finally(() => { polling = false; });
    }, 2000);
    return () => { controller.abort(); window.clearInterval(timer); };
  }, [active, items, loading]);

  async function discover(account: MotherAccount, login: boolean) {
    if (account.status !== 'active' || account.materialStatus !== 'complete' || locks.current.has(account.id)) return;
    locks.current.add(account.id);
    const sequence = begin(account.id);
    publish(account.id, sequence, { ...emptyRecord(), phase: login ? 'login' : 'discovering' });
    let access: Access | null = null;
    try {
      access = login ? await loginMotherAndObserve({
        motherId: account.id, login: refreshMotherPersonalAccess, personal: getMotherPersonalAccess,
        isCurrent: () => live.current && sequences.current.get(account.id) === sequence,
        onAccess: (current) => publish(account.id, sequence, { access: current, discovery: null, phase: 'login', notice: '' }),
      }) : await getMotherPersonalAccess(account.id);
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

  async function remove(account: MotherAccount): Promise<boolean> {
    if (deletionLock.current) return false;
    deletionLock.current = true;
    locks.current.add(account.id); begin(account.id);
    setRecords((current) => ({ ...current, [account.id]: { ...(current[account.id] ?? emptyRecord()), phase: null } }));
    setDeletingId(account.id); setNotice('');
    setDeletionFailures((current) => { const next = { ...current }; delete next[account.id]; return next; });
    try {
      await deleteMotherAccount(account.id);
      if (!live.current) return true;
      setItems((current) => current.filter((item) => item.id !== account.id));
      setRecords((current) => { const next = { ...current }; delete next[account.id]; return next; });
      setNotice(`已删除母号 ${account.loginIdentifier}`);
      setRevision((current) => current + 1);
      return true;
    } catch (error: unknown) {
      const problem = ownerProblem(error);
      const message = problem.status === 401 ? '登录已失效，请重新登录。' : problem.status === 404 ? '母号已删除，请刷新列表。' : '删除未完成，请重试。';
      if (live.current) setDeletionFailures((current) => ({ ...current, [account.id]: message }));
      return false;
    } finally {
      locks.current.delete(account.id); deletionLock.current = false;
      if (live.current) setDeletingId(null);
    }
  }

  return {
    items, total, page, pageSize, search, loading, readFailed, notice, dismissNotice, records, read,
    remove, deletingId, deletionFailures, dismissDeletionFailures,
    connect: (account: MotherAccount) => discover(account, true),
    rediscover: (account: MotherAccount) => discover(account, false),
    setSearch: (value: string) => { setNotice(''); setSearch(value); setPage(1); },
    setPage: (value: number) => { setNotice(''); setPage(value); },
    setPageSize: (value: number) => { setNotice(''); setPageSize(value); setPage(1); },
    refresh: () => { setNotice(''); setRevision((value) => value + 1); },
    imported: () => { setNotice(''); setSearch(''); setPage(1); setRevision((value) => value + 1); },
  };
}
