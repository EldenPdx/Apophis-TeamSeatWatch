import ActionNotice from '../shared/ActionNotice';
import { useActionFeedback } from '../shared/useActionFeedback';
import { Alert, Box, Button, Checkbox, Group, Modal, Paper, Stack, Table, Text, TextInput, Title, Select } from '@mantine/core';
import { useState } from 'react';
import type { components } from '../generated/owner';
import { exportMotherAccounts, ownerProblem } from './auth';
import { confirmWorkspace } from './workspaceSelection';
import { togglePage } from './childSelection';
import { useMotherWorkspaces, type MotherAccount, type MotherWorkspaceRecord } from './useMotherWorkspaces';
import { useRecordCopy } from '../shared/useRecordCopy';
import MotherAccountImportView from './MotherAccountImportView';
import SelectedWorkspaceConsole from './SelectedWorkspaceConsole';
import OwnerIcon from './OwnerIcon';
import ListPagination from '../shared/ListPagination';
import StatusBadge, { type StatusTone } from '../shared/StatusBadge';
import { formatDateTime } from '../shared/dateTime';

type ImportResult = components['schemas']['MotherAccountImportResult'];
type Status = { tone: StatusTone; label: string };
const accessLabels = { not_verified: '待登录', verifying: '登录中', ready: '已登录', session_expired: '登录已失效', invalid_login: '登录资料无效', missing_credentials: '资料待补', refresh_failed: '登录失败', unavailable: '暂不可用' };
const discoveryLabels = { not_verified: '未发现', discovering: '发现中', discovered: '已发现', empty: '未发现空间', session_expired: '登录已失效', missing_credentials: '资料待补', discovery_failed: '发现失败', permission_denied: '权限不足', unavailable: '暂不可用' };
function loginStatus(account: MotherAccount, record: MotherWorkspaceRecord | undefined): Status {
  if (account.status !== 'active') return { tone: 'gray', label: '已停用' };
  if (record?.phase === 'login') return { tone: 'indigo', label: '登录中' };
  if (record?.access) return { tone: record.access.status === 'ready' ? 'success' : record.access.status === 'invalid_login' || record.access.status === 'refresh_failed' ? 'error' : 'warning', label: accessLabels[record.access.status] };
  if (record?.discovery?.status === 'session_expired') return { tone: 'warning', label: '登录已失效' };
  if (record?.discovery?.status === 'missing_credentials') return { tone: 'warning', label: '资料待补' };
  if (record?.discovery?.status === 'unavailable') return { tone: 'warning', label: '暂不可用' };
  return { tone: 'gray', label: !record || record.phase === 'reading' ? '读取中' : record.notice ? '待读取' : '待登录' };
}
function spaceStatus(account: MotherAccount, record: MotherWorkspaceRecord | undefined): Status {
  if (account.status !== 'active') return { tone: 'gray', label: '—' };
  if (record?.phase === 'discovering') return { tone: 'indigo', label: '发现中' };
  if (record?.phase === 'login' || (record?.access && record.access.status !== 'ready' && !record.discovery)) return { tone: 'gray', label: '等待登录' };
  const result = record?.discovery;
  if (!result) return { tone: 'gray', label: record?.notice || record?.access ? '待读取' : '读取中' };
  return { tone: result.status === 'discovered' ? 'success' : result.status === 'discovery_failed' ? 'error' : 'warning', label: result.status === 'discovered' ? `${result.workspaces.length} 个空间` : discoveryLabels[result.status] };
}

