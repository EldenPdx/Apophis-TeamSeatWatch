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

var errWorkspaceTokenRequired = errors.New("explicit Workspace token exchange required")

type workspaceTokenAttempt struct {
	binding    workspaceAccessBinding
	platformID string
	personal   platform.PersonalSession
}

func (h *OwnerAuthHandler) GetSelectedWorkspaceAccess(w http.ResponseWriter, r *http.Request, workspaceID openapi_types.UUID, params ownerapi.GetSelectedWorkspaceAccessParams) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	item, err := h.selectedWorkspaceAccess(r.Context(), workspaceID, params.MotherAccountId)
	if !h.selectedWorkspaceError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *OwnerAuthHandler) ExchangeSelectedWorkspaceToken(w http.ResponseWriter, r *http.Request, workspaceID openapi_types.UUID, _ ownerapi.ExchangeSelectedWorkspaceTokenParams) {
	if _, ok := h.authenticated(w, r, true); !ok {
		return
	}
	var request ownerapi.ExchangeSelectedWorkspaceTokenJSONRequestBody
	if !decodeJSON(w, r, &request) || !bool(request.Confirmed) {
		writeProblem(w, r, 422, "selection_required", "Selection Required", "Confirm the selected mother and canonical workspace", 0)
		return
	}
	if h.workspaceTokenExchanger == nil {
		writeProblem(w, r, 501, "workspace_exchange_unavailable", "Unavailable", "Workspace token exchange is not configured", 0)
		return
	}
	attempt, err := h.reserveWorkspaceTokenExchange(r.Context(), workspaceID, request.MotherAccountId)
	if !h.selectedWorkspaceError(w, r, err) {
		return
	}
	if attempt.binding.attempt == 0 {
		writeProblem(w, r, 403, "workspace_permission_denied", "Forbidden", "Discover and select a readable workspace", 0)
		return
	}
	var access platform.WorkspaceAccess
	if attempt.personal.AccessToken != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		access, err = h.workspaceTokenExchanger.ExchangeWorkspace(ctx, attempt.personal, attempt.platformID)
		cancel()
	} else {
		err = &platform.WorkspaceExchangeFailure{Code: "workspace_session_invalid"}
	}
	if err == nil && !platform.ValidateWorkspaceAccess(access, attempt.platformID, time.Now()) {
		err = platform.ErrWorkspaceExchangeUnavailable
	}
	if finishErr := h.finishWorkspaceTokenExchange(r.Context(), attempt, access, err); finishErr != nil {
		if errors.Is(finishErr, errSelectedAttemptSuperseded) {
			writeProblem(w, r, 409, "workspace_exchange_superseded", "Conflict", "The Personal or discovery generation changed", 0)
		} else {
			h.workspaceFailure(w, r, finishErr)
		}
		return
	}
	item, err := h.selectedWorkspaceAccess(r.Context(), workspaceID, request.MotherAccountId)
	if !h.selectedWorkspaceError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *OwnerAuthHandler) reserveWorkspaceTokenExchange(ctx context.Context, workspaceID, motherID uuid.UUID) (workspaceTokenAttempt, error) {
	var attempt workspaceTokenAttempt
	attempt.binding.motherID, attempt.binding.workspaceID = motherID, workspaceID
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return attempt, err
	}
	defer tx.Rollback(ctx)
	var access string
	var keyVersion int16
	var nonce, sealed []byte
	err = tx.QueryRow(ctx, `SELECT visibility.access_status,workspace.platform_workspace_id,discovery.run_id,session.generation,credential.secret_revision,session.key_version,session.nonce,session.sealed_session`+selectedWorkspaceFromSQL+` FOR SHARE OF account,credential,discovery,session,visibility`, workspaceID, motherID).Scan(&access, &attempt.platformID, &attempt.binding.run, &attempt.binding.generation, &attempt.binding.revision, &keyVersion, &nonce, &sealed)
	if err != nil {
		return attempt, err
	}
	if access != "readable" {
		return attempt, nil
	}
	attempt.binding.exchangeID = uuid.New()
	err = tx.QueryRow(ctx, `INSERT INTO tsw_selected_workspace_tokens(mother_account_id,workspace_id,discovery_run_id,session_generation,secret_revision,exchange_id,status)
		VALUES($1,$2,$3,$4,$5,$6,'exchanging') ON CONFLICT(mother_account_id,workspace_id) DO UPDATE SET
		discovery_run_id=EXCLUDED.discovery_run_id,session_generation=EXCLUDED.session_generation,secret_revision=EXCLUDED.secret_revision,
		attempt=tsw_selected_workspace_tokens.attempt+1,exchange_id=EXCLUDED.exchange_id,status='exchanging',key_version=NULL,nonce=NULL,sealed_access=NULL,expires_at=NULL,failure=NULL,updated_at=now()
		RETURNING attempt`, motherID, workspaceID, attempt.binding.run, attempt.binding.generation, attempt.binding.revision, attempt.binding.exchangeID).Scan(&attempt.binding.attempt)
	if err != nil {
		return attempt, err
	}
	if err = tx.Commit(ctx); err != nil {
		return attempt, err
	}
	attempt.personal, err = openPersonalSession(h.keyRing, motherID, attempt.binding.revision, uint16(keyVersion), nonce, sealed)
	if err != nil || !platform.ValidatePersonalRefresh(platform.PersonalRefreshResult{Status: "ready", Session: attempt.personal}, time.Now()) {
		attempt.personal = platform.PersonalSession{}
	}
	return attempt, nil
}

