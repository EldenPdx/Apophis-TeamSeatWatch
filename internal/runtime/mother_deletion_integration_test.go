//go:build integration

package runtime

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/mothersecret"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func TestMotherDeletionRetainsCredentialsRelationshipsAndHistory(t *testing.T) {
	pool, h, owner, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	var mother uuid.UUID
	var version int64
	if err := pool.QueryRow(ctx, `SELECT mother_account_id FROM tsw_mother_workspace_bindings LIMIT 1`).Scan(&mother); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT version FROM tsw_mother_accounts WHERE id=$1`, mother).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tsw_mother_personal_access(mother_account_id,secret_revision,status) SELECT mother_account_id,secret_revision,'refresh_failed' FROM tsw_mother_account_credentials WHERE mother_account_id=$1`, mother); err != nil {
		t.Fatal(err)
	}
	tables := []string{"tsw_mother_account_credentials", "tsw_mother_personal_access", "tsw_mother_personal_sessions", "tsw_mother_workspace_visibility", "tsw_mother_discoveries", "tsw_selected_workspace_tokens", "tsw_workspaces", "tsw_mother_workspace_bindings", "tsw_batches", "tsw_batch_memberships", "tsw_operations", "tsw_operation_targets", "tsw_oauth_assets", "tsw_delivery_versions", "tsw_cards", "tsw_orders", "tsw_rotation_global_protections"}
	snapshots := make(map[string]string)
	for _, table := range tables {
		var snapshot string
		if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(record) ORDER BY to_jsonb(record)::text),'[]')::text FROM %s record`, table)).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		snapshots[table] = snapshot
	}
	endpoint := "/api/owner/v1/mother-accounts/" + mother.String()
	body := map[string]any{"confirmed": true}
	for _, tc := range []struct {
		name, session, csrf string
		body                any
		status              int
	}{
		{"unauthenticated", "", csrf, body, 401},
		{"csrf", session, "wrong", body, 403},
		{"unconfirmed", session, csrf, map[string]any{"confirmed": false}, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := standbyDeletionRequest(t, h, tc.session, tc.csrf, "DELETE", endpoint, tc.body)
			if r.Code != tc.status {
				t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
			}
		})
	}
	if r := standbyDeletionRequest(t, h, session, csrf, "DELETE", endpoint, body); r.Code != 204 {
		t.Fatalf("delete=%d %s", r.Code, r.Body.String())
	}
	var deleted bool
	var deletedBy, status string
	var afterVersion int64
	if err := pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL,deleted_by::text,status,version FROM tsw_mother_accounts WHERE id=$1`, mother).Scan(&deleted, &deletedBy, &status, &afterVersion); err != nil || !deleted || deletedBy != owner || status != "disabled" || afterVersion != version+1 {
		t.Fatalf("deletion metadata=%v %s %s %d %v", deleted, deletedBy, status, afterVersion, err)
	}
	for _, table := range tables {
		var after string
		if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(record) ORDER BY to_jsonb(record)::text),'[]')::text FROM %s record`, table)).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != snapshots[table] {
			t.Errorf("deletion changed retained records in %s", table)
		}
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_audit_events WHERE event_type='mother_account.deleted' AND entity_id=$1 AND owner_id=$2`, mother.String(), owner).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("deletion audit=%d %v", auditCount, err)
	}
	list := decodeStandby[ownerapi.MotherAccountList](t, standbyDeletionRequest(t, h, session, csrf, "GET", "/api/owner/v1/mother-accounts", nil), 200)
	for _, item := range list.Items {
		if item.Id == mother {
			t.Fatal("deleted mother still listed")
		}
	}
	if r := standbyDeletionRequest(t, h, session, csrf, "DELETE", endpoint, body); r.Code != 404 {
		t.Fatalf("repeat deletion=%d %s", r.Code, r.Body.String())
	}
	for _, suffix := range []string{"/personal-session", "/discovery"} {
		if r := standbyDeletionRequest(t, h, session, csrf, "POST", endpoint+suffix, nil); r.Code != 404 {
			t.Errorf("deleted account action %s=%d %s", suffix, r.Code, r.Body.String())
		}
	}
	if r := standbyDeletionRequest(t, h, session, csrf, "POST", "/api/owner/v1/mother-accounts/export", map[string]any{"accountIds": []uuid.UUID{mother}, "expectedCount": 1, "confirmed": true}); r.Code != 409 {
		t.Fatalf("deleted account export=%d %s", r.Code, r.Body.String())
	}
}

