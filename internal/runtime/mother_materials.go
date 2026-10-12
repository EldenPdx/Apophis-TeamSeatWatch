package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/audit"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/identity"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/mothersecret"
)

func materialString(value string) *string { return &value }

type parsedMotherMaterial struct {
	identifier string
	password   string
	totp       string
}

// parseMotherMaterialTXT deliberately accepts incomplete TOTP material. An
// owner can save the account now and repair the missing material later; this
// parser never turns that state into a verified platform connection.
func parseMotherMaterialTXT(content string) ([]parsedMotherMaterial, []ownerapi.MotherAccountImportRow) {
	materials := make([]parsedMotherMaterial, 0)
	rows := make([]ownerapi.MotherAccountImportRow, 0)
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSuffix(scanner.Text(), "\r")
		parts := strings.Split(line, "----")
		row := ownerapi.MotherAccountImportRow{Line: lineNumber}
		if len(parts) != 3 {
			row.Status = ownerapi.MotherAccountImportRowStatusInvalid
			row.Message = materialString("需要账号----密码----2FA三项")
			rows = append(rows, row)
			materials = append(materials, parsedMotherMaterial{})
			continue
		}
		identifier, password, totp := strings.TrimSpace(parts[0]), parts[1], strings.TrimSpace(parts[2])
		if !validLength(identifier, 1, 254) || strings.ContainsAny(identifier, "\r\n") ||
			!validLength(password, 1, 1024) {
			row.Status = ownerapi.MotherAccountImportRowStatusInvalid
			row.Message = materialString("账号或密码无效")
			rows = append(rows, row)
			materials = append(materials, parsedMotherMaterial{})
			continue
		}
		row.Identifier = &identifier
		if totp == "" {
			row.Status = ownerapi.MotherAccountImportRowStatusNeedsTotp
			row.Message = materialString("2FA待补")
		} else if _, err := auth.TOTPCode(totp, timeNow()); err != nil {
			row.Status = ownerapi.MotherAccountImportRowStatusNeedsTotp
			row.Message = materialString("2FA格式待修正")
		} else {
			row.Status = ownerapi.MotherAccountImportRowStatusImported
		}
		rows = append(rows, row)
		materials = append(materials, parsedMotherMaterial{identifier: identifier, password: password, totp: totp})
	}
	return materials, rows
}

// timeNow is a variable to keep material parsing deterministic in tests that
// need to validate a syntactically valid TOTP secret.
var timeNow = func() time.Time { return time.Now() }

