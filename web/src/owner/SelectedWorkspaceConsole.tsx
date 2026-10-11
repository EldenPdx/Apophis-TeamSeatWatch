import ActionNotice from '../shared/ActionNotice';
import { Box, Button, Group, Paper, Stack, Table, Tabs, Text } from '@mantine/core';
import { useEffect, useRef, useState } from 'react';
import ListPagination from '../shared/ListPagination';
import StatusBadge from '../shared/StatusBadge';
import OwnerIcon from './OwnerIcon';
import WorkspaceSeatTable from './WorkspaceSeatTable';
import { formatDateTime } from '../shared/dateTime';
import { useSelectedWorkspace } from './useSelectedWorkspace';
import { seatTypeLabel } from './seatTypes';
import { canShowSelectedWorkspaceReadSource, workspaceSubscriptionState } from './workspaceVerification';
import { workspaceConnectionFeedback, workspaceSyncProgress } from './workspaceConnection';

const roleLabels: Record<string, string> = { 'account-owner': '所有者', 'account-admin': '管理员', owner: '所有者', 'primary-owner': '所有者', admin: '管理员', administrator: '管理员', member: '成员', 'standard-user': '成员' };
const date = (value?: string) => value ? formatDateTime(value) : '未获取';
const role = (value?: string) => value ? roleLabels[value] ?? '未知' : '未获取';

