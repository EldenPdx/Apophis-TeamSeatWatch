package runtime

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/audit"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func (h *OwnerAuthHandler) DeleteMotherAccount(w http.ResponseWriter, r *http.Request, accountID uuid.UUID, _ ownerapi.DeleteMotherAccountParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.MotherAccountDelete
	if !decodeJSON(w, r, &request) || !bool(request.Confirmed) {
		writeProblem(w, r, 422, "invalid_account_deletion", "Invalid Request", "Confirm mother account deletion", 0)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var version int64
	err = tx.QueryRow(r.Context(), `SELECT version FROM tsw_mother_accounts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, accountID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "mother_not_found", "Not Found", "Mother account was not found", 0)
		return
	}
	if err == nil {
		err = h.lockCurrentOwnerSession(r.Context(), tx, owner)
	}
	if errors.Is(err, errPersonalOwnerSession) {
		writeProblem(w, r, 401, "session_expired", "Session Expired", "Authentication is required", 0)
		return
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE tsw_mother_accounts SET status='disabled',deleted_at=now(),deleted_by=$2,version=version+1,updated_at=now() WHERE id=$1`, accountID, owner.OwnerID)
	}
	if err == nil {
		_, err = audit.Write(r.Context(), tx, audit.Event{
			Type: audit.MotherAccountDeleted, Actor: audit.ActorOwner, OwnerID: owner.OwnerID,
			EntityType: "mother_account", EntityID: accountID.String(), Outcome: audit.OutcomeSucceeded,
			CorrelationID: correlation(r), Details: audit.WorkspaceDetails{Result: "deleted"},
			IdempotencyKey: accountID.String() + ":deleted:" + strconv.FormatInt(version+1, 10),
		})
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
