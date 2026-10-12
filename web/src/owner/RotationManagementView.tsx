import ActionNotice from '../shared/ActionNotice';
import { useActionFeedback } from '../shared/useActionFeedback';
import TaskConcurrencyControl from './TaskConcurrencyControl';
import { Alert, Box, Button, Group, Modal, Paper, Select, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../generated/owner';
import { ownerProblem } from './auth';
import { batchOperationsApi, type Batch, type BatchList } from './batchOperations';
import { operationDraftApi } from './operationDraft';
import { useBatchRemoval } from './useBatchRemoval';
import { useMotherWorkspaceScope } from './useMotherWorkspaceScope';
import MotherWorkspaceControls from './MotherWorkspaceControls';
import ListPagination from '../shared/ListPagination';
import StatusBadge from '../shared/StatusBadge';
import OwnerIcon from './OwnerIcon';
import { memberLabels, removalLabels, removalReason, rotationLabels, rotationTime } from './rotationPresentation';

type Context = { motherAccountId: string; workspaceId: string; previousBatchId?: string };
type Props = { active?: boolean; focusBatchId?: string | null; onClearBatchScope?: () => void; onStartNextBatch: (context: Context) => void };
type FilterState = components['schemas']['BatchRotationState'] | '';
type RemovalTarget = components['schemas']['RemovalPreviewTarget'];

export default function RotationManagementView({ active = true, focusBatchId = null, onClearBatchScope, onStartNextBatch }: Props) {
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [readFailed, setReadFailed] = useState(false);
  const [bindingId, setBindingId] = useState('');
  const [batches, setBatches] = useState<BatchList>({ items: [], page: 1, pageSize: 20, total: 0 });
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState<FilterState>('');
  const [selectedId, setSelectedId] = useState('');
  const [selectedBatch, setSelectedBatch] = useState<Batch | null>(null);
  const [listLoading, setListLoading] = useState(false);
  const [revision, setRevision] = useState(0);
  const [confirm, setConfirm] = useState<true | RemovalTarget | null>(null);
  const [debouncedSearch] = useDebouncedValue(search, 300);
  const focusHandled = useRef<string | null>(null);
  const motherScope = useMotherWorkspaceScope(active, null, setNotice);
  const removal = useBatchRemoval(selectedId, active && Boolean(selectedId));
  const busy = listLoading || removal.loading || removal.pending;
  const detailVisible = Boolean(selectedId && selectedBatch);
  const currentScope = `${motherScope.motherId ?? ''}:${motherScope.workspaceId ?? ''}`;

  useEffect(() => {
    if (!active || !motherScope.workspaceId) { setBindingId(''); return; }
    const controller = new AbortController(); setReadFailed(false);
    void batchOperationsApi.workspace(motherScope.workspaceId, controller.signal).then((value) => {
      if (controller.signal.aborted) return;
      const binding = value.binding && value.binding.motherAccountId === motherScope.motherId ? value.binding : null;
      setBindingId(binding?.id ?? '');
      if (!binding) setNotice('当前母号在此空间还没有执行批次。');
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) { setBindingId(''); setReadFailed(true); setNotice(ownerProblem(error).status === 401 ? '登录已失效，请重新登录。' : '空间批次读取失败，请刷新重试。'); }
    });
    return () => controller.abort();
  }, [active, motherScope.motherId, motherScope.workspaceId, revision]);

  useEffect(() => {
    setSelectedId(''); setSelectedBatch(null); setPage(1); setBatches((value) => ({ ...value, items: [], total: 0 }));
  }, [currentScope]);

  useEffect(() => {
    if (!active || !bindingId || detailVisible || focusBatchId) return;
    const controller = new AbortController(); setReadFailed(false);
    setListLoading(true); setNotice('');
    void batchOperationsApi.list(bindingId, page, pageSize, controller.signal, false, {
      ...(debouncedSearch.trim() ? { search: debouncedSearch.trim() } : {}),
      ...(filter ? { rotation_state: filter } : {}),
    }).then((value) => { if (controller.signal.aborted) return; const last = Math.max(1, Math.ceil(value.total / pageSize)); if (page > last) { setPage(last); return; } setBatches(value); }).catch((error: unknown) => {
      if (!controller.signal.aborted) { setBatches({ items: [], page, pageSize, total: 0 }); setReadFailed(true); setNotice(ownerProblem(error).status === 401 ? '登录已失效，请重新登录。' : '轮转批次读取失败，请刷新重试。'); }
    }).finally(() => { if (!controller.signal.aborted) setListLoading(false); });
    return () => controller.abort();
  }, [active, bindingId, page, pageSize, debouncedSearch, filter, detailVisible, focusBatchId, revision]);

  useEffect(() => {
    if (!active || !bindingId || detailVisible || focusBatchId) return;
    const timer = window.setInterval(() => setRevision((value) => value + 1), 30_000);
    return () => window.clearInterval(timer);
  }, [active, bindingId, detailVisible, focusBatchId]);

  useEffect(() => {
    if (!active || !focusBatchId || focusHandled.current === focusBatchId) return;
    focusHandled.current = focusBatchId;
    const controller = new AbortController(); setReadFailed(false);
    setListLoading(true); setNotice('');
    void batchOperationsApi.batch(focusBatchId, 1, 20, controller.signal).then(async ({ batch }) => {
      if (controller.signal.aborted) return;
      await motherScope.chooseMother(batch.motherAccountId ?? '');
      if (controller.signal.aborted) return;
      motherScope.chooseWorkspace(batch.workspaceId);
      setSelectedId(batch.id); setSelectedBatch(batch); setBatches({ items: [batch], page: 1, pageSize: 20, total: 1 });
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setNotice(ownerProblem(error).status === 401 ? '登录已失效，请重新登录。' : '本轮轮转记录读取失败，请刷新重试。');
    }).finally(() => { if (!controller.signal.aborted) setListLoading(false); });
    return () => controller.abort();
  }, [active, focusBatchId, motherScope]);

  const preview = removal.preview;
  const operation = removal.operation;
  const batch = preview?.batch ?? selectedBatch ?? batches.items.find((item) => item.id === selectedId) ?? null;
  const allDone = batch?.rotation.state === 'completed';
  const canStart = Boolean(preview?.canProceed && (!operation || operation.status === 'succeeded') && !busy && removal.taskConcurrency.ready && batch && batch.rotation.joinedCount > batch.rotation.removedCount);
  const needsReview = operation && ['failed', 'blocked'].includes(operation.status);
  const title = batch ? `第 ${batch.sequenceNo} 批` : '轮转管理';

  function openBatch(item: Batch) { setSelectedBatch(item); setSelectedId(item.id); }
  function closeDetail() { setSelectedId(''); setSelectedBatch(null); setConfirm(null); focusHandled.current = null; setRevision((value) => value + 1); }

  return <Stack gap={24} className="management-page rotation-page">
    <Group className="management-subheader" justify="space-between"><Group gap={12}><Box className="management-page-icon"><OwnerIcon name="rotation" size={20} /></Box><Title order={2}>{detailVisible ? title : '轮转管理'}</Title></Group><Group gap={8}><Button variant="default" disabled={busy} onClick={() => { if (detailVisible) removal.refresh(); else setRevision((value) => value + 1); }}>刷新</Button>{detailVisible ? <Button variant="subtle" disabled={busy} onClick={closeDetail}>返回轮转列表</Button> : null}</Group></Group>
    {focusBatchId && detailVisible ? <Group gap={12}><StatusBadge tone="indigo" label="本轮轮转" /><Button variant="subtle" size="xs" onClick={() => { closeDetail(); onClearBatchScope?.(); }}>查看全部轮转</Button></Group> : null}
    <ActionNotice message={notice || removal.notice} onClose={() => { dismissNotice(); removal.dismissNotice(); }} />
    {!detailVisible ? <Paper withBorder radius={12} className="management-list-panel">
      <MotherWorkspaceControls scope={motherScope} disabled={busy} onChange={() => { setPage(1); setNotice(''); }} />
      <Group className="management-toolbar" align="end" gap={12}><TextInput className="management-search" aria-label="搜索批次" placeholder="批次名称或轮次" value={search} onChange={(event) => { setSearch(event.currentTarget.value); setPage(1); }} /><Select aria-label="轮转状态" placeholder="全部状态" clearable value={filter || null} data={Object.entries(rotationLabels).map(([value, item]) => ({ value, label: item.label }))} onChange={(value) => { setFilter((value as FilterState) || ''); setPage(1); }} /></Group>
      <Table.ScrollContainer minWidth={1120}><Table className="management-table" aria-busy={listLoading}><Table.Thead><Table.Tr><Table.Th>批次</Table.Th><Table.Th>母号 / 空间</Table.Th><Table.Th>计划人数</Table.Th><Table.Th>实际加入</Table.Th><Table.Th>已清退</Table.Th><Table.Th>计划清退时间</Table.Th><Table.Th>轮转状态</Table.Th><Table.Th>操作</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{batches.items.map((item) => <Table.Tr key={item.id} data-selected={item.id === selectedId || undefined}><Table.Td><Text size="sm">第 {item.sequenceNo} 批</Text><Text size="xs" c="dimmed">{item.sourceBatchName ?? '来源批次未留存'}</Text></Table.Td><Table.Td><Text size="sm">{item.motherAccountName}</Text><Text size="xs" c="dimmed">{item.workspaceName}</Text></Table.Td><Table.Td>{item.targetCount}</Table.Td><Table.Td>{item.rotation.joinedCount}</Table.Td><Table.Td>{item.rotation.removedCount}</Table.Td><Table.Td>{rotationTime(item.plannedAt)}</Table.Td><Table.Td><StatusBadge {...(rotationLabels[item.rotation.state] ?? rotationLabels.not_started)} /></Table.Td><Table.Td><Group gap={4}><Button variant="subtle" size="xs" disabled={busy} onClick={() => openBatch(item)}>查看详情</Button><Button variant="subtle" size="xs" color="error" disabled={busy || item.rotation.joinedCount <= item.rotation.removedCount || item.rotation.state === 'removing'} onClick={() => { openBatch(item); setConfirm(true); }}>清退批次</Button></Group></Table.Td></Table.Tr>)}</Table.Tbody></Table></Table.ScrollContainer>
      {listLoading ? <Text className="management-empty-state" role="status">正在读取轮转批次</Text> : !batches.items.length ? <Box className="management-empty-state"><OwnerIcon name="rotation" size={24} /><Text c="dimmed">{readFailed ? '轮转批次读取失败，请刷新重试' : motherScope.workspaceId ? '此空间还没有轮转批次' : '选择母号和空间查看轮转批次'}</Text></Box> : null}
      <ListPagination page={page} pageSize={pageSize} total={batches.total} disabled={busy || !bindingId} onPageChange={setPage} onPageSizeChange={(value) => { setPageSize(value); setPage(1); }} />
    </Paper> : null}
    {detailVisible && batch ? <Paper withBorder radius={12} className="management-list-panel rotation-detail-panel">
      <Group className="management-toolbar" justify="space-between" align="start"><Box><Title order={3} size="h4">本轮清退范围</Title><Text size="sm" c="dimmed">{batch.motherAccountName} · {batch.workspaceName} · {batch.sourceBatchName ?? '来源批次未留存'}</Text></Box><StatusBadge {...(rotationLabels[batch.rotation.state] ?? rotationLabels.not_started)} /></Group>
      <Group className="rotation-facts" gap={24} p="md"><Text size="sm">计划人数 <b>{batch.targetCount}</b></Text><Text size="sm">实际加入 <b>{batch.rotation.joinedCount}</b></Text><Text size="sm">已清退 <b>{batch.rotation.removedCount}</b></Text><Text size="sm">计划时间 <b>{rotationTime(batch.plannedAt)}</b></Text></Group>
      {!allDone && preview?.blockers.length ? <Alert color="warning" m="md"><Stack gap={4}>{preview.blockers.map((blocker) => <Text key={blocker.code} size="sm">{blocker.message}</Text>)}</Stack></Alert> : null}
      <Group className="management-toolbar" justify="space-between"><Text size="sm" c="dimmed">本轮固定成员 {preview?.targetTotal ?? batch.rotation.joinedCount} 个</Text><Group gap={8}><Button variant="default" disabled={busy} onClick={removal.refresh}>重新核验</Button>{canStart ? <TaskConcurrencyControl value={removal.taskConcurrency.concurrency} limit={removal.taskConcurrency.limit} disabled={!removal.taskConcurrency.ready || busy} onChange={removal.taskConcurrency.setConcurrency} /> : null}{canStart ? <Button variant="outline" color="error" onClick={() => setConfirm(true)}>清退批次</Button> : null}{needsReview ? <Button variant="default" loading={removal.pending} onClick={() => void removal.act('reconcile')}>继续核验清退结果</Button> : null}{allDone ? <Button onClick={() => onStartNextBatch({ motherAccountId: batch.motherAccountId ?? motherScope.motherId ?? '', workspaceId: batch.workspaceId, previousBatchId: batch.id })}>开始下一批次</Button> : null}</Group></Group>
      {preview?.differenceTotal ? <Text px="md" pb="sm" size="sm" c="dimmed">空间中另有 {preview.differenceTotal} 个不属于本轮的成员，本次保留。</Text> : null}
      {operation ? <Text px="md" pb="sm" size="sm">已完成 {operation.succeededCount} · 待处理 {operation.pendingCount} · 需处理 {operation.blockedCount}</Text> : null}
      <Table.ScrollContainer minWidth={880}><Table className="management-table" aria-busy={removal.loading}>
        <Table.Thead><Table.Tr><Table.Th className="account-identity-cell">账号</Table.Th><Table.Th>空间状态</Table.Th><Table.Th>清退结果</Table.Th><Table.Th>结果说明</Table.Th><Table.Th>操作</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>{(preview?.targets ?? []).map((target) => {
          const progress = operation?.targets.find((item) => item.membershipId === target.membershipId);
          const removed = target.state === 'removed';
          const status = removed ? { tone: 'success' as const, label: '已清退' } : progress ? removalLabels[progress.status] ?? { tone: 'warning' as const, label: '待核验' } : { tone: 'gray' as const, label: '尚未清退' };
          return <Table.Tr key={target.membershipId}>
            <Table.Td className="account-identity-cell">{target.identifier || target.displayLabel}</Table.Td>
            <Table.Td><StatusBadge {...(memberLabels[target.state] ?? { tone: 'warning' as const, label: '待核验' })} /></Table.Td>
            <Table.Td><StatusBadge {...status} /></Table.Td>
            <Table.Td>{removed ? '已确认退出空间' : progress ? removalReason(progress.diagnosticCode ?? progress.outcomeCode) : '等待人工确认清退'}</Table.Td>
            <Table.Td><Button variant="subtle" size="xs" color="error" aria-label={`清退账号 ${target.identifier || target.displayLabel}`} disabled={!canStart || !['present', 'absent'].includes(target.state)} onClick={() => setConfirm(target)}>清退账号</Button></Table.Td>
          </Table.Tr>;
        })}</Table.Tbody>
      </Table></Table.ScrollContainer>
      {preview ? <ListPagination page={removal.page} pageSize={removal.pageSize} total={preview.targetTotal} disabled={busy} onPageChange={removal.setPage} onPageSizeChange={removal.setPageSize} /> : null}
    </Paper> : selectedId && removal.loading ? <Text role="status">正在读取本轮成员</Text> : null}
    <Modal opened={Boolean(confirm)} onClose={() => setConfirm(null)} title={confirm === true ? '确认清退批次' : '确认清退账号'} centered><Stack gap="md">
      <Text>{batch?.motherAccountName} · {batch?.workspaceName} · 第 {batch?.sequenceNo} 批</Text>
      {confirm && confirm !== true ? <><Text>{confirm.identifier || confirm.displayLabel}</Text><Text>确认后将清退此账号，保留本批次其他账号。</Text></> : <Text>本批次尚有 {(batch?.rotation.joinedCount ?? 0) - (batch?.rotation.removedCount ?? 0)} 个账号未清退，确认后将逐个处理并保留每个结果。</Text>}
      {batch?.rotation.state === 'not_due' ? <Text c="error">尚未到计划清退时间，确认后将立即提前清退。</Text> : null}
      <Group justify="flex-end"><Button variant="default" onClick={() => setConfirm(null)}>取消</Button><Button color="error" disabled={!canStart} onClick={() => { const membershipId = confirm && confirm !== true ? confirm.membershipId : undefined; setConfirm(null); void removal.act('start', membershipId); }}>确认清退</Button></Group>
    </Stack></Modal>
  </Stack>;
}