export default function SelectedWorkspaceConsole({ motherAccountId, workspaceId, onNextAction, onMotherChanged }: { motherAccountId: string; workspaceId: string; onNextAction: () => void; onMotherChanged: () => void }) {
  const state = useSelectedWorkspace(motherAccountId, workspaceId);
  const [tab, setTab] = useState<string | null>('overview');
  const { facts, ready, pending, loading } = state;
  const current = ready && !pending;
  const subscriptionCurrent = !pending && canShowSelectedWorkspaceReadSource(facts, state.access, 'subscriptions', state.now);
  const subscription = pending ? { label: '同步中', tone: 'indigo' as const } : workspaceSubscriptionState(facts, state.access, state.now);
  const seatCountsCurrent = !pending && canShowSelectedWorkspaceReadSource(facts, state.access, 'seat_type_counts', state.now);
  const value = (data: string | number | undefined) => current ? data ?? '未获取' : '未同步';
  const permission = current ? facts?.canManage ? '可管理' : '可读取' : facts?.status === 'permission_denied' ? '权限不足' : '待核验';
  const chooseTab = (next: string | null) => { setTab(next); state.setKind(next === 'invites' ? 'pending_invite' : 'member'); };
  const connectionFailure = !pending && !loading ? state.syncFailure || (state.access?.status === 'failed' ? workspaceConnectionFeedback(state.access) : '') : '';
  const invalidation = useRef('');
  useEffect(() => {
    const failure = state.access?.failure;
    if (!state.motherRevision && (failure?.httpStatus !== 401 || !['workspace_token_revoked', 'workspace_session_expired'].includes(failure.code))) return;
    const observation = `${motherAccountId}:${state.motherRevision}:${state.access?.checkedAt}:${failure?.code}`;
    if (invalidation.current === observation) return;
    invalidation.current = observation; onMotherChanged();
  }, [motherAccountId, state.access, state.motherRevision, onMotherChanged]);

  return <Stack gap={20} className="workspace-modal-details records-detail">
    <Group justify="space-between"><Group gap={8}><StatusBadge tone={current ? 'success' : 'warning'} label={pending ? '同步中' : current ? '已同步' : subscriptionCurrent || seatCountsCurrent ? '部分已同步' : '未同步'} /><StatusBadge tone={current ? 'gray' : 'warning'} label={permission} /></Group><Button variant="default" size="sm" leftSection={<OwnerIcon name="refresh" size={16} />} loading={pending} disabled={pending || loading} onClick={() => void state.sync()}>同步空间</Button></Group>
    <ActionNotice message={connectionFailure ? '' : state.notice} tone={state.noticeTone} onClose={state.dismissNotice} />
    {connectionFailure ? <Box role="alert"><Text size="sm" c="error">{connectionFailure}</Text>{state.access?.checkedAt ? <Text size="xs" c="dimmed" mt={4}>上次连接：{formatDateTime(state.access.checkedAt)}</Text> : null}</Box> : null}
    {pending ? <Text size="sm" c="dimmed" role="status">{workspaceSyncProgress[state.syncStage]}</Text> : null}
    <Tabs value={tab} onChange={chooseTab}>
      <Tabs.List><Tabs.Tab disabled={pending} value="overview">概览</Tabs.Tab><Tabs.Tab disabled={pending} value="members">成员{facts?.memberCount !== undefined ? ` ${facts.memberCount}` : ''}</Tabs.Tab><Tabs.Tab disabled={pending} value="invites">待接受账号{facts?.pendingInviteCount !== undefined ? ` ${facts.pendingInviteCount}` : ''}</Tabs.Tab></Tabs.List>
      <Tabs.Panel value="overview" pt={20}><Stack gap={20}>
        <Box className="detail-facts workspace-core-facts">
          <Box className="detail-fact"><Text className="detail-fact-label">订阅状态</Text><StatusBadge {...subscription} /></Box>
          {[
            ['当前身份', value(role(facts?.motherRole))], ['订阅到期', subscriptionCurrent ? date(facts?.activeUntil) : '未同步'],
            ['当前成员', value(facts?.memberCount)],
          ].map(([label, content]) => <Box key={label} className="detail-fact"><Text className="detail-fact-label">{label}</Text><Text size="sm" fw={600}>{content}</Text></Box>)}
        </Box>
        <WorkspaceSeatTable facts={facts} subscriptionCurrent={subscriptionCurrent} countsCurrent={seatCountsCurrent} rosterCurrent={current} />
        {current && facts?.canManage ? <Group justify="flex-end"><Button onClick={onNextAction}>选择批次开始操作</Button></Group> : null}
      </Stack></Tabs.Panel>
      {(['members', 'invites'] as const).map((section) => <Tabs.Panel key={section} value={section} pt={20}><Paper withBorder radius={8} className="management-list-panel"><Table.ScrollContainer minWidth={600}><Table className="management-table" aria-busy={loading || pending}><Table.Thead><Table.Tr><Table.Th className="account-identity-cell">账号</Table.Th><Table.Th>{section === 'members' ? '身份' : '状态'}</Table.Th><Table.Th>席位类型</Table.Th><Table.Th>同步时间</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{current && !loading ? facts?.members.filter((entry) => entry.kind === (section === 'members' ? 'member' : 'pending_invite')).map((entry) => <Table.Tr key={`${entry.kind}:${entry.identifier}`}><Table.Td className="account-identity-cell"><Text size="sm" fw={600}>{entry.identifier}</Text></Table.Td><Table.Td>{entry.kind === 'member' ? role(entry.role) : <StatusBadge tone="warning" label="待接受" />}</Table.Td><Table.Td><StatusBadge tone={entry.seatType === 'prolite' ? 'indigo' : entry.seatType ? 'gray' : 'warning'} label={seatTypeLabel(entry.seatType)} /></Table.Td><Table.Td><Text size="xs" c="dimmed">{date(entry.observedAt)}</Text></Table.Td></Table.Tr>) : null}</Table.Tbody></Table></Table.ScrollContainer>{loading || pending ? <Text className="management-empty-state" role="status">正在读取</Text> : !current ? <Text className="management-empty-state">未同步</Text> : !facts?.members.length ? <Text className="management-empty-state">{section === 'members' ? '暂无成员' : '暂无待接受账号'}</Text> : null}<ListPagination page={state.page} pageSize={state.pageSize} total={facts?.total ?? 0} disabled={loading || pending || !current} onPageChange={state.setPage} onPageSizeChange={state.setPageSize} /></Paper></Tabs.Panel>)}
    </Tabs>
  </Stack>;
}