func (h *OwnerAuthHandler) finishWorkspaceTokenExchange(ctx context.Context, attempt workspaceTokenAttempt, access platform.WorkspaceAccess, exchangeErr error) error {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	b := attempt.binding
	var visibility string
	var run, generation uuid.UUID
	var revision int64
	err = tx.QueryRow(ctx, `SELECT visibility.access_status,discovery.run_id,session.generation,credential.secret_revision`+selectedWorkspaceFromSQL+` FOR SHARE OF account,credential,discovery,session,visibility`, b.workspaceID, b.motherID).Scan(&visibility, &run, &generation, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return errSelectedAttemptSuperseded
	}
	if err != nil {
		return err
	}
	if visibility != "readable" || run != b.run || generation != b.generation || revision != b.revision {
		return errSelectedAttemptSuperseded
	}
	var priorAttempt int64
	var priorID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT attempt,exchange_id FROM tsw_selected_workspace_tokens WHERE mother_account_id=$1 AND workspace_id=$2 AND status='exchanging' FOR UPDATE`, b.motherID, b.workspaceID).Scan(&priorAttempt, &priorID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (priorAttempt != b.attempt || priorID != b.exchangeID)) {
		return errSelectedAttemptSuperseded
	}
	if err != nil {
		return err
	}
	status := "failed"
	var keyVersion *int16
	var nonce, sealed []byte
	var failure []byte
	var expiry *time.Time
	if exchangeErr == nil {
		version, nonceValue, ciphertext, sealErr := sealWorkspaceAccess(h.keyRing, b, access)
		if sealErr != nil {
			return sealErr
		}
		v := int16(version)
		keyVersion, nonce, sealed, expiry = &v, nonceValue, ciphertext, &access.ExpiresAt
		status = "ready"
	} else {
		diagnostic := platform.WorkspaceExchangeDiagnostic(exchangeErr)
		failure, err = json.Marshal(diagnostic)
		if err != nil {
			return err
		}
		if diagnostic.HTTPStatus == http.StatusUnauthorized && (diagnostic.Code == "workspace_token_revoked" || diagnostic.Code == "workspace_session_expired") {
			// The scope locks above fence this observation to the exact Personal
			// generation. Preserve the attempt for GET and the stored materials
			// for a full login; never invalidate a newer login from a stale result.
			_, err = tx.Exec(ctx, `INSERT INTO tsw_mother_personal_access(mother_account_id,secret_revision,status)
				VALUES($1,$2,'session_expired') ON CONFLICT(mother_account_id) DO UPDATE SET status='session_expired',checked_at=now()
				WHERE tsw_mother_personal_access.secret_revision=$2 AND tsw_mother_personal_access.status<>'verifying'`, b.motherID, b.revision)
			if err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE tsw_selected_workspace_tokens SET status=$3,key_version=$4,nonce=$5,sealed_access=$6,expires_at=$7,failure=$10,updated_at=now() WHERE mother_account_id=$1 AND workspace_id=$2 AND attempt=$8 AND exchange_id=$9 AND status='exchanging'`, b.motherID, b.workspaceID, status, keyVersion, nonce, sealed, expiry, b.attempt, b.exchangeID, failure)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Only current, readable mother discovery and its Personal generation may
