package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/mothersecret"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

const motherPersonalRefreshTimeout = 120 * time.Second
const motherPersonalRefreshStaleAfter = motherPersonalRefreshTimeout + 10*time.Second

func (h *OwnerAuthHandler) GetMotherPersonalAccess(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	result, err := h.motherPersonalAccess(r, id)
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

type personalRefreshAttempt struct {
	material platform.MotherMaterial
	revision int64
	attempt  int64
}

func (h *OwnerAuthHandler) reservePersonalRefresh(ctx context.Context, id uuid.UUID) (personalRefreshAttempt, bool, error) {
	var attempt personalRefreshAttempt
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return attempt, false, err
	}
	defer tx.Rollback(ctx)
	var password, totp []byte
	var active bool
	err = tx.QueryRow(ctx, `SELECT credential.login_identifier,credential.password_secret,credential.totp_secret,credential.secret_revision,account.status='active' FROM tsw_mother_accounts account JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=account.id WHERE account.id=$1 FOR UPDATE OF account,credential`, id).Scan(&attempt.material.LoginIdentifier, &password, &totp, &attempt.revision, &active)
	if err != nil || !active {
		return attempt, active, err
	}
	plainPassword, openErr := mothersecret.Open(h.keyRing, id, attempt.revision, mothersecret.Password, password)
	if openErr != nil {
		return attempt, active, openErr
	}
	var plainTOTP []byte
	if totp != nil {
		plainTOTP, openErr = mothersecret.Open(h.keyRing, id, attempt.revision, mothersecret.TOTP, totp)
	}
	if openErr != nil {
		clear(plainPassword)
		return attempt, active, openErr
	}
	attempt.material.Password, attempt.material.TOTPSecret = string(plainPassword), string(plainTOTP)
	clear(plainPassword)
	clear(plainTOTP)
	err = tx.QueryRow(ctx, `INSERT INTO tsw_mother_personal_access(mother_account_id,secret_revision,status) VALUES ($1,$2,'verifying') ON CONFLICT(mother_account_id) DO UPDATE SET secret_revision=EXCLUDED.secret_revision,attempt=tsw_mother_personal_access.attempt+1,status='verifying',failure=NULL,checked_at=now() RETURNING attempt`, id, attempt.revision).Scan(&attempt.attempt)
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM tsw_selected_workspace_tokens WHERE mother_account_id=$1`, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM tsw_mother_personal_sessions WHERE mother_account_id=$1`, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM tsw_mother_workspace_visibility WHERE mother_account_id=$1`, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM tsw_mother_discoveries WHERE mother_account_id=$1`, id)
	}
	if err != nil {
		return attempt, active, err
	}
	return attempt, active, tx.Commit(ctx)
}