export default function WorkspaceDiscoveryView({ onNextAction, active = true }: { onNextAction: (motherAccountId: string, workspaceId: string) => void; active?: boolean }) {
  const [view, setView] = useState<'list' | 'import'>('list');
  const [inspecting, setInspecting] = useState<MotherAccount | null>(null);
  const [workspaceId, setWorkspaceId] = useState<string | null>(null);
  const mothers = useMotherWorkspaces(active && view === 'list');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [importResult, setImportResult] = useActionFeedback<ImportResult | null>(null);
  const copier = useRecordCopy();
  const [pending, setPending] = useState(false);
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const { items, records, loading } = mothers;
  const pageIds = items.map((item) => item.id);
  const pageSelected = pageIds.length > 0 && pageIds.every((id) => selected.has(id));

  async function exportSelected() {
    if (pending || !selected.size) return;
    setPending(true); setNotice('');
    try {
      const content = await exportMotherAccounts([...selected], selected.size);
      const url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }));
      const link = document.createElement('a'); link.href = url; link.download = 'mother-account-materials.txt'; link.click(); URL.revokeObjectURL(url);
    } catch (error: unknown) { setNotice(ownerProblem(error).status === 409 ? '所选母号已变化，请刷新后重新选择。' : '母号导出失败，请重试。'); }
    finally { setPending(false); }
  }

  return <Stack gap={24} className="management-page workspace-page">
    <Group className="management-subheader" justify="space-between" align="center"><Group gap={12}><Box className="management-page-icon"><OwnerIcon name="workspaces" size={20} /></Box><Title order={2}>空间管理</Title></Group><Group gap={8}><Button variant="default" leftSection={<OwnerIcon name="download" size={16} />} disabled={!selected.size || pending} loading={pending} onClick={() => void exportSelected()}>导出母号</Button><Button leftSection={<OwnerIcon name="upload" size={16} />} disabled={pending} onClick={() => { setNotice(''); setView('import'); }}>导入母号</Button></Group></Group>
    <ActionNotice message={notice || mothers.notice || copier.notice} onClose={() => { dismissNotice(); mothers.dismissNotice(); copier.dismissNotice(); }} />
    {importResult ? <Alert color={importResult.invalid ? 'warning' : 'success'} withCloseButton closeButtonLabel="关闭提示" onClose={() => setImportResult(null)}>已导入 {importResult.imported} 个母号 · 重复 {importResult.duplicate} 个 · 无效 {importResult.invalid} 个{importResult.rows.filter((row) => row.status === 'invalid' || row.status === 'needs_totp').map((row) => <Text size="sm" key={row.line}>第 {row.line} 行 · {row.status === 'needs_totp' ? '2FA 待补' : '格式无效'}</Text>)}</Alert> : null}
    <Paper withBorder radius={12} className="management-list-panel">
      <Group className="management-toolbar" align="end"><TextInput className="management-search" aria-label="搜索母号" placeholder="账号或名称" leftSection={<OwnerIcon name="search" size={16} />} value={mothers.search} onChange={(event) => { mothers.setSearch(event.currentTarget.value); setSelected(new Set()); }} /><Button variant="subtle" color="gray" disabled={loading || pending} onClick={mothers.refresh}>刷新</Button></Group>
      {selected.size ? <Group className="management-selection-bar" justify="space-between"><Text size="sm">已选 {selected.size} 个母号</Text><Button variant="subtle" size="xs" color="gray" onClick={() => setSelected(new Set())}>取消选择</Button></Group> : null}
      <Table.ScrollContainer minWidth={1000}><Table className="management-table" aria-label="母号列表" aria-busy={loading} highlightOnHover><Table.Thead><Table.Tr><Table.Th><Checkbox aria-label="选择本页" checked={pageSelected} indeterminate={!pageSelected && pageIds.some((id) => selected.has(id))} disabled={loading || !items.length} onChange={() => setSelected(togglePage(selected, pageIds))} /></Table.Th><Table.Th className="account-identity-cell">母号</Table.Th><Table.Th>登录状态</Table.Th><Table.Th>空间</Table.Th><Table.Th>最近发现</Table.Th><Table.Th>操作</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{items.map((account) => {
        const record = records[account.id];
        const busy = Boolean(record?.phase);
        const loggedIn = record?.access?.status === 'ready';
        return <Table.Tr key={account.id} data-selected={selected.has(account.id) || undefined}>
          <Table.Td><Checkbox aria-label={`选择 ${account.loginIdentifier}`} checked={selected.has(account.id)} onChange={() => setSelected((current) => { const next = new Set(current); if (next.has(account.id)) next.delete(account.id); else next.add(account.id); return next; })} /></Table.Td>
          <Table.Td className="account-identity-cell"><Group gap={10} wrap="nowrap"><Text className="account-avatar" data-accent="indigo" aria-hidden="true">{account.loginIdentifier.slice(0, 1).toUpperCase()}</Text><Box><Text size="sm" fw={600}>{account.loginIdentifier}</Text>{account.displayName !== account.loginIdentifier ? <Text size="xs" c="dimmed">{account.displayName}</Text> : null}</Box></Group></Table.Td>
          <Table.Td><StatusBadge {...loginStatus(account, record)} /></Table.Td><Table.Td><StatusBadge {...spaceStatus(account, record)} /></Table.Td><Table.Td><Text size="xs" c="dimmed">{record?.discovery?.observedAt ? formatDateTime(record.discovery.observedAt) : '—'}</Text></Table.Td>
          <Table.Td><Stack gap={4}><Group gap={4} justify="center" wrap="nowrap"><Button variant="subtle" size="xs" loading={copier.copyingId === account.id} disabled={loading || pending || copier.copyingId !== null} onClick={() => void copier.copy(account.id, async () => (await exportMotherAccounts([account.id], 1)).replace(/\r?\n$/, ''))}>{copier.copiedId === account.id ? '已复制' : '复制'}</Button><Button variant="subtle" size="xs" loading={record?.phase === 'login' || record?.phase === 'discovering'} disabled={busy || pending || account.status !== 'active' || account.materialStatus !== 'complete'} onClick={() => void (loggedIn ? mothers.rediscover(account) : mothers.connect(account))}>{record?.phase === 'login' ? '登录中' : record?.phase === 'discovering' ? '发现中' : loggedIn ? '重新发现' : '登录并发现空间'}</Button><Button variant="subtle" size="xs" disabled={busy || pending || account.status !== 'active'} onClick={() => { setInspecting(account); setWorkspaceId(null); }}>查看空间</Button></Group>{record?.notice ? <Group gap={4} justify="center"><Text size="xs" c="error" role="alert">{record.notice}</Text><Button variant="subtle" size="xs" disabled={busy} onClick={() => void mothers.read(account)}>重试读取</Button></Group> : account.materialStatus !== 'complete' ? <Text size="xs" c="warning">2FA 待补</Text> : null}</Stack></Table.Td>
        </Table.Tr>;
      })}</Table.Tbody></Table></Table.ScrollContainer>
      {loading ? <Text className="management-empty-state" role="status">正在读取母号</Text> : !items.length ? <Box className="management-empty-state"><OwnerIcon name="workspaces" size={24} /><Text c="dimmed">{mothers.readFailed ? '母号读取失败，请刷新重试' : mothers.search ? '没有匹配的母号' : '暂无母号'}</Text></Box> : null}
      <ListPagination page={mothers.page} pageSize={mothers.pageSize} total={mothers.total} disabled={loading || pending} onPageChange={mothers.setPage} onPageSizeChange={mothers.setPageSize} />
    </Paper>
    <Modal opened={inspecting !== null} onClose={() => { setInspecting(null); setWorkspaceId(null); }} title="查看空间" centered size="xl">
      {inspecting ? <Stack gap={20}><Group align="end" grow><TextInput label="母号" value={inspecting.loginIdentifier} readOnly /><Select label="空间" placeholder="选择空间" value={workspaceId} data={records[inspecting.id]?.discovery?.workspaces.map((space) => ({ value: space.id, label: space.displayName, disabled: space.accessStatus !== 'readable' })) ?? []} onChange={(id) => setWorkspaceId(id ? confirmWorkspace(records[inspecting.id]?.discovery ?? null, id) : null)} /></Group>{workspaceId ? <SelectedWorkspaceConsole key={`${inspecting.id}:${workspaceId}`} motherAccountId={inspecting.id} workspaceId={workspaceId} onNextAction={() => { setInspecting(null); onNextAction(inspecting.id, workspaceId); }} onMotherChanged={() => void mothers.read(inspecting)} /> : <Text className="management-empty-state">{records[inspecting.id]?.discovery?.status === 'discovered' ? '选择空间' : spaceStatus(inspecting, records[inspecting.id]).label}</Text>}</Stack> : null}
    </Modal>
    <Modal opened={view === 'import'} onClose={() => { if (!pending) setView('list'); }} title="导入母号" centered size="xl"><MotherAccountImportView onBack={() => setView('list')} onImported={(result) => { setImportResult(result); setSelected(new Set()); setNotice(''); mothers.imported(); setView('list'); }} /></Modal>

  </Stack>;
}
