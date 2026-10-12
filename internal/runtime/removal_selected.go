package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

// Use the same scoped verification as space management; legacy-only installs
// retain their existing evidence source.
func (h *OwnerAuthHandler) currentRemovalPreview(ctx context.Context, tx pgx.Tx, batch ownerapi.Batch) (ownerapi.RemovalPreview, error) {
	if h.selectedWorkspaceReader == nil {
		return removalPreview(ctx, tx, batch)
	}
	snapshot := removalSnapshot{operationalState: "unknown", completeness: "unknown"}
	var mother uuid.UUID
	err := tx.QueryRow(ctx, `SELECT binding.mother_account_id,c.login_identifier FROM tsw_mother_workspace_bindings binding JOIN tsw_mother_account_credentials c ON c.mother_account_id=binding.mother_account_id WHERE binding.id=$1 AND binding.workspace_id=$2 AND binding.ended_at IS NULL`, batch.BindingId, batch.WorkspaceId).Scan(&mother, &snapshot.ownerIdentifier)
	if err != nil {
		return ownerapi.RemovalPreview{}, err
	}
	facts, id, _, err := h.selectedWorkspaceSnapshotTx(ctx, tx, batch.WorkspaceId, mother)
	if errors.Is(err, pgx.ErrNoRows) {
		return removalPreviewWithSnapshot(ctx, tx, batch, snapshot)
	}
	if err != nil {
		return ownerapi.RemovalPreview{}, err
	}
	snapshot.completeness = string(facts.Completeness)
	snapshot.snapshotObserved, snapshot.snapshotExpiresAt, snapshot.evidenceExpiresAt = facts.ObservedAt, facts.ExpiresAt, facts.ExpiresAt
	snapshot.snapshotSource, snapshot.declaredCount = facts.Source, facts.MemberCount
	if facts.Status == "verified" && facts.Permission == "read" && facts.ActiveUntil != nil && facts.ActiveUntil.After(time.Now()) {
		snapshot.operationalState = "operational"
	}
	if id > 0 {
		snapshot.available = true
		rows, err := tx.Query(ctx, `SELECT identifier,COALESCE(role,'') FROM tsw_workspace_verification_entries WHERE verification_id=$1 AND kind='member' ORDER BY identifier`, id)
		if err != nil {
			return ownerapi.RemovalPreview{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var entry removalSnapshotEntry
			if err := rows.Scan(&entry.Identifier, &entry.Role); err != nil {
				return ownerapi.RemovalPreview{}, err
			}
			snapshot.entries = append(snapshot.entries, entry)
		}
		if err := rows.Err(); err != nil {
			return ownerapi.RemovalPreview{}, err
		}
	}
	return removalPreviewWithSnapshot(ctx, tx, batch, snapshot)
}