func TestMotherDeletionSupportsExpiredDisabledAndReimportedAccounts(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	for index, status := range []string{"active", "disabled", "active"} {
		identifier := fmt.Sprintf("removed%d@example.test", index)
		content := identifier + "----old-password----JBSWY3DPEHPK3PXP"
		imported := standbyDeletionRequest(t, h, session, csrf, "POST", "/api/owner/v1/mother-accounts/import", map[string]any{"content": content})
		decodeStandby[ownerapi.MotherAccountImportResult](t, imported, 200)
		session = childRotatedSession(t, imported)
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT mother_account_id FROM tsw_mother_account_credentials WHERE login_identifier=$1`, identifier).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE tsw_mother_accounts SET status=$2 WHERE id=$1`, id, status); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO tsw_mother_discoveries(mother_account_id,run_id,secret_revision,status) VALUES($1,$2,1,'session_expired')`, id, uuid.New()); err != nil {
			t.Fatal(err)
		}
		if index == 2 {
			if _, err := pool.Exec(ctx, `INSERT INTO tsw_mother_personal_access(mother_account_id,secret_revision,status) VALUES($1,1,'verifying')`, id); err != nil {
				t.Fatal(err)
			}
		}
		endpoint := "/api/owner/v1/mother-accounts/" + id.String()
		if r := standbyDeletionRequest(t, h, session, csrf, "DELETE", endpoint, map[string]any{"confirmed": true}); r.Code != 204 {
			t.Fatalf("%s deletion=%d %s", status, r.Code, r.Body.String())
		}
		if r := standbyDeletionRequest(t, h, session, csrf, "POST", endpoint+"/personal-session", nil); r.Code != 409 {
			t.Fatalf("removed account started another login=%d %s", r.Code, r.Body.String())
		}
		imported = standbyDeletionRequest(t, h, session, csrf, "POST", "/api/owner/v1/mother-accounts/import", map[string]any{"content": strings.Replace(content, "old-password", "new-password", 1)})
		result := decodeStandby[ownerapi.MotherAccountImportResult](t, imported, 200)
		if result.Imported != 1 || result.Duplicate != 0 {
			t.Fatalf("removed mother was not reimported: %+v", result)
		}
		session = childRotatedSession(t, imported)
		var restoredID uuid.UUID
		var deleted bool
		var restoredStatus string
		var revision int64
		var password []byte
		if err := pool.QueryRow(ctx, `SELECT account.id,account.deleted_at IS NOT NULL,account.status,credential.secret_revision,credential.password_secret FROM tsw_mother_accounts account JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=account.id WHERE credential.login_identifier=$1`, identifier).Scan(&restoredID, &deleted, &restoredStatus, &revision, &password); err != nil {
			t.Fatal(err)
		}
		opened, err := mothersecret.Open(h.keyRing, restoredID, revision, mothersecret.Password, password)
		if err != nil || restoredID != id || deleted || restoredStatus != "active" || revision != 2 || string(opened) != "new-password" {
			t.Fatalf("restore did not preserve identity and refresh credentials: id=%s deleted=%v status=%s revision=%d err=%v", restoredID, deleted, restoredStatus, revision, err)
		}
		clear(opened)
		var staleAccess int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_mother_discoveries WHERE mother_account_id=$1`, id).Scan(&staleAccess); err != nil || staleAccess != 0 {
			t.Fatalf("restoration reused stale access: %d %v", staleAccess, err)
		}
	}
}

