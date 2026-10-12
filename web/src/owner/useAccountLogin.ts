import { useActionFeedback } from '../shared/useActionFeedback';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../generated/owner';
import { getTargetPersonalAccess, listChildMaterials, ownerProblem, refreshTargetPersonalAccess } from './auth';
import { accountNetworkPrerequisite } from './accountActionPrerequisites';
import { hasUsablePersonalAccess, personalAccessFeedback, type AccountFeedback } from './accountSession';
import { AccountATBatchRunner, restoreATBatch, type ATBatch } from './accountATBatch';

type Account = components['schemas']['TargetAccount'];
const batchStorageKey = 'owner-account-at-batch';

function storedBatch() {
  try { return restoreATBatch(sessionStorage.getItem(batchStorageKey)); } catch { return null; }
}

export function useAccountLogin(onResult: () => void, concurrency = 1, capacity = 100) {
  const [pendingIds, setPendingIds] = useState<ReadonlySet<string>>(new Set());
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [feedback, setFeedback] = useActionFeedback<ReadonlyMap<string, AccountFeedback>>(new Map());
  const [batch, setBatch] = useState<ATBatch | null>(storedBatch);
  const [stopping, setStopping] = useState(false);
  const locks = useRef(new Set<string>());
  const batchLock = useRef(false);
  const runner = useRef<AccountATBatchRunner | null>(null);
  const live = useRef(true);
  const refreshTimer = useRef<number | undefined>(undefined);
  const onResultRef = useRef(onResult);
  onResultRef.current = onResult;

  function updateBatch(next: ATBatch) {
    try { sessionStorage.setItem(batchStorageKey, JSON.stringify(next)); } catch { /* API results stay saved even if browser storage is unavailable. */ }
    if (!live.current) return;
    setBatch(next);
    if (next.status !== 'running') { setStopping(false); onResultRef.current(); }
    else if (next.processed > 0 && refreshTimer.current === undefined) {
      refreshTimer.current = window.setTimeout(() => { refreshTimer.current = undefined; onResultRef.current(); }, 1500);
    }
  }

  useEffect(() => {
    live.current = true;
    return () => { live.current = false; runner.current?.pause(); window.clearTimeout(refreshTimer.current); };
  }, []);

  async function startAll(previous?: ATBatch) {
    if (batchLock.current || locks.current.size) return;
    batchLock.current = true; setNotice(''); setStopping(false);
    let prerequisite: ReturnType<typeof accountNetworkPrerequisite> | undefined;
    const execution = new AccountATBatchRunner({
      listPage: page => listChildMaterials(page, '', undefined, 100),
      beforeAcquire: async id => {
        prerequisite ??= accountNetworkPrerequisite();
        const blocked = await prerequisite;
        if (blocked) setFeedback(current => new Map(current).set(id, { ...blocked, updatedAt: Date.now() }));
        return blocked?.message;
      },
      acquire: async id => {
        const access = await refreshTargetPersonalAccess(id);
        setFeedback(current => new Map(current).set(id, { ...personalAccessFeedback(access), updatedAt: Date.now() }));
        return access;
      },
      readAccess: getTargetPersonalAccess,
    }, updateBatch);
    runner.current = execution;
    try { await execution.run(previous, concurrency, capacity); }
    finally { batchLock.current = false; runner.current = null; }
  }

  function stopAll() { setStopping(true); runner.current?.stop(); }
  function dismissBatch() { if (batchLock.current) return; setBatch(null); sessionStorage.removeItem(batchStorageKey); }

  async function login(account: Account) {
    if (batchLock.current || locks.current.size || account.status !== 'active') return;
    locks.current.add(account.id); setPendingIds(new Set(locks.current)); setNotice('');
    const report = (result: AccountFeedback) => setFeedback(current => new Map(current).set(account.id, { ...result, updatedAt: Date.now() }));
    report({ message: '正在检查登录态', tone: 'warning' });
    const startedAt = Date.now();
    const observe = async (access: Awaited<ReturnType<typeof getTargetPersonalAccess>>) => {
      report(personalAccessFeedback(access));
      while (access.status === 'verifying' && live.current) {
        await new Promise(resolve => window.setTimeout(resolve, 2000));
        if (!live.current) return;
        access = await getTargetPersonalAccess(account.id);
        report(personalAccessFeedback(access));
      }
      if (live.current) onResultRef.current();
    };
    try {
      const saved = await getTargetPersonalAccess(account.id);
      if (hasUsablePersonalAccess(saved)) { report({ message: '已复用有效 AT，无需重新登录', tone: 'success' }); onResultRef.current(); return; }
      if (saved.status === 'verifying') { await observe(saved); return; }
      if (!account.hasPassword || !account.hasTotp) { report({ message: '请补全账号密码和有效 2FA 后获取 AT', tone: 'warning' }); return; }
      const blocked = await accountNetworkPrerequisite();
      if (blocked) { report(blocked); return; }
      report({ message: '正在获取 AT，优先复用登录态', tone: 'warning' });
      await observe(await refreshTargetPersonalAccess(account.id));
    } catch (error: unknown) {
      const status = ownerProblem(error).status;
      if (status !== 401 && status !== 403 && status !== 409) {
        // The service owns an admitted reservation even if its POST response is
        // lost. Read that attempt; never replay the mutation while it verifies.
        try {
          const access = await getTargetPersonalAccess(account.id);
          if (hasUsablePersonalAccess(access) || access.status === 'verifying' ||
              access.checkedAt && Date.parse(access.checkedAt) >= startedAt - 1000) {
            await observe(access); return;
          }
        } catch { /* Keep an uncertain response distinct from a confirmed result. */ }
      }
      report({ message: status === 401 ? '管理端登录已过期' : status === 409 ? '账号资料已变化，请刷新后重试' : '获取 AT 失败，请检查网络连接后重试', tone: 'error' });
      onResultRef.current();
    } finally { locks.current.delete(account.id); setPendingIds(new Set(locks.current)); }
  }

  const processingIds = new Set(pendingIds);
  if (batch?.status === 'running') for (const item of batch.inFlight) processingIds.add(item.id);
  return { login, startAll, stopAll, dismissBatch, batch, stopping, pendingIds: processingIds, feedback, notice, dismissNotice, busy: pendingIds.size > 0 || batch?.status === 'running' };
}
