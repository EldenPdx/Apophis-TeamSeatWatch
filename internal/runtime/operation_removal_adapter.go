package runtime

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

type operationRemovalAdapter struct {
	owner                       *OwnerAuthHandler
	target                      task.RemovalTarget
	mother, workspace, exchange uuid.UUID
	remover                     platform.OfficialWorkspaceMemberRemover
}

func (h *OwnerAuthHandler) operationRemover(ctx context.Context, client *http.Client, target task.RemovalTarget) (platform.Remover, error) {
	mother, err := uuid.Parse(target.MotherID)
	if err != nil {
		return nil, errOperationAuthority
	}
	space, err := uuid.Parse(target.WorkspaceID)
	if err != nil {
		return nil, errOperationAuthority
	}
	a := &operationRemovalAdapter{owner: h, target: target, mother: mother, workspace: space}
	a.remover = platform.OfficialWorkspaceMemberRemover{
		Client: func(context.Context) (*http.Client, func(), error) { return client, nil, nil },
		ResolveMemberIdentifiers: func(ctx context.Context, ids []string) (map[string]string, error) {
			return h.confirmedWorkspaceMemberIdentifiers(ctx, space, ids)
		},
	}
	if _, err = a.authority(ctx); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *operationRemovalAdapter) authority(ctx context.Context) (platform.WorkspaceAccess, error) {
	tx, err := a.owner.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return platform.WorkspaceAccess{}, errOperationAuthority
	}
	defer tx.Rollback(ctx)
	var bound bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tsw_batches b JOIN tsw_mother_workspace_bindings binding ON binding.id=b.binding_id WHERE b.id=$1 AND binding.mother_account_id=$2 AND binding.workspace_id=$3 AND binding.ended_at IS NULL)`, a.target.BatchID, a.mother, a.workspace).Scan(&bound)
	if err != nil || !bound {
		return platform.WorkspaceAccess{}, errOperationAuthority
	}
	facts, _, access, err := a.owner.selectedWorkspaceSnapshotTx(ctx, tx, a.workspace, a.mother)
	if err != nil || facts.Status != "verified" || facts.Completeness != "complete" || !facts.CanManage || facts.MotherRole == nil || !removalOwnerRole(string(*facts.MotherRole)) || facts.ExchangeId == nil || access.WorkspaceID != a.target.PlatformWorkspace || a.exchange != uuid.Nil && a.exchange != *facts.ExchangeId {
		return platform.WorkspaceAccess{}, errOperationAuthority
	}
	if err = tx.Commit(ctx); err != nil {
		return platform.WorkspaceAccess{}, errOperationAuthority
	}
	a.exchange = *facts.ExchangeId
	return access, nil
}

func (a *operationRemovalAdapter) SnapshotMembers(ctx context.Context, workspace string) (platform.ExactMemberSnapshot, error) {
	if workspace != a.target.PlatformWorkspace {
		return platform.ExactMemberSnapshot{}, errOperationAuthority
	}
	if _, err := a.authority(ctx); err != nil {
		return platform.ExactMemberSnapshot{}, err
	}
	attempt, err := a.owner.reserveSelectedWorkspace(ctx, a.workspace, a.mother)
	if err != nil || attempt.id == 0 || attempt.tokenExchangeID != a.exchange {
		return platform.ExactMemberSnapshot{}, errOperationAuthority
	}
	reader := platform.OfficialSelectedWorkspaceReader{Client: a.remover.Client, BearerOnly: true, ResolveMemberIdentifiers: a.remover.ResolveMemberIdentifiers}
	readContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	facts, readErr := reader.VerifySelectedWorkspace(readContext, attempt.access, a.mother.String(), workspace)
	cancel()
	status, err := a.owner.recordSelectedWorkspaceFacts(ctx, a.workspace, a.mother, attempt, facts, readErr)
	if err != nil {
		return platform.ExactMemberSnapshot{}, err
	}
	if status != "verified" {
		return platform.ExactMemberSnapshot{}, errors.New("membership snapshot is not exact")
	}
	if _, err := a.authority(ctx); err != nil {
		return platform.ExactMemberSnapshot{}, err
	}
	fact := facts.Result
	fact.Endpoint, fact.HTTPStatus, fact.PendingInviteCount = platform.EndpointMembers, http.StatusOK, nil
	fact.Members = []platform.Member{}
	for _, member := range facts.Result.Members {
		if member.Kind == "member" {
			fact.Members = append(fact.Members, member)
		}
	}
	return platform.ExactMemberSnapshot{Fact: fact, DeclaredMemberCount: *fact.MemberCount}, nil
}

func (a *operationRemovalAdapter) RemoveMember(ctx context.Context, workspace, member string) (platform.RemoveMemberResult, error) {
	if workspace != a.target.PlatformWorkspace {
		return platform.RemoveMemberResult{}, errOperationAuthority
	}
	access, err := a.authority(ctx)
	if err != nil {
		return platform.RemoveMemberResult{}, err
	}
	return a.remover.RemoveWorkspaceMember(ctx, access, workspace, member, a.target.OperationID)
}
