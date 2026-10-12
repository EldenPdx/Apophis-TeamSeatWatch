import ActionNotice from '../shared/ActionNotice';
import { ActionIcon, Box, Button, Checkbox, Group, Modal, Paper, ScrollArea, Select, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useRef, useState } from 'react';
import type { AccountProbeFilter } from './auth';
import type { Batch } from './standbyBatches';
import useStandbyBatches from './useStandbyBatches';
import { latestProbe, probeLabels } from './accountPresentation';
import AccountIdentity from './AccountIdentity';
import OwnerIcon from './OwnerIcon';
import StatusBadge from '../shared/StatusBadge';
import ListPagination from '../shared/ListPagination';
import ListSelectionBar from '../shared/ListSelectionBar';
import Timestamp from '../shared/Timestamp';

export default function StandbyChildBatchesView({ active = true }: { active?: boolean }) {
  const model = useStandbyBatches(active);
  const [renaming, setRenaming] = useState<Batch | null>(null);
  const [nextName, setNextName] = useState('');
  const nameInput = useRef<HTMLInputElement>(null);
  const isList = model.view === 'list';
  const isMembers = model.view === 'members';
  const isNew = model.view === 'new';
  const query = isList ? model.listQuery : model.query;
  const disabled = model.busy || !!model.confirmation || !!model.deleting;
  const pageIds = model.accounts.map((item) => item.id);
  const allPage = pageIds.length > 0 && pageIds.every((id) => model.selected.has(id));
  const title = isList ? '批次管理' : isNew ? '新建批次' : `${isMembers ? '批次成员' : '添加账号'} · ${model.batch?.name ?? ''}`;
  const transfers = new Map<string, { name: string; count: number }>();
  for (const member of model.confirmation?.selection.members ?? []) {
    const source = member.currentBatch;
    if (!source || source.id === model.confirmation?.batch?.id) continue;
    const item = transfers.get(source.id) ?? { name: source.name, count: 0 };
    item.count++; transfers.set(source.id, item);
  }

  return <Stack gap={24} className="management-page batch-management-page">
    <Group className="management-subheader" justify="space-between" align="center">
      <Group gap={12}><Box className="management-page-icon"><OwnerIcon name="batches" size={20} /></Box><Title order={2}>{title}</Title></Group>
      <Group gap={8}>
        {isList ? <Button leftSection={<OwnerIcon name="operation" size={16} />} disabled={disabled} onClick={() => model.open('new')}>新建批次</Button> : <>
          <Button variant="default" leftSection={<OwnerIcon name="arrow-left" size={16} />} disabled={disabled} onClick={() => model.open(model.view === 'add' ? 'members' : 'list')}>{model.view === 'add' ? '返回批次成员' : '返回批次列表'}</Button>
          {isMembers ? <><Button variant="default" leftSection={<OwnerIcon name="download" size={16} />} loading={model.pending === `export:${model.batch?.id}`} disabled={disabled || !model.batch?.memberCount} onClick={() => model.batch && void model.download(model.batch)}>导出批次</Button><Button disabled={disabled} onClick={() => model.open('add')}>添加账号</Button></> : <Button loading={model.pending === 'save'} disabled={disabled || model.loading || !model.stable || model.selected.size === 0 || model.selected.size > 10000} onClick={() => { if (isNew && !model.name.trim()) nameInput.current?.focus(); void model.save(); }}>{isNew ? '保存批次' : `添加已选 ${model.selected.size} 个账号`}</Button>}
        </>}
      </Group>
    </Group>
    <ActionNotice message={model.notice} tone={model.notice.startsWith('已') ? 'success' : 'error'} onClose={model.dismissNotice} />
    <Paper withBorder radius={12} className="account-list-panel">
      {isNew ? <Group className="batch-name-row"><TextInput ref={nameInput} label="批次名称" aria-label="批次名称" placeholder="输入批次名称" required error={model.nameError} maxLength={120} value={model.name} disabled={disabled} onChange={(event) => model.setName(event.currentTarget.value)} /></Group> : null}
      <Box className="account-filters batch-filters" data-list={isList || undefined} data-members={isMembers || undefined}>
        <TextInput aria-label={isList ? '搜索批次' : '搜索账号'} placeholder={isList ? '搜索批次名称' : '搜索账号'} leftSection={<OwnerIcon name="search" size={16} />} disabled={disabled} value={query.search} onChange={(event) => model.changeQuery({ search: event.currentTarget.value })} />
        <TextInput aria-label="邮箱域名" placeholder={isList || isMembers ? '邮箱域名' : '输入邮箱域名'} disabled={disabled} value={query.domain} onChange={(event) => model.changeQuery({ domain: event.currentTarget.value })} />
        {!isList ? <Select aria-label="探测结果" clearable placeholder="全部探测结果" disabled={disabled} value={query.probe ?? null} data={Object.entries(probeLabels).map(([value, label]) => ({ value, label }))} onChange={(value) => model.changeQuery({ probe: value ? value as AccountProbeFilter : undefined })} /> : null}
        {!isList && !isMembers ? <Select aria-label="分批状态" clearable placeholder="全部分批状态" disabled={disabled} value={query.membership ?? null} data={[{ value: 'unassigned', label: '未分批' }, { value: 'assigned', label: '已分批' }]} onChange={(value) => model.changeQuery({ membership: value === 'assigned' || value === 'unassigned' ? value : undefined })} /> : null}
        <Group className="account-filter-actions" gap={4} wrap="nowrap">
          <Button variant="subtle" color="gray" size="sm" disabled={disabled || (!query.search && !query.domain && !query.probe && !query.membership)} onClick={() => model.changeQuery({ search: '', domain: '', probe: undefined, membership: undefined })}>重置</Button>
          <ActionIcon variant="subtle" color="gray" size={32} aria-label="刷新" title="刷新" disabled={disabled || model.loading} onClick={model.refresh}><OwnerIcon name="refresh" size={16} /></ActionIcon>
        </Group>
      </Box>
      {!isList ? <ListSelectionBar count={model.selected.size} total={model.total} actions={isMembers ? <Button variant="outline" color="error" size="sm" disabled={disabled || model.loading || !model.stable} loading={model.pending === 'remove'} onClick={() => void model.remove()}>移出已选 {model.selected.size} 个账号</Button> : undefined} /> : null}
      <ScrollArea.Autosize mah="max(320px, calc(100dvh - 340px))" type="auto" className="account-table-scroll">
        {isList ? <Table aria-busy={model.loading} highlightOnHover stickyHeader miw={820} className="management-table batch-list-table">
          <Table.Thead><Table.Tr><Table.Th>批次名称</Table.Th><Table.Th>域名</Table.Th><Table.Th>账号数</Table.Th><Table.Th>更新时间</Table.Th><Table.Th>操作</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>{model.batches.map((item) => <Table.Tr key={item.id}>
            <Table.Td><Text fw={600} size="sm">{item.name}</Text></Table.Td>
            <Table.Td><Group gap={4} justify="center">{item.domains.length ? item.domains.map((value) => <Text key={value} size="xs" className="account-domain">{value}</Text>) : '—'}</Group></Table.Td>
            <Table.Td><Text size="sm">{item.memberCount}</Text></Table.Td>
            <Table.Td><Timestamp value={item.updatedAt} /></Table.Td>
            <Table.Td><Stack gap={4} align="center"><Group gap={4} wrap="nowrap" justify="center">
              <Button variant="subtle" size="xs" disabled={disabled || model.loading} onClick={() => model.open('members', item)}>管理成员</Button>
              <Button variant="subtle" size="xs" disabled={disabled || model.loading} onClick={() => { setRenaming(item); setNextName(item.name); }}>重命名</Button>
              <Button variant="subtle" size="xs" loading={model.pending === `export:${item.id}`} disabled={disabled || model.loading || !item.memberCount} onClick={() => void model.download(item)}>导出</Button>
              <Button variant="outline" color="error" className="record-delete-action" size="xs" loading={model.pending === `delete:${item.id}`} disabled={disabled || model.loading || !model.stable} onClick={() => model.requestDeletion(item)}>删除</Button>
            </Group>{model.feedback[item.id] ? <Text size="xs" c="dimmed" role="status">{model.feedback[item.id]}</Text> : null}</Stack></Table.Td>
          </Table.Tr>)}</Table.Tbody>
        </Table> : <Table aria-busy={model.loading} highlightOnHover stickyHeader miw={940} className="account-table batch-account-table">
          <Table.Thead><Table.Tr>
            <Table.Th><Checkbox aria-label="选择本页" checked={allPage} indeterminate={!allPage && pageIds.some((id) => model.selected.has(id))} disabled={disabled || model.loading || !model.stable || pageIds.length === 0} onChange={() => model.toggle()} /></Table.Th>
            <Table.Th className="account-identity-cell">账号</Table.Th><Table.Th>域名</Table.Th><Table.Th>AT</Table.Th><Table.Th>最近探测</Table.Th><Table.Th>探测时间</Table.Th>{!isMembers ? <Table.Th>当前批次</Table.Th> : null}
          </Table.Tr></Table.Thead>
          <Table.Tbody>{model.accounts.map((item) => { const probe = latestProbe(item); return <Table.Tr key={item.id} data-selected={model.selected.has(item.id) || undefined}>
            <Table.Td><Checkbox aria-label={`选择 ${item.identifier}`} checked={model.selected.has(item.id)} disabled={disabled || model.loading || !model.stable} onChange={() => model.toggle(item.id)} /></Table.Td>
            <Table.Td className="account-identity-cell"><AccountIdentity identifier={item.identifier} /></Table.Td>
            <Table.Td><Text size="xs" className="account-domain">{item.identifier.split('@')[1] ?? '—'}</Text></Table.Td>
            <Table.Td><StatusBadge label={!item.tokenStatus ? '—' : item.tokenStatus.hasAccessToken ? '有' : '无'} tone={item.tokenStatus?.hasAccessToken ? 'success' : 'gray'} /></Table.Td>
            <Table.Td><StatusBadge label={probe.label} tone={probe.status === 'available' ? 'success' : probe.status === 'unprobed' ? 'gray' : probe.status === 'account_problem' || probe.status === 'definitely_unavailable' ? 'error' : 'warning'} /></Table.Td>
            <Table.Td>{probe.at ? <Timestamp value={probe.at} /> : '—'}</Table.Td>
            {!isMembers ? <Table.Td><Text size="sm" {...(!item.standbyBatch ? { c: 'dimmed' } : {})}>{item.standbyBatch?.name ?? '未分批'}</Text></Table.Td> : null}
          </Table.Tr>; })}</Table.Tbody>
        </Table>}
      </ScrollArea.Autosize>
      {model.loading ? <Text className="account-empty-state" role="status">正在读取{isList ? '批次' : '账号'}</Text> : (isList ? model.batches.length === 0 : model.accounts.length === 0) ? <Box className="account-empty-state"><OwnerIcon name={isList ? 'batches' : 'search'} size={24} /><Text c="dimmed">{model.readFailed ? '列表读取失败，请刷新重试' : isList ? query.search || query.domain ? '无匹配批次' : '暂无批次' : isMembers ? '无匹配成员' : query.search || query.domain || query.probe || query.membership ? '无匹配账号' : '暂无账号'}</Text></Box> : null}
      <ListPagination page={query.page} pageSize={query.pageSize} total={isList ? model.batchTotal : model.total} disabled={disabled || model.loading || !model.stable} onPageChange={(page) => model.changeQuery({ page })} onPageSizeChange={(pageSize) => model.changeQuery({ pageSize, page: 1 })} />
    </Paper>
    <Modal opened={active && renaming !== null} centered title="重命名批次" closeOnClickOutside={!model.busy} closeOnEscape={!model.busy} onClose={() => { if (!model.busy) setRenaming(null); }}>
      <Stack gap={16}><TextInput label="批次名称" value={nextName} maxLength={120} disabled={model.busy} onChange={(event) => setNextName(event.currentTarget.value)} /><Group justify="flex-end"><Button variant="default" disabled={model.busy} onClick={() => setRenaming(null)}>取消</Button><Button loading={model.pending === 'rename'} disabled={!nextName.trim() || nextName.trim() === renaming?.name || model.busy} onClick={() => { if (renaming) void model.rename(renaming, nextName).then((saved) => { if (saved) setRenaming(null); }); }}>保存</Button></Group><ActionNotice message={model.notice.startsWith('已') ? '' : model.notice} onClose={model.dismissNotice} /></Stack>
    </Modal>
    <Modal opened={active && model.deleting !== null} centered title="删除批次" closeOnClickOutside={!model.busy} closeOnEscape={!model.busy} withCloseButton={!model.busy} onClose={model.cancelDeletion}>
      {model.deleting ? <Stack gap={16}>
        <Text size="sm">确认删除「{model.deleting.name}」？{model.deleting.memberCount} 个账号将解除该批次归属。</Text>
        <Text size="sm" c="dimmed">账号资料、凭据及已执行的交付和清退记录保留。</Text>
        <Group justify="flex-end"><Button variant="default" disabled={model.busy} onClick={model.cancelDeletion}>取消</Button><Button variant="outline" color="error" loading={model.pending === `delete:${model.deleting.id}`} disabled={model.busy} onClick={() => void model.deleteBatch()}>确认删除</Button></Group>
      </Stack> : null}
    </Modal>
    <Modal opened={active && model.confirmation !== null} centered title={model.confirmation?.kind === 'remove' ? '移出批次成员' : '确认移批'} closeOnClickOutside={!model.busy} closeOnEscape={!model.busy} onClose={model.cancelConfirmation}>
      {model.confirmation ? <Stack gap={16}>
        {model.confirmation.kind === 'remove' ? <Text size="sm">从「{model.confirmation.batch?.name}」移出已选 {model.confirmation.selection.count} 个账号，账号资料保留。</Text> : <><Text size="sm">以下账号将离开原批次，加入「{model.confirmation.name}」。</Text><Table><Table.Thead><Table.Tr><Table.Th>原批次</Table.Th><Table.Th>移出账号数</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{[...transfers].map(([id, item]) => <Table.Tr key={id}><Table.Td>{item.name}</Table.Td><Table.Td>{item.count}</Table.Td></Table.Tr>)}</Table.Tbody></Table><Text size="sm">本次共{isNew ? '保存' : '添加'} {model.confirmation.selection.count} 个账号。</Text></>}
        <Group justify="flex-end"><Button variant="default" disabled={model.busy} onClick={model.cancelConfirmation}>取消</Button><Button color={model.confirmation.kind === 'remove' ? 'error' : 'indigo'} loading={model.pending === 'confirm'} disabled={model.busy} onClick={() => void model.confirm()}>{model.confirmation.kind === 'remove' ? `确认移出 ${model.confirmation.selection.count} 个账号` : '确认移批并保存'}</Button></Group>
      </Stack> : null}
    </Modal>
  </Stack>;
}