func (h *OwnerAuthHandler) RefreshMotherPersonalAccess(w http.ResponseWriter, r *http.Request, id openapi_types.UUID, _ ownerapi.RefreshMotherPersonalAccessParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	attempt, active, err := h.reservePersonalRefresh(r.Context(), id)
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
	// A reserved login belongs to the service, so refreshing the page cannot
	// cancel its result publication. Allow for the shared lease wait and TOTP
	// rollover as well as the measured full login; keep every stage bounded.
	taskCtx, stopTask := context.WithTimeout(context.WithoutCancel(r.Context()), motherPersonalRefreshTimeout)
	defer stopTask()
	r = r.WithContext(taskCtx)
	persisted := false
	defer func() {
		if !persisted {
			h.finishMotherRefreshAttempt(taskCtx, id, attempt)
		}
	}()
	status := "missing_credentials"
	var session platform.PersonalSession
	var failure []byte
	_, totpErr := auth.TOTPCode(attempt.material.TOTPSecret, time.Now())
	if strings.TrimSpace(attempt.material.LoginIdentifier) != "" && attempt.material.Password != "" && totpErr == nil {
		adapter := h.personalRefresh
		if adapter == nil {
			adapter = platform.UnavailablePersonalRefresh{}
		}
		result, refreshErr := adapter.RefreshPersonal(taskCtx, attempt.material)
		switch {
		case errors.Is(refreshErr, platform.ErrPersonalRefreshUnavailable):
			status = "unavailable"
		case refreshErr != nil || !platform.ValidatePersonalRefresh(result, time.Now()):
			status = "refresh_failed"
			code := "login_request_failed"
			if errors.Is(refreshErr, context.DeadlineExceeded) || errors.Is(taskCtx.Err(), context.DeadlineExceeded) {
				code = "login_task_timeout"
			} else if errors.Is(refreshErr, context.Canceled) {
				code = "login_task_interrupted"
			}
			failure, _ = json.Marshal(platform.PersonalRefreshFailure{Code: code})
		default:
			status, session = result.Status, result.Session
			if result.Failure != nil {
				failure, _ = json.Marshal(result.Failure)
			}
		}
	}
	// Result persistence needs its own short deadline even when the platform
	// attempt has exhausted its time budget.
	publishCtx, stopPublish := context.WithTimeout(context.WithoutCancel(taskCtx), 5*time.Second)
	defer stopPublish()
	r = r.WithContext(publishCtx)
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var revision, currentAttempt int64
	var stillActive bool
	err = tx.QueryRow(r.Context(), `SELECT credential.secret_revision,access.attempt,account.status='active' FROM tsw_mother_accounts account JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=account.id JOIN tsw_mother_personal_access access ON access.mother_account_id=account.id WHERE account.id=$1 FOR UPDATE OF account,credential,access`, id).Scan(&revision, &currentAttempt, &stillActive)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!stillActive || revision != attempt.revision || currentAttempt != attempt.attempt)) {
		writeProblem(w, r, http.StatusConflict, "mother_access_changed", "Conflict", "Mother access changed; verify again", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE tsw_mother_personal_access SET status=$2,failure=$3,checked_at=now() WHERE mother_account_id=$1`, id, status, failure)
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if status == "ready" {
		version, nonce, sealed, sealErr := sealPersonalSession(h.keyRing, id, attempt.revision, session)
		if sealErr != nil {
			h.workspaceFailure(w, r, sealErr)
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO tsw_mother_personal_sessions(mother_account_id,secret_revision,generation,key_version,nonce,sealed_session,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, attempt.revision, uuid.New(), version, nonce, sealed, session.ExpiresAt)
		if err != nil {
			h.workspaceFailure(w, r, err)
			return
		}
	}
	if status == "ready" {
		err = h.lockCurrentOwnerSession(r.Context(), tx, owner)
	}
	if errors.Is(err, errPersonalOwnerSession) {
		writeProblem(w, r, http.StatusUnauthorized, "session_expired", "Session Expired", "The Owner session changed before mother login publication", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	persisted = true
	response, err := h.motherPersonalAccess(r, id)
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *OwnerAuthHandler) finishMotherRefreshAttempt(parent context.Context, id uuid.UUID, attempt personalRefreshAttempt) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	failure, _ := json.Marshal(platform.PersonalRefreshFailure{Code: "login_result_not_saved"})
	_, err := h.pool.Exec(ctx, `UPDATE tsw_mother_personal_access SET status='refresh_failed',failure=$4,checked_at=now() WHERE mother_account_id=$1 AND secret_revision=$2 AND attempt=$3 AND status='verifying'`, id, attempt.revision, attempt.attempt, failure)
	if err != nil {
		slog.ErrorContext(ctx, "mother_login_result_cleanup_failed", "error", err)
	}
}

func (h *OwnerAuthHandler) motherPersonalAccess(r *http.Request, id uuid.UUID) (ownerapi.MotherPersonalAccess, error) {
	response := ownerapi.MotherPersonalAccess{MotherAccountId: id, Status: ownerapi.MotherPersonalAccessStatusNotVerified}
	var status *string
	var checked *time.Time
	var failure []byte
	err := h.pool.QueryRow(r.Context(), `SELECT CASE WHEN access.status='ready' AND (session.mother_account_id IS NULL OR session.expires_at<=now()) THEN NULL ELSE access.status END,access.checked_at,access.failure FROM tsw_mother_accounts account JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=account.id LEFT JOIN tsw_mother_personal_access access ON access.mother_account_id=account.id AND access.secret_revision=credential.secret_revision LEFT JOIN tsw_mother_personal_sessions session ON session.mother_account_id=account.id AND session.secret_revision=credential.secret_revision WHERE account.id=$1 AND account.status='active'`, id).Scan(&status, &checked, &failure)
	if err != nil {
		return response, err
	}
	if status != nil {
		if *status == "verifying" && checked != nil && checked.Before(time.Now().Add(-motherPersonalRefreshStaleAfter)) {
			*status = "refresh_failed"
			failure, _ = json.Marshal(platform.PersonalRefreshFailure{Code: "login_task_interrupted"})
		}
		response.Status = ownerapi.MotherPersonalAccessStatus(*status)
		response.CheckedAt = checked
		if *status == "refresh_failed" && len(failure) > 0 {
			var diagnostic platform.PersonalRefreshFailure
			if err := json.Unmarshal(failure, &diagnostic); err != nil {
				return response, err
			}
			if platform.ValidatePersonalRefresh(platform.PersonalRefreshResult{Status: "refresh_failed", Failure: &diagnostic}, time.Now()) {
				if err := json.Unmarshal(failure, &response.Failure); err != nil {
					return response, err
				}
			}
		}
	}
	return response, nil
}
