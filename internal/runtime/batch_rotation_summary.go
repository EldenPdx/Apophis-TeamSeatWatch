package runtime

// One projection supplies list filtering, detail state and the next-batch gate.
// Counts retain confirmed historical joins; removed members do not erase joins.
const batchRotationJoinsSQL = `
 LEFT JOIN LATERAL (
  SELECT count(*)::int AS joined_count,count(*) FILTER (WHERE state='removed')::int AS removed_count
  FROM tsw_batch_memberships WHERE batch_id=batch.id
 ) membership_facts ON true
 LEFT JOIN LATERAL (
  SELECT id,status FROM tsw_operations WHERE batch_id=batch.id AND operation_type='remove'
  ORDER BY created_at DESC,id DESC LIMIT 1
 ) removal_operation ON true
 LEFT JOIN LATERAL (
  SELECT count(*)::int AS total,count(*) FILTER (WHERE status='succeeded')::int AS succeeded,
   count(*) FILTER (WHERE status IN ('failed','blocked'))::int AS failed,
   count(*) FILTER (WHERE status='unknown')::int AS unknown
  FROM tsw_operation_targets WHERE operation_id=removal_operation.id
 ) removal_facts ON true`

const batchRotationStateSQL = `CASE
 WHEN batch.status='ended' AND membership_facts.joined_count>0
  AND membership_facts.joined_count=membership_facts.removed_count
  AND removal_operation.status='succeeded' AND removal_facts.total>0
  AND removal_facts.succeeded=removal_facts.total THEN 'completed'
 WHEN removal_facts.unknown>0 OR batch.status='ended' THEN 'pending_check'
 WHEN removal_facts.failed>0 OR (batch.status='removing' AND removal_operation.status IN ('failed','blocked')) THEN 'needs_attention'
 WHEN batch.status='removing' THEN 'removing'
 WHEN batch.status='serving' AND batch.planned_at<=now() THEN 'pending_removal'
 WHEN batch.status='serving' THEN 'not_due'
 WHEN batch.status='joining' THEN 'joining'
 ELSE 'not_started' END`

const batchFromSQL = ` FROM tsw_batches batch
 JOIN tsw_mother_workspace_bindings binding ON binding.id=batch.binding_id
 JOIN tsw_workspaces workspace ON workspace.id=binding.workspace_id
 JOIN tsw_mother_accounts account ON account.id=binding.mother_account_id` + batchRotationJoinsSQL