func TestMotherDeletionPaginationKeepsAccurateTotalOnEmptyPage(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	var content strings.Builder
	for index := 0; index < 21; index++ {
		fmt.Fprintf(&content, "page%02d@mother-delete.test----password----JBSWY3DPEHPK3PXP\n", index)
	}
	imported := standbyDeletionRequest(t, h, session, csrf, "POST", "/api/owner/v1/mother-accounts/import", map[string]any{"content": content.String()})
	decodeStandby[ownerapi.MotherAccountImportResult](t, imported, 200)
	session = childRotatedSession(t, imported)
	path := "/api/owner/v1/mother-accounts?search=mother-delete.test&page=2&page_size=20"
	list := decodeStandby[ownerapi.MotherAccountList](t, standbyDeletionRequest(t, h, session, csrf, http.MethodGet, path, nil), 200)
	if list.Total != 21 || len(list.Items) != 1 {
		t.Fatalf("page before deletion: %+v", list)
	}
	last := list.Items[0]
	if r := standbyDeletionRequest(t, h, session, csrf, http.MethodDelete, "/api/owner/v1/mother-accounts/"+last.Id.String(), map[string]any{"confirmed": true}); r.Code != 204 {
		t.Fatalf("delete last page row=%d %s", r.Code, r.Body.String())
	}
	list = decodeStandby[ownerapi.MotherAccountList](t, standbyDeletionRequest(t, h, session, csrf, http.MethodGet, path, nil), 200)
	if list.Total != 20 || len(list.Items) != 0 || list.Page != 2 {
		t.Fatalf("empty page lost the actual total: %+v", list)
	}
	var retained int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_mother_account_credentials WHERE login_identifier LIKE '%@mother-delete.test'`).Scan(&retained); err != nil || retained != 21 {
		t.Fatalf("deleted credentials were not retained: %d %v", retained, err)
	}
}

func TestMotherDeletionDoesNotWaitForRunningLoginOrAllowItsLatePublication(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	imported := standbyDeletionRequest(t, h, session, csrf, "POST", "/api/owner/v1/mother-accounts/import", map[string]any{"content": "running@mother-delete.test----password----JBSWY3DPEHPK3PXP"})
	decodeStandby[ownerapi.MotherAccountImportResult](t, imported, 200)
	session = childRotatedSession(t, imported)
	var mother uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT mother_account_id FROM tsw_mother_account_credentials WHERE login_identifier='running@mother-delete.test'`).Scan(&mother); err != nil {
		t.Fatal(err)
	}
	login := &sequencedPersonalRefresh{entered: make(chan struct{}), release: make(chan struct{}), session: platform.PersonalSession{AccessToken: "fixture-at", DeviceID: "fixture-device", ExpiresAt: time.Now().Add(time.Hour), Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "fixture-cookie"}}}}
	h.personalRefresh = login
	defer func() {
		select {
		case <-login.release:
		default:
			close(login.release)
		}
	}()
	endpoint := "/api/owner/v1/mother-accounts/" + mother.String()
	finished := make(chan int, 1)
	go func() {
		finished <- standbyDeletionRequest(t, h, session, csrf, "POST", endpoint+"/personal-session", nil).Code
	}()
	select {
	case <-login.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("login did not reach the platform")
	}
	// An unrelated record update after the frontend opens confirmation is no gate.
	if _, err := pool.Exec(ctx, `UPDATE tsw_mother_accounts SET version=version+1 WHERE id=$1`, mother); err != nil {
		t.Fatal(err)
	}
	if r := standbyDeletionRequest(t, h, session, csrf, "DELETE", endpoint, map[string]any{"confirmed": true}); r.Code != 204 {
		t.Fatalf("running login blocked deletion=%d %s", r.Code, r.Body.String())
	}
	close(login.release)
	select {
	case status := <-finished:
		if status != 409 {
			t.Fatalf("deleted mother published a late login: HTTP %d", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deleted mother login did not finish")
	}
	var sessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_mother_personal_sessions WHERE mother_account_id=$1`, mother).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("late login restored credentials after deletion: %d %v", sessions, err)
	}
}
