import { useActionFeedback } from '../shared/useActionFeedback';
import type { StatusTone } from '../shared/StatusBadge';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../generated/owner';
import { exchangeSelectedWorkspaceToken, getSelectedWorkspaceAccess, getSelectedWorkspaceVerification, ownerProblem, verifySelectedWorkspace, refreshMotherPersonalAccess, runMotherDiscovery, getMotherPersonalAccess } from './auth';
import { canShowSelectedWorkspaceFacts, createWorkspaceRequestGate } from './workspaceVerification';
import { workspaceConnectionFeedback, workspaceRequestFeedback, workspaceVerificationFeedback, connectSelectedWorkspace, WorkspaceRecoveryError, type WorkspaceSyncStage } from './workspaceConnection';

type Access = components['schemas']['SelectedWorkspaceAccessStatus'];
type Facts = components['schemas']['SelectedWorkspaceVerification'];
export type WorkspaceEntryKind = 'member' | 'pending_invite';

export function useSelectedWorkspace(motherId: string, workspaceId: string) {
  const [access, setAccess] = useState<Access | null>(null);
  const [facts, setFacts] = useState<Facts | null>(null);
  const [kind, setKind] = useState<WorkspaceEntryKind>('member');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState(false);
  const [syncStage, setSyncStage] = useState<WorkspaceSyncStage>('exchange');
  const [syncFailure, setSyncFailure] = useState('');
  const [motherRevision, setMotherRevision] = useState(0);
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [noticeTone, setNoticeTone] = useState<StatusTone>('error');
  const [now, setNow] = useState(Date.now());
  const gate = useRef(createWorkspaceRequestGate());
  const polling = useRef<AbortController | null>(null);

  useEffect(() => { const timer = window.setInterval(() => setNow(Date.now()), 30_000); return () => window.clearInterval(timer); }, []);
  useEffect(() => {
    const load = () => {
      const sequence = gate.current.beginPoll(); if (sequence === null) return;
      polling.current?.abort(); const controller = new AbortController(); polling.current = controller;
      setLoading(true);
      void Promise.all([
        getSelectedWorkspaceAccess(workspaceId, motherId, controller.signal),
        getSelectedWorkspaceVerification(workspaceId, motherId, controller.signal, { page, pageSize, kind }),
      ]).then(([nextAccess, nextFacts]) => {
        if (controller.signal.aborted || !gate.current.acceptPoll(sequence)) return;
        setAccess(nextAccess); setFacts(nextFacts); setNotice('');
      }).catch((error: unknown) => {
        if (controller.signal.aborted || !gate.current.acceptPoll(sequence)) return;
        setFacts(null); setNoticeTone('error'); setNotice(workspaceRequestFeedback(ownerProblem(error), '读取'));
      }).finally(() => { if (!controller.signal.aborted && gate.current.acceptPoll(sequence)) setLoading(false); });
    };
    load(); const timer = window.setInterval(load, 60_000);
    return () => { polling.current?.abort(); gate.current.invalidate(); window.clearInterval(timer); };
  }, [motherId, workspaceId, page, pageSize, kind]);

  async function sync() {
    if (pending) return;
    const sequence = gate.current.beginMutation(); polling.current?.abort(); setLoading(false); setPending(true); setNotice(''); setSyncFailure(''); setFacts(null);
    let recovered = false;
    try {
      const connect = (current: Access | null) => connectSelectedWorkspace({ motherId, workspaceId, access: current, exchange: exchangeSelectedWorkspaceToken, login: refreshMotherPersonalAccess, personal: getMotherPersonalAccess, discover: runMotherDiscovery, onStage: (stage) => { setSyncStage(stage); if (stage === 'login') { recovered = true; setAccess(null); } }, isCurrent: () => gate.current.isCurrentMutation(sequence) });
      let nextAccess = await connect(access);
      if (!gate.current.isCurrentMutation(sequence)) return;
      setAccess(nextAccess);
      if (nextAccess?.status !== 'ready') { setFacts(null); setNoticeTone('error'); setSyncFailure(`${recovered ? '自动重新登录后仍未完成空间连接：' : ''}${workspaceConnectionFeedback(nextAccess)}`); return; }
      setSyncStage('reading');
      let verified = await verifySelectedWorkspace(workspaceId, motherId);
      if (!gate.current.isCurrentMutation(sequence)) return;
      if (!recovered && verified.readSources?.some(source => source.outcome === 'unauthorized')) {
        nextAccess = await connect({ ...nextAccess, status: 'failed', failure: { code: 'workspace_session_expired', httpStatus: 401 } });
        if (!gate.current.isCurrentMutation(sequence)) return;
        setAccess(nextAccess);
        if (nextAccess.status !== 'ready') { setSyncFailure(`自动重新登录后仍未完成空间连接：${workspaceConnectionFeedback(nextAccess)}`); return; }
        setSyncStage('reading'); verified = await verifySelectedWorkspace(workspaceId, motherId);
      }
      if (!gate.current.isCurrentMutation(sequence)) return;
      const next = await getSelectedWorkspaceVerification(workspaceId, motherId, undefined, { page, pageSize, kind });
      if (!gate.current.isCurrentMutation(sequence)) return;
      setFacts(next); setNow(Date.now());
      setNoticeTone(next.status === 'verified' ? 'success' : next.status === 'permission_denied' ? 'error' : 'warning');
      if (next.status === 'verified') setNotice(workspaceVerificationFeedback(next)); else setSyncFailure(workspaceVerificationFeedback(next));
    } catch (error: unknown) {
      if (!gate.current.isCurrentMutation(sequence)) return;
      const problem = ownerProblem(error); setFacts(null);
      if (problem.code === 'workspace_token_required') setAccess(null);
      setNoticeTone('error'); setSyncFailure(error instanceof WorkspaceRecoveryError ? error.message : `${recovered ? '自动恢复未完成：' : ''}${workspaceRequestFeedback(problem, '同步')}`);
    } finally { if (gate.current.acceptMutation(sequence)) { setPending(false); if (recovered) setMotherRevision(value => value + 1); } }
  }
  return { access, facts, loading, pending, syncStage, syncFailure, motherRevision, notice, noticeTone, dismissNotice, sync, page, pageSize, kind, now,
    ready: canShowSelectedWorkspaceFacts(facts, access, now), setPage,
    setPageSize: (value: number) => { setPageSize(value); setPage(1); },
    setKind: (value: WorkspaceEntryKind) => { setKind(value); setPage(1); },
  };
}
