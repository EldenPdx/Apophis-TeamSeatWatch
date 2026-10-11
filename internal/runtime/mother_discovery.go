package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func (h *OwnerAuthHandler) GetMotherDiscovery(w http.ResponseWriter, r *http.Request, accountID openapi_types.UUID) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	result, err := h.motherDiscovery(r, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "mother_not_found", "Not Found", "Mother account was not found", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type discoveryAttempt struct {
	revision, attempt int64
	generation        *uuid.UUID
	keyVersion        *int16
	nonce, ciphertext []byte
	expires           *time.Time
}

func (h *OwnerAuthHandler) reserveMotherDiscovery(ctx context.Context, id uuid.UUID) (discoveryAttempt, bool, error) {
	var attempt discoveryAttempt
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return attempt, false, err
	}
	defer tx.Rollback(ctx)
	var active bool
	err = tx.QueryRow(ctx, `SELECT credential.secret_revision,account.status='active',session.generation,session.key_version,session.nonce,session.sealed_session,session.expires_at FROM tsw_mother_accounts account JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=account.id LEFT JOIN tsw_mother_personal_sessions session ON session.mother_account_id=account.id AND session.secret_revision=credential.secret_revision WHERE account.id=$1 FOR UPDATE OF account,credential`, id).Scan(&attempt.revision, &active, &attempt.generation, &attempt.keyVersion, &attempt.nonce, &attempt.ciphertext, &attempt.expires)
	if err != nil || !active {
		return attempt, active, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO tsw_mother_discoveries(mother_account_id,run_id,secret_revision,session_generation,status) VALUES ($1,$2,$3,$4,'discovering') ON CONFLICT(mother_account_id) DO UPDATE SET run_id=EXCLUDED.run_id,attempt=tsw_mother_discoveries.attempt+1,secret_revision=EXCLUDED.secret_revision,session_generation=EXCLUDED.session_generation,status='discovering',failure=NULL,observed_at=now() RETURNING attempt`, id, uuid.New(), attempt.revision, attempt.generation).Scan(&attempt.attempt)
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM tsw_selected_workspace_tokens WHERE mother_account_id=$1`, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM tsw_mother_workspace_visibility WHERE mother_account_id=$1`, id)
	}
	if err != nil {
		return attempt, active, err
	}
	return attempt, active, tx.Commit(ctx)
}

func (h *OwnerAuthHandler) RunMotherDiscovery(w http.ResponseWriter, r *http.Request, accountID openapi_types.UUID, _ ownerapi.RunMotherDiscoveryParams) {
	if _, ok := h.authenticated(w, r, true); !ok {
		return
	}
	attempt, active, err := h.reserveMotherDiscovery(r.Context(), accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "mother_not_found", "Not Found", "Mother account was not found", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if !active {
		writeProblem(w, r, http.StatusConflict, "mother_disabled", "Account Disabled", "Mother account is disabled", 0)
		return
	}
	status := "missing_credentials"
	var found []platform.DiscoveredWorkspace
	var failure []byte
	if attempt.generation != nil && attempt.expires != nil && !attempt.expires.After(time.Now()) {
		status = "session_expired"
	}
	if attempt.generation != nil && attempt.keyVersion != nil && attempt.expires != nil && attempt.expires.After(time.Now()) {
		session, openErr := openPersonalSession(h.keyRing, accountID, attempt.revision, uint16(*attempt.keyVersion), attempt.nonce, attempt.ciphertext)
		if openErr != nil || !platform.ValidatePersonalRefresh(platform.PersonalRefreshResult{Status: "ready", Session: session}, time.Now()) {
			status = "discovery_failed"
		} else {
			adapter := h.discovery
			if adapter == nil {
				adapter = platform.UnavailableDiscovery{}
			}
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			result, discoverErr := adapter.Discover(ctx, session)
			cancel()
			switch {
			case errors.Is(discoverErr, platform.ErrDiscoveryUnavailable):
				status = "unavailable"
			case discoverErr != nil || !platform.ValidateDiscovery(result):
				status = "discovery_failed"
			default:
				status = result.Status
				if result.Failure != nil {
					failure, _ = json.Marshal(platform.WorkspaceExchangeDiagnostic(result.Failure))
				}
				if status == "discovered" {
					found = result.Workspaces
					if len(found) == 0 {
						status = "empty"
					}
				}
			}
		}
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var revision, currentAttempt int64
	var activeNow bool
	var reserved, current *uuid.UUID
	var expires *time.Time
	var access *string
	err = tx.QueryRow(r.Context(), `SELECT credential.secret_revision,account.status='active',discovery.attempt,discovery.session_generation,session.generation,session.expires_at,access.status FROM tsw_mother_accounts account JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=account.id JOIN tsw_mother_discoveries discovery ON discovery.mother_account_id=account.id LEFT JOIN tsw_mother_personal_sessions session ON session.mother_account_id=account.id AND session.secret_revision=credential.secret_revision LEFT JOIN tsw_mother_personal_access access ON access.mother_account_id=account.id AND access.secret_revision=credential.secret_revision WHERE account.id=$1 FOR UPDATE OF account,credential,discovery`, accountID).Scan(&revision, &activeNow, &currentAttempt, &reserved, &current, &expires, &access)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!activeNow || revision != attempt.revision || currentAttempt != attempt.attempt || !sameUUID(reserved, attempt.generation) || !sameUUID(current, attempt.generation))) {
		writeProblem(w, r, http.StatusConflict, "mother_access_changed", "Conflict", "Mother access changed; verify again", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if attempt.generation != nil && (access == nil || *access != "ready" || expires == nil) {
		writeProblem(w, r, http.StatusConflict, "mother_access_changed", "Conflict", "Mother access changed; verify again", 0)
		return
	}
	if expires != nil && !expires.After(time.Now()) {
		status, found = "session_expired", nil
	}
	if status == "session_expired" {
		_, err = tx.Exec(r.Context(), `DELETE FROM tsw_mother_personal_sessions WHERE mother_account_id=$1 AND generation=$2`, accountID, current)
		if err != nil {
			h.workspaceFailure(w, r, err)
			return
		}
	}
	var runID uuid.UUID
	err = tx.QueryRow(r.Context(), `UPDATE tsw_mother_discoveries SET status=$2,failure=$4,observed_at=now() WHERE mother_account_id=$1 AND attempt=$3 RETURNING run_id`, accountID, status, attempt.attempt, failure).Scan(&runID)
	for _, item := range found {
		if err != nil {
			break
		}
		var workspaceID uuid.UUID
		// Canonical identity is the platform ID; visibility does not create a binding.
		err = tx.QueryRow(r.Context(), `INSERT INTO tsw_workspaces(platform_workspace_id,display_name) VALUES ($1,$2) ON CONFLICT (platform_workspace_id) DO UPDATE SET platform_workspace_id=EXCLUDED.platform_workspace_id RETURNING id`, item.PlatformID, item.Name).Scan(&workspaceID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO tsw_workspace_projections(workspace_id) VALUES ($1) ON CONFLICT (workspace_id) DO NOTHING`, workspaceID)
		}
		if err == nil {
			role := item.Role
			if role == "" {
				role = "unknown"
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO tsw_mother_workspace_visibility(mother_account_id,workspace_id,run_id,access_status,workspace_role) VALUES ($1,$2,$3,$4,$5)`, accountID, workspaceID, runID, item.Access, role)
		}
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	response, err := h.motherDiscovery(r, accountID)
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func sameUUID(a, b *uuid.UUID) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func (h *OwnerAuthHandler) motherDiscovery(r *http.Request, accountID uuid.UUID) (ownerapi.MotherDiscovery, error) {
	// Both status and visibility must use the same committed snapshot. A newer
	// reservation can clear visibility between two READ COMMITTED statements.
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ownerapi.MotherDiscovery{}, err
	}
	defer tx.Rollback(r.Context())
	return motherDiscoverySnapshot(r.Context(), tx, accountID)
}

func motherDiscoverySnapshot(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) (ownerapi.MotherDiscovery, error) {
	response := ownerapi.MotherDiscovery{MotherAccountId: accountID, Status: ownerapi.MotherDiscoveryStatusNotVerified, Workspaces: []ownerapi.MotherVisibleWorkspace{}}
	var status *string
	var observed *time.Time
	var failure []byte
	err := tx.QueryRow(ctx, `SELECT CASE WHEN discovery.status IN ('discovered','empty') AND (session.expires_at IS NULL OR session.expires_at<=now()) THEN NULL WHEN discovery.status='discovering' AND discovery.observed_at<now()-interval '1 minute' THEN 'discovery_failed' ELSE discovery.status END,discovery.observed_at,discovery.failure FROM tsw_mother_accounts account JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=account.id LEFT JOIN tsw_mother_discoveries discovery ON discovery.mother_account_id=account.id AND discovery.secret_revision=credential.secret_revision AND discovery.observed_at>now()-interval '7 days' LEFT JOIN tsw_mother_personal_sessions session ON session.mother_account_id=account.id AND session.secret_revision=credential.secret_revision AND session.generation=discovery.session_generation WHERE account.id=$1 AND account.status='active'`, accountID).Scan(&status, &observed, &failure)
	if err != nil {
		return response, err
	}
	if status == nil {
		return response, nil
	}
	response.Status, response.ObservedAt = ownerapi.MotherDiscoveryStatus(*status), observed
	if *status != "discovered" {
		if len(failure) > 0 {
			var diagnostic platform.WorkspaceExchangeFailure
			if err := json.Unmarshal(failure, &diagnostic); err != nil {
				return response, err
			}
			encoded, err := json.Marshal(platform.WorkspaceExchangeDiagnostic(&diagnostic))
			if err != nil {
				return response, err
			}
			if err := json.Unmarshal(encoded, &response.Failure); err != nil {
				return response, err
			}
		}
		return response, nil
	}
	rows, err := tx.Query(ctx, `SELECT workspace.id,workspace.display_name,visibility.access_status FROM tsw_mother_workspace_visibility visibility JOIN tsw_workspaces workspace ON workspace.id=visibility.workspace_id JOIN tsw_mother_discoveries discovery ON discovery.mother_account_id=visibility.mother_account_id AND discovery.run_id=visibility.run_id AND discovery.observed_at>now()-interval '7 days' JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=visibility.mother_account_id AND credential.secret_revision=discovery.secret_revision JOIN tsw_mother_personal_sessions session ON session.mother_account_id=visibility.mother_account_id AND session.secret_revision=credential.secret_revision AND session.generation=discovery.session_generation AND session.expires_at>now() WHERE visibility.mother_account_id=$1 ORDER BY workspace.display_name,workspace.id`, accountID)
	if err != nil {
		return response, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ownerapi.MotherVisibleWorkspace
		if err := rows.Scan(&item.Id, &item.DisplayName, &item.AccessStatus); err != nil {
			return response, err
		}
		response.Workspaces = append(response.Workspaces, item)
	}
	return response, rows.Err()
}