func (h *OwnerAuthHandler) importMotherAccounts(w http.ResponseWriter, r *http.Request, _ ownerapi.ImportMotherAccountsParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.ImportMotherAccountsJSONRequestBody
	if !decodeJSON(w, r, &request) || len(request.Content) > 10*1024*1024 {
		h.rejectOwnerMutation(w, r, owner, "mother_account.import", "invalid_request", http.StatusUnprocessableEntity, "invalid_materials", "Invalid Materials", "资料文件无效")
		return
	}
	parsed, rows := parseMotherMaterialTXT(request.Content)
	result := ownerapi.MotherAccountImportResult{Rows: rows}
	if len(result.Rows) == 0 {
		writeJSON(w, http.StatusOK, result)
		return
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	seen := make(map[string]struct{})
	type importedAccount struct {
		id       uuid.UUID
		restored bool
		version  int64
	}
	importedAccounts := make([]importedAccount, 0)
	changed := false
	for index := range result.Rows {
		row := &result.Rows[index]
		material := parsed[index]
		switch row.Status {
		case ownerapi.MotherAccountImportRowStatusInvalid:
			result.Invalid++
			continue
		}
		identifier := material.identifier
		key, keyVersion, fingerprint, fingerprintErr := identity.Fingerprint(h.keyRing, identity.MotherLogin, identifier)
		if fingerprintErr != nil {
			row.Status = ownerapi.MotherAccountImportRowStatusInvalid
			row.Message = materialString("账号无效")
			result.Invalid++
			continue
		}
		if _, exists := seen[key]; exists {
			row.Status = ownerapi.MotherAccountImportRowStatusDuplicate
			row.Message = materialString("文件内重复")
			result.Duplicate++
			continue
		}
		seen[key] = struct{}{}
		var accountID uuid.UUID
		var restored bool
		var revision, accountVersion int64
		err = tx.QueryRow(r.Context(), `SELECT account.id,account.deleted_at IS NOT NULL,credential.secret_revision,account.version FROM tsw_mother_account_credentials credential JOIN tsw_mother_accounts account ON account.id=credential.mother_account_id WHERE credential.identifier_key_version=$1 AND credential.identifier_hmac=$2 FOR UPDATE OF account,credential`, keyVersion, fingerprint[:]).Scan(&accountID, &restored, &revision, &accountVersion)
		if err == nil && !restored {
			row.Status = ownerapi.MotherAccountImportRowStatusDuplicate
			row.Message = materialString("资料已存在")
			result.Duplicate++
			continue
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			h.workspaceFailure(w, r, err)
			return
		}
		if restored {
			revision++
			err = tx.QueryRow(r.Context(), `UPDATE tsw_mother_accounts SET status='active',deleted_at=NULL,deleted_by=NULL,version=version+1,updated_at=now() WHERE id=$1 RETURNING version`, accountID).Scan(&accountVersion)
		} else {
			revision = 1
			err = tx.QueryRow(r.Context(), `INSERT INTO tsw_mother_accounts (display_name) VALUES ($1) RETURNING id,version`, identifier).Scan(&accountID, &accountVersion)
		}
		if err == nil {
			var sealedPassword, sealedTOTP []byte
			sealedPassword, err = mothersecret.Seal(h.keyRing, accountID, revision, mothersecret.Password, []byte(material.password))
			if err == nil && material.totp != "" {
				sealedTOTP, err = mothersecret.Seal(h.keyRing, accountID, revision, mothersecret.TOTP, []byte(material.totp))
			}
			if err == nil {
				if restored {
					_, err = tx.Exec(r.Context(), `UPDATE tsw_mother_account_credentials SET password_secret=$2,totp_secret=$3,secret_revision=$4,version=version+1,updated_at=now() WHERE mother_account_id=$1`, accountID, sealedPassword, sealedTOTP, revision)
				} else {
					_, err = tx.Exec(r.Context(), `INSERT INTO tsw_mother_account_credentials (mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret,totp_secret) VALUES ($1,$2,$3,$4,$5,$6)`, accountID, identifier, fingerprint[:], keyVersion, sealedPassword, sealedTOTP)
				}
			}
		}
		if err == nil && restored {
			err = clearMotherAccess(r.Context(), tx, accountID)
		}
		if err != nil {
			h.workspaceFailure(w, r, err)
			return
		}
		changed = true
		importedAccounts = append(importedAccounts, importedAccount{id: accountID, restored: restored, version: accountVersion})
		result.Imported++
	}
	var token string
	var idle time.Time
	if changed {
		for _, account := range importedAccounts {
			event := audit.Event{Type: audit.MotherAccountCreated, Actor: audit.ActorOwner, OwnerID: owner.OwnerID, EntityType: "mother_account", EntityID: account.id.String(), Outcome: audit.OutcomeSucceeded, CorrelationID: correlation(r), Details: audit.WorkspaceDetails{Result: "created"}, IdempotencyKey: account.id.String() + ":mother-import"}
			if account.restored {
				event.Type = audit.MotherAccountUpdated
				event.Details = audit.WorkspaceDetails{Result: "updated"}
				event.IdempotencyKey = fmt.Sprintf("%s:mother-restored:%d", account.id, account.version)
			}
			if _, err = audit.Write(r.Context(), tx, event); err != nil {
				h.workspaceFailure(w, r, err)
				return
			}
		}
		token, idle, err = h.rotateSessionTx(r.Context(), tx, owner, "session_revocation", r)
		if err != nil {
			h.workspaceFailure(w, r, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if changed {
		auth.SetSessionCookie(w, token, idle, h.secureCookies)
	}
	writeJSON(w, http.StatusOK, result)
}

// Credential replacement invalidates saved platform access, never business history.
func clearMotherAccess(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) error {
	for _, query := range []string{
		`DELETE FROM tsw_selected_workspace_tokens WHERE mother_account_id=$1`,
		`DELETE FROM tsw_mother_personal_sessions WHERE mother_account_id=$1`,
		`DELETE FROM tsw_mother_workspace_visibility WHERE mother_account_id=$1`,
		`DELETE FROM tsw_mother_discoveries WHERE mother_account_id=$1`,
		`DELETE FROM tsw_mother_personal_access WHERE mother_account_id=$1`,
	} {
		if _, err := tx.Exec(ctx, query, accountID); err != nil {
			return err
		}
	}
	return nil
}

func (h *OwnerAuthHandler) exportMotherAccounts(w http.ResponseWriter, r *http.Request, _ ownerapi.ExportMotherAccountsParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.ExportMotherAccountsJSONRequestBody
	if !decodeJSON(w, r, &request) || request.ExpectedCount < 1 {
		h.rejectOwnerMutation(w, r, owner, "mother_account.export", "invalid_request", http.StatusUnprocessableEntity, "invalid_export", "Invalid Export", "导出范围无效")
		return
	}
	if !request.Confirmed {
		h.rejectOwnerMutation(w, r, owner, "mother_account.export", "confirmation_required", http.StatusPreconditionRequired, "export_confirmation_required", "Confirmation Required", "请确认导出范围与数量")
		return
	}

	query := `SELECT account.id,credential.secret_revision,credential.login_identifier,credential.password_secret,credential.totp_secret
		FROM tsw_mother_account_credentials credential JOIN tsw_mother_accounts account ON account.id=credential.mother_account_id WHERE account.deleted_at IS NULL`
	var args []any
	if request.AccountIds != nil {
		query += ` AND account.id = ANY($1)`
		args = append(args, *request.AccountIds)
	}
	query += ` ORDER BY credential.login_identifier ASC`
	rows, err := h.pool.Query(r.Context(), query, args...)
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer rows.Close()
	var output strings.Builder
	count := 0
	for rows.Next() {
		var accountID uuid.UUID
		var revision int64
		var identifier string
		var sealedPassword, sealedTOTP []byte
		if err = rows.Scan(&accountID, &revision, &identifier, &sealedPassword, &sealedTOTP); err != nil {
			h.workspaceFailure(w, r, err)
			return
		}
		password, openErr := mothersecret.Open(h.keyRing, accountID, revision, mothersecret.Password, sealedPassword)
		if openErr != nil {
			h.workspaceFailure(w, r, openErr)
			return
		}
		var totp []byte
		if sealedTOTP != nil {
			totp, openErr = mothersecret.Open(h.keyRing, accountID, revision, mothersecret.TOTP, sealedTOTP)
		}
		if openErr != nil {
			clear(password)
			h.workspaceFailure(w, r, openErr)
			return
		}
		_, _ = fmt.Fprintf(&output, "%s----%s----%s\n", identifier, password, totp)
		clear(password)
		clear(totp)
		count++
	}
	if err = rows.Err(); err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if count != request.ExpectedCount {
		h.rejectOwnerMutation(w, r, owner, "mother_account.export", "range_changed", http.StatusConflict, "export_range_changed", "Export Range Changed", "导出范围已变化，请重新确认")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mother-account-materials.txt"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(output.String()))
}