// reveal whether a sealed token exists. No token/cookie/session ID reaches JSON.
func (h *OwnerAuthHandler) selectedWorkspaceAccess(ctx context.Context, workspaceID, motherID uuid.UUID) (ownerapi.SelectedWorkspaceAccessStatus, error) {
	result := ownerapi.SelectedWorkspaceAccessStatus{WorkspaceId: workspaceID, MotherAccountId: motherID, Status: "required"}
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	var visible, platformID string
	var b workspaceAccessBinding
	b.motherID, b.workspaceID = motherID, workspaceID
	err = tx.QueryRow(ctx, `SELECT visibility.access_status,workspace.platform_workspace_id,discovery.run_id,session.generation,credential.secret_revision`+selectedWorkspaceFromSQL, workspaceID, motherID).Scan(&visible, &platformID, &b.run, &b.generation, &b.revision)
	if err != nil {
		return result, err
	}
	if visible != "readable" {
		result.Status = "permission_denied"
		return result, tx.Commit(ctx)
	}
	var status string
	var updated time.Time
	var expires *time.Time
	var keyVersion *int16
	var nonce, sealed, failure []byte
	err = tx.QueryRow(ctx, `SELECT status,attempt,exchange_id,updated_at,expires_at,key_version,nonce,sealed_access,failure FROM tsw_selected_workspace_tokens WHERE mother_account_id=$1 AND workspace_id=$2 AND discovery_run_id=$3 AND session_generation=$4 AND secret_revision=$5`, motherID, workspaceID, b.run, b.generation, b.revision).Scan(&status, &b.attempt, &b.exchangeID, &updated, &expires, &keyVersion, &nonce, &sealed, &failure)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, tx.Commit(ctx)
	}
	if err != nil {
		return result, err
	}
	result.CheckedAt = &updated
	switch status {
	case "exchanging":
		if updated.After(time.Now().Add(-time.Minute)) {
			result.Status = "exchanging"
		} else {
			result.Status = "failed"
		}
	case "failed":
		result.Status = "failed"
		if len(failure) > 0 {
			var diagnostic platform.WorkspaceExchangeFailure
			if err := json.Unmarshal(failure, &diagnostic); err != nil {
				return result, err
			}
			encoded, err := json.Marshal(platform.WorkspaceExchangeDiagnostic(&diagnostic))
			if err != nil {
				return result, err
			}
			if err := json.Unmarshal(encoded, &result.Failure); err != nil {
				return result, err
			}
		}
	case "ready":
		if expires != nil && expires.After(time.Now().Add(30*time.Second)) && keyVersion != nil {
			access, openErr := openWorkspaceAccess(h.keyRing, b, uint16(*keyVersion), nonce, sealed)
			if openErr == nil && platform.ValidateWorkspaceAccess(access, platformID, time.Now()) && !access.ExpiresAt.Truncate(time.Microsecond).After(*expires) {
				result.Status, result.ExpiresAt, result.ExchangeId = "ready", expires, &b.exchangeID
			}
		}
	}
	return result, tx.Commit(ctx)
}

func (h *OwnerAuthHandler) currentWorkspaceAccessTx(ctx context.Context, tx pgx.Tx, b workspaceAccessBinding, platformID string, lock bool) (platform.WorkspaceAccess, workspaceAccessBinding, error) {
	var access platform.WorkspaceAccess
	var keyVersion int16
	var nonce, sealed []byte
	var expires time.Time
	query := `SELECT attempt,exchange_id,key_version,nonce,sealed_access,expires_at FROM tsw_selected_workspace_tokens
		WHERE mother_account_id=$1 AND workspace_id=$2 AND discovery_run_id=$3 AND session_generation=$4 AND secret_revision=$5 AND status='ready' AND expires_at>now()+interval '30 seconds'`
	if lock {
		query += ` FOR SHARE`
	}
	err := tx.QueryRow(ctx, query, b.motherID, b.workspaceID, b.run, b.generation, b.revision).Scan(&b.attempt, &b.exchangeID, &keyVersion, &nonce, &sealed, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return access, b, errWorkspaceTokenRequired
	}
	if err != nil {
		return access, b, err
	}
	access, err = openWorkspaceAccess(h.keyRing, b, uint16(keyVersion), nonce, sealed)
	if err != nil || !platform.ValidateWorkspaceAccess(access, platformID, time.Now()) || access.ExpiresAt.Truncate(time.Microsecond).After(expires) {
		return platform.WorkspaceAccess{}, b, errWorkspaceTokenRequired
	}
	return access, b, nil
}
