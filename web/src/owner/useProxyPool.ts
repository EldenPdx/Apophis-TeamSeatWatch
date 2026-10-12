import { useActionFeedback } from '../shared/useActionFeedback';
import { useEffect, useRef, useState } from 'react';
import type { StatusTone } from '../shared/StatusBadge';
import { ownerProblem } from './auth';
import { proxyPoolApi, type ProxyNode, type ProxyPool, type ProxyState, type ProxySourceFilter, diagnosticLabel, type SourceDiagnostic, type SourceDraft } from './proxyPool';

export default function useProxyPool(active: boolean) {
  const [pool, setPool] = useState<ProxyPool | null>(null);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [filterState, setFilterState] = useState<ProxyState | undefined>();
  const [filterSource, setFilterSource] = useState<ProxySourceFilter>();
  const [revision, setRevision] = useState(0);
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState<string | null>(null);
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [noticeTone, setNoticeTone] = useState<StatusTone>('error');
  const [dialog, setDialog] = useState<'source' | 'manual' | 'settings' | null>(null);
  const [draft, setDraft] = useState<SourceDraft>({ kind: 'direct' });
  const [diagnostic, setDiagnostic] = useState<SourceDiagnostic | null>(null);
  const [manual, setManual] = useState('');
  const [targetHealthy, setTargetHealthy] = useState(5);
  const [probeConcurrency, setProbeConcurrency] = useState(5);
  const [taskConcurrency, setTaskConcurrency] = useState(5);
  const [removing, setRemoving] = useState<ProxyNode | null>(null);
  const locked = useRef(false);
  const reads = useRef<AbortController | null>(null);
  const isActive = useRef(active); isActive.current = active;
  useEffect(() => {
    if (!active) return;
    const controller = new AbortController(); reads.current = controller;
    setLoading(true);
    void proxyPoolApi.get(page, pageSize, controller.signal, filterState, filterSource).then(next => {
      if (controller.signal.aborted) return;
      const lastPage = Math.max(1, Math.ceil(next.total / pageSize));
      if (page > lastPage) { setPage(lastPage); return; }
      setPool(next); setLoading(false);
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return;
      setPool(null); setLoading(false); setNoticeTone('error');
      setNotice(ownerProblem(error).status === 401 ? '登录已过期' : '代理池读取失败，请重试');
    });
    return () => controller.abort();
  }, [active, page, pageSize, revision, filterState, filterSource]);
  useEffect(() => {
    if (!active) return;
    const timer = window.setInterval(() => { if (!locked.current) setRevision(value => value + 1); }, 5000);
    return () => window.clearInterval(timer);
  }, [active]);
  function chooseSource(kind: SourceDraft['kind']) {
    const saved = pool?.sources.find(source => source.kind === kind);
    const provider = pool?.providers.find(source => source.kind === kind);
    if (kind === 'direct') { setDraft({ kind }); setDiagnostic(null); return; }
    setDraft(saved ? { ...saved.config } : { kind, label: '', host: provider?.host ?? '', port: provider?.port ?? 443, protocol: 'socks5h', country: 'US', state: '', city: '', sessionType: 'sticky', sessionMinutes: 120, updateSeconds: 300 });
    setDiagnostic(null);
  }
  function changeDraft(update: Partial<SourceDraft>) { setDraft(value => ({ ...value, ...update })); setDiagnostic(null); }
  function failed(error: unknown) {
    const problem = ownerProblem(error); setNoticeTone('error');
    setNotice(problem.status === 401 ? '登录已过期' : problem.code === 'proxy_node_unavailable' ? '该节点已退役或不可用，请刷新列表' : problem.code === 'proxy_node_in_use' ? '该代理正在使用，结束后再移除' : problem.code === 'invalid_proxy_node' ? '代理格式无效，请检查地址和端口' : problem.code === 'invalid_proxy_source' ? '来源参数无效，请检查账密、网关、地区和会话时长' : '操作未完成，请重试');
  }
  async function perform(key: string, action: () => Promise<ProxyPool>, success: string) {
    if (locked.current) return;
    locked.current = true; reads.current?.abort(); setLoading(false); setPending(key); setNotice('');
    try {
      const next = await action();
      const { nodes: _nodes, total: _total, page: _page, pageSize: _pageSize, ...metadata } = next;
      setPool(current => current ? { ...current, ...metadata } : next);
      if (key === 'source') { setDraft(value => { const { username: _username, password: _password, url: _url, ...config } = value; return config; }); setDialog(null); }
      if (key === 'settings') setDialog(null);
      if (key === 'import') { setManual(''); setDialog(null); }
      if (key.startsWith('remove:')) setRemoving(null);
      const verification = next.verification;
      const failure = verification?.diagnostics.find(step => !!step.code);
      const feedback = verification ? [
        verification.removedCount ? `已移除 ${verification.removedCount} 个不可用代理` : '',
        verification.retryCount ? `${verification.retryCount} 个代理暂时未通过，${next.mode === 'direct' ? '可重新验证' : '等待重试'}` : '',
        failure ? diagnosticLabel(failure) : '',
      ].filter(Boolean).join(' · ') || success : success;
      setNotice(feedback); setNoticeTone(verification && (verification.removedCount || verification.retryCount || failure) ? 'warning' : 'success');
    } catch (error: unknown) { failed(error); }
    finally { locked.current = false; setPending(null); if (isActive.current) setRevision(value => value + 1); }
  }
  async function testSource() {
    if (locked.current) return;
    locked.current = true; setPending('test'); setDiagnostic(null); setNotice('');
    try { setDiagnostic(await proxyPoolApi.test(draft)); }
    catch (error: unknown) { failed(error); }
    finally { locked.current = false; setPending(null); }
  }
  const provider = pool?.providers.find(item => item.kind === draft.kind);
  const savedSource = pool?.sources.find(item => item.kind === draft.kind);
  const supplier = !!provider;
  const credentials = (!!draft.username?.trim() || savedSource?.usernameSet) && (!!draft.password || savedSource?.passwordSet);
  const validSource = draft.kind === 'direct' || (draft.kind === 'subscription' ? (!!draft.url?.trim() || !!savedSource) && Number.isInteger(draft.updateSeconds) && Number(draft.updateSeconds) >= 60 && Number(draft.updateSeconds) <= 86400 : !!provider && credentials && !!draft.host?.trim() && Number.isInteger(draft.port) && Number(draft.port) >= 1 && Number(draft.port) <= 65535 && /^[A-Z]{2}$/.test(draft.country ?? '') && (draft.sessionType === 'rotating' || Number.isInteger(draft.sessionMinutes) && Number(draft.sessionMinutes) >= provider.minMinutes && Number(draft.sessionMinutes) <= provider.maxMinutes));
  const validSettings = ([{ value: targetHealthy, max: 1000 }, { value: probeConcurrency, max: 100 }, { value: taskConcurrency, max: 100 }]).every(({ value, max }) => Number.isInteger(value) && value >= 1 && value <= max);
  return {
    pool, loading, pending, notice, noticeTone, dismissNotice, manual, setManual, draft, changeDraft, chooseSource, diagnostic, testSource, supplier, provider, savedSource, validSource,
    targetHealthy, setTargetHealthy, probeConcurrency, setProbeConcurrency, taskConcurrency, setTaskConcurrency, dialog, setDialog: (value: typeof dialog) => { setNotice(''); setDialog(value); },
    removing, setRemoving: (value: ProxyNode | null) => { setNotice(''); setRemoving(value); }, validSettings, setPage, filterState, filterSource,
    filterByState: (value: ProxyState | undefined) => { setPage(1); setFilterState(value); },
    filterBySource: (value: ProxySourceFilter) => { setPage(1); setFilterSource(value); },
    changePageSize: (value: number) => { setPage(1); setPageSize(value); },
    openSource: () => { setNotice(''); chooseSource(pool?.mode === 'direct' ? 'direct' : pool?.source?.kind ?? 'cliproxy'); setDialog('source'); },
    openSettings: () => { setNotice(''); if (pool) { setTargetHealthy(pool.targetHealthy); setProbeConcurrency(pool.probeConcurrency); setTaskConcurrency(pool.taskConcurrency); } setDialog('settings'); },
    refresh: () => setRevision(value => value + 1),
    sync: () => perform('sync', proxyPoolApi.sync, '已请求补齐，结果会在列表更新'),
    probe: () => perform('probe', proxyPoolApi.probe, '代理验证完成'),
    probeNode: (node: ProxyNode) => perform(`probe:${node.id}`, () => proxyPoolApi.probeNode(node.id), '该代理验证完成'),
    saveSource: () => perform('source', () => proxyPoolApi.source(draft), draft.kind === 'direct' ? '直连模式已保存并生效' : '来源已保存，正在自动补齐'),
    importNodes: () => perform('import', () => proxyPoolApi.importNodes(manual.split(/\r?\n/).map(item => item.trim()).filter(Boolean)), '代理已导入并验证'),
    saveSettings: () => perform('settings', () => proxyPoolApi.settings({ targetHealthy, probeConcurrency, taskConcurrency }), '运行参数已保存并生效'),
    remove: () => removing ? perform(`remove:${removing.id}`, () => proxyPoolApi.remove(removing.id), '代理已移除') : Promise.resolve(),
  };
}
