//go:build integration

package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/mothersecret"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

type fixtureDiscovery struct {
	result platform.DiscoveryResult
	calls  int
}

func (f *fixtureDiscovery) Discover(_ context.Context, session platform.PersonalSession) (platform.DiscoveryResult, error) {
	if session.AccessToken != "personal-token" {
		panic("discovery did not receive saved Personal session")
	}
	f.calls++
	return f.result, nil
}

type fixturePersonalRefresh struct {
	result platform.PersonalRefreshResult
	calls  int
}

type failedPersonalRefresh struct{ err error }

func (f failedPersonalRefresh) RefreshPersonal(context.Context, platform.MotherMaterial) (platform.PersonalRefreshResult, error) {
	return platform.PersonalRefreshResult{}, f.err
}

func (f *fixturePersonalRefresh) RefreshPersonal(_ context.Context, material platform.MotherMaterial) (platform.PersonalRefreshResult, error) {
	if material.Password != "password" || material.TOTPSecret == "" {
		panic("refresh did not receive saved mother material")
	}
	f.calls++
	return f.result, nil
}

type sequencedPersonalRefresh struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
	session platform.PersonalSession
}

func (f *sequencedPersonalRefresh) RefreshPersonal(ctx context.Context, _ platform.MotherMaterial) (platform.PersonalRefreshResult, error) {
	if f.calls.Add(1) == 1 {
		close(f.entered)
		select {
		case <-f.release:
		case <-ctx.Done():
			return platform.PersonalRefreshResult{}, ctx.Err()
		}
	}
	return platform.PersonalRefreshResult{Status: "ready", Session: f.session}, nil
}

type sequencedDiscovery struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (f *sequencedDiscovery) Discover(ctx context.Context, _ platform.PersonalSession) (platform.DiscoveryResult, error) {
	if f.calls.Add(1) == 1 {
		close(f.entered)
		select {
		case <-f.release:
		case <-ctx.Done():
			return platform.DiscoveryResult{}, ctx.Err()
		}
	}
	return platform.DiscoveryResult{Status: "discovered", Workspaces: []platform.DiscoveredWorkspace{{PlatformID: "team-one", Name: "One", Access: "readable", Role: "owner"}}}, nil
}

func rotateMotherFixture(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, password string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var priorPassword, priorTOTP []byte
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT password_secret,totp_secret,secret_revision FROM tsw_mother_account_credentials WHERE mother_account_id=$1 FOR UPDATE`, id).Scan(&priorPassword, &priorTOTP, &revision); err != nil {
		return err
	}
	plain, err := mothersecret.Open(cardIntegrationKeyRing{}, id, revision, mothersecret.Password, priorPassword)
	if err != nil {
		return err
	}
	clear(plain)
	var totp []byte
	if priorTOTP != nil {
		totp, err = mothersecret.Open(cardIntegrationKeyRing{}, id, revision, mothersecret.TOTP, priorTOTP)
		if err != nil {
			return err
		}
	}
	sealedPassword, err := mothersecret.Seal(cardIntegrationKeyRing{}, id, revision+1, mothersecret.Password, []byte(password))
	if err != nil {
		return err
	}
	var sealedTOTP []byte
	if totp != nil {
		sealedTOTP, err = mothersecret.Seal(cardIntegrationKeyRing{}, id, revision+1, mothersecret.TOTP, totp)
		clear(totp)
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE tsw_mother_account_credentials SET password_secret=$2,totp_secret=$3,secret_revision=secret_revision+1,version=version+1 WHERE mother_account_id=$1`, id, sealedPassword, sealedTOTP); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func TestMotherDiscoveryPersistsPerMotherVisibilityWithoutBindingOrImplicitSelection(t *testing.T) {
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TSW_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ownerID, firstID, secondID, missingID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	session, sessionHash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	csrf := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x77}, 32))
	for _, query := range []struct {
		text string
		args []any
	}{
		{`INSERT INTO tsw_owners(id,username,password_hash) VALUES ($1,'owner','hash')`, []any{ownerID}},
		{`INSERT INTO tsw_owner_sessions(owner_id,token_hash,auth_version,idle_expires_at,absolute_expires_at) VALUES ($1,$2,1,now()+interval '1 hour',now()+interval '2 hours')`, []any{ownerID, sessionHash[:]}},
		{`INSERT INTO tsw_mother_accounts(id,display_name) VALUES ($1,'Mother A'),($2,'Mother B'),($3,'Missing')`, []any{firstID, secondID, missingID}},
	} {
		if _, err := pool.Exec(ctx, query.text, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []uuid.UUID{firstID, secondID, missingID} {
		secret := "JBSWY3DPEHPK3PXP"
		if i == 2 {
			secret = ""
		}
		_, err := pool.Exec(ctx, `INSERT INTO tsw_mother_account_credentials(mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret,totp_secret) VALUES ($1,$2,$3,1,'password',NULLIF($4,'')::bytea)`, id, []string{"a@example.test", "b@example.test", "missing@example.test"}[i], bytes.Repeat([]byte{byte(i + 1)}, 32), secret)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := mothersecret.Migrate(ctx, pool, cardIntegrationKeyRing{}); err != nil {
		t.Fatal(err)
	}
	origins, _ := auth.ParseOriginPolicy("https://owner.test")
	refresh := &fixturePersonalRefresh{result: platform.PersonalRefreshResult{Status: "ready", Session: platform.PersonalSession{AccessToken: "personal-token", DeviceID: "device-one", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie-secret"}}, ExpiresAt: time.Now().Add(time.Hour)}}}
	adapter := &fixtureDiscovery{result: platform.DiscoveryResult{Status: "discovered", Workspaces: []platform.DiscoveredWorkspace{{PlatformID: "team-one", Name: "One", Access: "readable", Role: "owner"}, {PlatformID: "team-two", Name: "Two", Access: "permission_denied", Role: "member"}}}}
	handler := &OwnerAuthHandler{pool: pool, keyRing: cardIntegrationKeyRing{}, origins: origins, personalRefresh: refresh, discovery: adapter}
	serve := ownerapi.HandlerWithOptions(handler, ownerapi.StdHTTPServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
		if isCSRFBindingError(err) {
			writeProblem(w, r, 403, "csrf_rejected", "Forbidden", "Origin or CSRF validation failed", 0)
			return
		}
		writeProblem(w, r, 400, "invalid_request", "Invalid Request", "Invalid parameters", 0)
	}})
	call := func(method, path string, authorize, csrfHeader bool) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/owner/v1/mother-accounts/"+path, nil)
		req.Header.Set("Origin", "https://owner.test")
		if authorize {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		}
		if csrfHeader {
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			req.Header.Set(auth.CSRFHeaderName, csrf)
		}
		rec := httptest.NewRecorder()
		serve.ServeHTTP(rec, req)
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == auth.SessionCookieName {
				session = cookie.Value
			}
		}
		return rec.Code, rec.Body.Bytes()
	}
	readDiscovery := func(method string, id uuid.UUID, authorize, csrfHeader bool) (int, ownerapi.MotherDiscovery) {
		status, body := call(method, id.String()+"/discovery", authorize, csrfHeader)
		var result ownerapi.MotherDiscovery
		if status == 200 && json.Unmarshal(body, &result) != nil {
			t.Fatalf("invalid discovery: %s", body)
		}
		return status, result
	}
	readAccess := func(method string, id uuid.UUID, authorize, csrfHeader bool) (int, ownerapi.MotherPersonalAccess) {
		status, body := call(method, id.String()+"/personal-session", authorize, csrfHeader)
		var result ownerapi.MotherPersonalAccess
		if status == 200 && json.Unmarshal(body, &result) != nil {
			t.Fatalf("invalid access: %s", body)
		}
		return status, result
	}
	listRequest := httptest.NewRequest(http.MethodGet, "/api/owner/v1/mother-accounts?page=1&page_size=100", nil)
	listRequest.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	listResponse := httptest.NewRecorder()
	serve.ServeHTTP(listResponse, listRequest)
	var list ownerapi.MotherAccountList
	if listResponse.Code != 200 || json.Unmarshal(listResponse.Body.Bytes(), &list) != nil || list.Total != 3 {
		t.Fatalf("saved mother list: %d %s", listResponse.Code, listResponse.Body.String())
	}
	if status, _ := readAccess(http.MethodPost, firstID, false, true); status != 401 {
		t.Fatalf("unauthenticated refresh=%d", status)
	}
	if status, _ := readAccess(http.MethodPost, firstID, true, false); status != 403 {
		t.Fatalf("missing CSRF refresh=%d", status)
	}
	if status, _ := readDiscovery(http.MethodPost, firstID, true, false); status != 403 {
		t.Fatalf("missing CSRF discovery=%d", status)
	}
	if refresh.calls != 0 || adapter.calls != 0 {
		t.Fatal("adapter called without authorization")
	}
	if status, result := readDiscovery(http.MethodGet, firstID, true, false); status != 200 || result.Status != "not_verified" {
		t.Fatalf("materials triggered discovery: %d %+v", status, result)
	}
	if status, result := readDiscovery(http.MethodPost, firstID, true, true); status != 200 || result.Status != "missing_credentials" || adapter.calls != 0 {
		t.Fatalf("discovered without Personal session: %d %+v", status, result)
	}
	if status, result := readAccess(http.MethodPost, missingID, true, true); status != 200 || result.Status != "missing_credentials" || refresh.calls != 0 {
		t.Fatalf("missing material refresh: %d %+v", status, result)
	}
	t.Run("browser disconnect does not cancel a reserved mother login or revoke Owner", func(t *testing.T) {
		login := &sequencedPersonalRefresh{entered: make(chan struct{}), release: make(chan struct{}), session: refresh.result.Session}
		handler.personalRefresh = login
		defer func() { handler.personalRefresh = refresh }()
		requestContext, disconnect := context.WithCancel(ctx)
		defer disconnect()
		req := httptest.NewRequest(http.MethodPost, "/api/owner/v1/mother-accounts/"+firstID.String()+"/personal-session", nil).WithContext(requestContext)
		req.Header.Set("Origin", "https://owner.test")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
		req.Header.Set(auth.CSRFHeaderName, csrf)
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { serve.ServeHTTP(rec, req); close(done) }()
		<-login.entered
		disconnect()
		close(login.release)
		<-done
		if rec.Code != http.StatusOK {
			t.Fatalf("browser disconnect prevented login publication: HTTP %d", rec.Code)
		}
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == auth.SessionCookieName {
				t.Fatal("ordinary mother login rotated Owner cookie; a disconnected browser cannot receive it")
			}
		}
		if status, result := readAccess(http.MethodGet, firstID, true, false); status != 200 || result.Status != "ready" {
			t.Fatalf("reload did not recover final login state with original Owner: %d %+v", status, result)
		}
	})
	if t.Failed() {
		return
	}
	t.Run("login timeout publishes a final failure instead of verifying", func(t *testing.T) {
		handler.personalRefresh = failedPersonalRefresh{err: context.DeadlineExceeded}
		defer func() { handler.personalRefresh = refresh }()
		if status, result := readAccess(http.MethodPost, firstID, true, true); status != 200 || result.Status != "refresh_failed" || result.Failure == nil || result.Failure.Code != "login_task_timeout" {
			t.Fatalf("deadline did not publish an actionable final failure: %d %+v", status, result)
		}
	})
	t.Run("logout during login blocks publication and clears the same attempt", func(t *testing.T) {
		login := &sequencedPersonalRefresh{entered: make(chan struct{}), release: make(chan struct{}), session: refresh.result.Session}
		handler.personalRefresh = login
		defer func() { handler.personalRefresh = refresh }()
		isolatedToken, isolatedHash, err := auth.NewSessionToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO tsw_owner_sessions(owner_id,token_hash,auth_version,idle_expires_at,absolute_expires_at) VALUES($1,$2,1,now()+interval '1 hour',now()+interval '2 hours')`, ownerID, isolatedHash[:]); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/owner/v1/mother-accounts/"+firstID.String()+"/personal-session", nil)
		req.Header.Set("Origin", "https://owner.test")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: isolatedToken})
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
		req.Header.Set(auth.CSRFHeaderName, csrf)
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { serve.ServeHTTP(rec, req); close(done) }()
		<-login.entered
		_, revokeErr := pool.Exec(ctx, `UPDATE tsw_owner_sessions SET revoked_at=now(),revocation_reason='logout' WHERE token_hash=$1`, isolatedHash[:])
		close(login.release)
		<-done
		if revokeErr != nil || rec.Code != http.StatusUnauthorized {
			t.Fatalf("revoked Owner published credentials: HTTP %d error=%v", rec.Code, revokeErr)
		}
		if status, result := readAccess(http.MethodGet, firstID, true, false); status != 200 || result.Status != "refresh_failed" || result.Failure == nil || result.Failure.Code != "login_result_not_saved" {
			t.Fatalf("authorization failure left a pending login: %d %+v", status, result)
		}
		var retained bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tsw_mother_personal_sessions WHERE mother_account_id=$1)`, firstID).Scan(&retained); err != nil || retained {
			t.Fatal("logout result retained credentials")
		}
	})
	t.Run("stale task has an explicit interruption cause", func(t *testing.T) {
		if _, err = pool.Exec(ctx, `UPDATE tsw_mother_personal_access SET status='verifying',failure=NULL,checked_at=now()-interval '3 minutes' WHERE mother_account_id=$1`, firstID); err != nil {
			t.Fatal(err)
		}
		if status, result := readAccess(http.MethodGet, firstID, true, false); status != 200 || result.Status != "refresh_failed" || result.Failure == nil || result.Failure.Code != "login_task_interrupted" {
			t.Fatalf("stale login lost the interruption cause: %d %+v", status, result)
		}
	})
	for _, id := range []uuid.UUID{firstID, secondID} {
		if status, result := readAccess(http.MethodPost, id, true, true); status != 200 || result.Status != "ready" {
			t.Fatalf("Personal refresh: %d %+v", status, result)
		}
	}
	var encrypted []byte
	if err := pool.QueryRow(ctx, `SELECT sealed_session FROM tsw_mother_personal_sessions WHERE mother_account_id=$1`, firstID).Scan(&encrypted); err != nil || bytes.Contains(encrypted, []byte("personal-token")) || bytes.Contains(encrypted, []byte("cookie-secret")) {
		t.Fatalf("personal session not encrypted: %v", err)
	}
	if status, result := readDiscovery(http.MethodPost, firstID, true, true); status != 200 || result.Status != "discovered" || len(result.Workspaces) != 2 {
		t.Fatalf("first discovery: %d %+v", status, result)
	}
	adapter.result.Workspaces = []platform.DiscoveredWorkspace{{PlatformID: "team-one", Name: "Renamed One", Access: "unknown", Role: "unknown"}}
	if status, result := readDiscovery(http.MethodPost, secondID, true, true); status != 200 || result.Status != "discovered" || len(result.Workspaces) != 1 {
		t.Fatalf("second discovery: %d %+v", status, result)
	}
	_, one := readDiscovery(http.MethodGet, firstID, true, false)
	_, two := readDiscovery(http.MethodGet, secondID, true, false)
	if one.Workspaces[0].Id != two.Workspaces[0].Id || one.Workspaces[0].AccessStatus != "readable" || two.Workspaces[0].AccessStatus != "unknown" {
		t.Fatalf("canonical identity/per-mother access: %+v %+v", one, two)
	}
	var spaces, relations, bindings int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM tsw_workspaces),(SELECT count(*) FROM tsw_mother_workspace_visibility),(SELECT count(*) FROM tsw_mother_workspace_bindings)`).Scan(&spaces, &relations, &bindings); err != nil || spaces != 2 || relations != 3 || bindings != 0 {
		t.Fatalf("spaces=%d relations=%d bindings=%d err=%v", spaces, relations, bindings, err)
	}
	var ownerRoles int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_mother_workspace_visibility visibility JOIN tsw_workspaces workspace ON workspace.id=visibility.workspace_id WHERE workspace.platform_workspace_id='team-one' AND visibility.workspace_role='owner'`).Scan(&ownerRoles); err != nil || ownerRoles != 1 {
		t.Fatalf("first-party Workspace owner role not persisted per mother: count=%d err=%v", ownerRoles, err)
	}
	// Fix a read-only snapshot, switch the current attempt, and verify status
	// and rows still come from the same generation; a new GET sees the new one.
	snapshot, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	var observedStatus string
	if err := snapshot.QueryRow(ctx, `SELECT status FROM tsw_mother_discoveries WHERE mother_account_id=$1`, firstID).Scan(&observedStatus); err != nil || observedStatus != "discovered" {
		t.Fatalf("snapshot start: %s %v", observedStatus, err)
	}
	if status, state := readDiscovery(http.MethodPost, firstID, true, true); status != 200 || state.Status != "discovered" || len(state.Workspaces) != 1 {
		t.Fatalf("switch attempt: %d %+v", status, state)
	}
	oldSnapshot, err := motherDiscoverySnapshot(ctx, snapshot, firstID)
	if err != nil || oldSnapshot.Status != "discovered" || len(oldSnapshot.Workspaces) != 2 {
		t.Fatalf("mixed status/visibility snapshot: %+v %v", oldSnapshot, err)
	}
	if err := snapshot.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if status, state := readDiscovery(http.MethodGet, firstID, true, false); status != 200 || state.Status != "discovered" || len(state.Workspaces) != 1 {
		t.Fatalf("new snapshot did not see switched attempt: %d %+v", status, state)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_mother_personal_sessions SET expires_at=now()-interval '1 second' WHERE mother_account_id=$1`, firstID); err != nil {
		t.Fatal(err)
	}
	if status, result := readDiscovery(http.MethodGet, firstID, true, false); status != 200 || result.Status != "not_verified" || len(result.Workspaces) != 0 {
		t.Fatalf("expired Personal session exposed old visibility: %d %+v", status, result)
	}
	if status, result := readDiscovery(http.MethodPost, firstID, true, true); status != 200 || result.Status != "session_expired" || len(result.Workspaces) != 0 {
		t.Fatalf("expired before discovery misclassified: %d %+v", status, result)
	}
	var expiredCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_mother_personal_sessions WHERE mother_account_id=$1`, firstID).Scan(&expiredCount); err != nil || expiredCount != 0 {
		t.Fatalf("expired generation retained: %d %v", expiredCount, err)
	}
	if status, state := readAccess(http.MethodPost, firstID, true, true); status != 200 || state.Status != "ready" {
		t.Fatalf("restore Personal after expiry: %d %+v", status, state)
	}
	adapter.result = platform.DiscoveryResult{Status: "session_expired"}
	if status, result := readDiscovery(http.MethodPost, firstID, true, true); status != 200 || result.Status != "session_expired" || len(result.Workspaces) != 0 {
		t.Fatalf("expired session: %d %+v", status, result)
	}
	if status, result := readAccess(http.MethodGet, firstID, true, false); status != 200 || result.Status != "not_verified" {
		t.Fatalf("revoked session still ready: %d %+v", status, result)
	}
	adapter.result = platform.DiscoveryResult{Status: "discovered"}
	if status, result := readDiscovery(http.MethodPost, secondID, true, true); status != 200 || result.Status != "empty" {
		t.Fatalf("empty: %d %+v", status, result)
	}
	adapter.result = platform.DiscoveryResult{Status: "permission_denied"}
	if status, result := readDiscovery(http.MethodPost, secondID, true, true); status != 200 || result.Status != "permission_denied" {
		t.Fatalf("permission: %d %+v", status, result)
	}
	adapter.result = platform.DiscoveryResult{Status: "discovery_failed"}
	if status, result := readDiscovery(http.MethodPost, secondID, true, true); status != 200 || result.Status != "discovery_failed" {
		t.Fatalf("failure: %d %+v", status, result)
	}
	if adapter.calls != 7 {
		t.Fatalf("unexpected discovery calls=%d", adapter.calls)
	}
	refresh.result = platform.PersonalRefreshResult{Status: "invalid_login"}
	if status, result := readAccess(http.MethodPost, firstID, true, true); status != 200 || result.Status != "invalid_login" {
		t.Fatalf("invalid login refresh: %d %+v", status, result)
	}
	if status, result := readDiscovery(http.MethodGet, firstID, true, false); status != 200 || result.Status != "not_verified" {
		t.Fatalf("failed refresh did not invalidate discovery: %d %+v", status, result)
	}
	refresh.result = platform.PersonalRefreshResult{Status: "refresh_failed"}
	if status, result := readAccess(http.MethodPost, firstID, true, true); status != 200 || result.Status != "refresh_failed" {
		t.Fatalf("refresh transport failure: %d %+v", status, result)
	}
	handler.personalRefresh = nil
	if status, result := readAccess(http.MethodPost, firstID, true, true); status != 200 || result.Status != "unavailable" {
		t.Fatalf("production refresh must fail closed: %d %+v", status, result)
	}
	handler.discovery = nil
	if status, result := readDiscovery(http.MethodPost, secondID, true, true); status != 200 || result.Status != "unavailable" {
		t.Fatalf("production default: %d %+v", status, result)
	}
	if err = rotateMotherFixture(ctx, pool, secondID, "new-password"); err != nil {
		t.Fatal(err)
	}
	if status, result := readAccess(http.MethodGet, secondID, true, false); status != 200 || result.Status != "not_verified" {
		t.Fatalf("revision must invalidate Personal session: %d %+v", status, result)
	}
	if status, result := readDiscovery(http.MethodGet, secondID, true, false); status != 200 || result.Status != "not_verified" {
		t.Fatalf("revision must invalidate discovery: %d %+v", status, result)
	}

	// A slow remote attempt must not hold an account lock: editing the source
	// credentials fences its result before anything is republished.
	handler.personalRefresh = refresh
	refresh.result = platform.PersonalRefreshResult{Status: "ready", Session: platform.PersonalSession{AccessToken: "personal-token", DeviceID: "device-one", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie-secret"}}, ExpiresAt: time.Now().Add(time.Hour)}}
	if status, _ := readAccess(http.MethodPost, firstID, true, true); status != 200 {
		t.Fatalf("restore Personal session: %d", status)
	}
	startAsync := func() <-chan int {
		out := make(chan int, 1)
		req := httptest.NewRequest(http.MethodPost, "/api/owner/v1/mother-accounts/"+firstID.String()+"/discovery", nil)
		req.Header.Set("Origin", "https://owner.test")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
		req.Header.Set(auth.CSRFHeaderName, csrf)
		go func() { rec := httptest.NewRecorder(); serve.ServeHTTP(rec, req); out <- rec.Code }()
		return out
	}
	first := &sequencedDiscovery{entered: make(chan struct{}), release: make(chan struct{})}
	handler.discovery = first
	result := startAsync()
	<-first.entered
	updateCtx, cancel := context.WithTimeout(ctx, time.Second)
	err = rotateMotherFixture(updateCtx, pool, firstID, "rotated")
	cancel()
	if err != nil {
		close(first.release)
		t.Fatalf("remote discovery held a database row lock: %v", err)
	}
	close(first.release)
	if status := <-result; status != 409 {
		t.Fatalf("stale credential generation published: %d", status)
	}
	if status, state := readDiscovery(http.MethodGet, firstID, true, false); status != 200 || state.Status != "not_verified" || len(state.Workspaces) != 0 {
		t.Fatalf("stale visibility: %d %+v", status, state)
	}
	if err = rotateMotherFixture(ctx, pool, firstID, "password"); err != nil {
		t.Fatal(err)
	}
	if status, _ := readAccess(http.MethodPost, firstID, true, true); status != 200 {
		t.Fatalf("restore revised Personal session: %d", status)
	}
	second := &sequencedDiscovery{entered: make(chan struct{}), release: make(chan struct{})}
	handler.discovery = second
	older := startAsync()
	<-second.entered
	if status, state := readDiscovery(http.MethodPost, firstID, true, true); status != 200 || state.Status != "discovered" || len(state.Workspaces) != 1 {
		close(second.release)
		t.Fatalf("newer discovery blocked: %d %+v", status, state)
	}
	close(second.release)
	if status := <-older; status != 409 {
		t.Fatalf("older attempt overwrote newer: %d", status)
	}
	if state, status := readDiscovery(http.MethodGet, firstID, true, false); state != 200 || status.Status != "discovered" || len(status.Workspaces) != 1 {
		t.Fatalf("latest discovery lost: %d %+v", state, status)
	}
	// Expiry after reservation but before a successful remote response must
	// settle as session_expired, clear visibility and revoke this generation.
	third := &sequencedDiscovery{entered: make(chan struct{}), release: make(chan struct{})}
	handler.discovery = third
	duringExpiry := startAsync()
	<-third.entered
	if _, err := pool.Exec(ctx, `UPDATE tsw_mother_personal_sessions SET expires_at=now()-interval '1 second' WHERE mother_account_id=$1`, firstID); err != nil {
		close(third.release)
		t.Fatal(err)
	}
	close(third.release)
	if status := <-duringExpiry; status != 200 {
		t.Fatalf("expiry during discovery left attempt pending: %d", status)
	}
	if status, state := readDiscovery(http.MethodGet, firstID, true, false); status != 200 || state.Status != "session_expired" || len(state.Workspaces) != 0 {
		t.Fatalf("expiry during discovery misclassified: %d %+v", status, state)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_mother_personal_sessions WHERE mother_account_id=$1`, firstID).Scan(&expiredCount); err != nil || expiredCount != 0 {
		t.Fatalf("in-flight expired generation retained: %d %v", expiredCount, err)
	}
	if status, state := readAccess(http.MethodPost, firstID, true, true); status != 200 || state.Status != "ready" {
		t.Fatalf("restore Personal after in-flight expiry: %d %+v", status, state)
	}
	// A newer explicit refresh fences a slower prior login without retaining
	// database locks across either platform attempt.
	logins := &sequencedPersonalRefresh{entered: make(chan struct{}), release: make(chan struct{}), session: refresh.result.Session}
	handler.personalRefresh = logins
	startRefresh := func() <-chan int {
		out := make(chan int, 1)
		req := httptest.NewRequest(http.MethodPost, "/api/owner/v1/mother-accounts/"+firstID.String()+"/personal-session", nil)
		req.Header.Set("Origin", "https://owner.test")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
		req.Header.Set(auth.CSRFHeaderName, csrf)
		go func() { rec := httptest.NewRecorder(); serve.ServeHTTP(rec, req); out <- rec.Code }()
		return out
	}
	olderLogin := startRefresh()
	<-logins.entered
	if status, state := readAccess(http.MethodPost, firstID, true, true); status != 200 || state.Status != "ready" {
		close(logins.release)
		t.Fatalf("newer Personal refresh blocked: %d %+v", status, state)
	}
	close(logins.release)
	if status := <-olderLogin; status != 409 {
		t.Fatalf("older login replaced newer generation: %d", status)
	}
	if status, state := readDiscovery(http.MethodGet, firstID, true, false); status != 200 || state.Status != "not_verified" || len(state.Workspaces) != 0 {
		t.Fatalf("refresh left stale visibility: %d %+v", status, state)
	}
	if status, state := readDiscovery(http.MethodPost, firstID, true, true); status != 200 || state.Status != "discovered" {
		t.Fatalf("prepare visibility before credential rotation: %d %+v", status, state)
	}
	// PATCH must delete the old encrypted AT and every access/visibility row
	// in the same transaction as the source credential revision increase.
	patch := httptest.NewRequest(http.MethodPatch, "/api/owner/v1/mother-accounts/"+firstID.String(), bytes.NewBufferString(`{"displayName":"Mother A","status":"active","password":"replaced"}`))
	patch.Header.Set("Origin", "https://owner.test")
	patch.Header.Set("Content-Type", "application/json")
	patch.Header.Set("If-Match", `"1"`)
	patch.Header.Set(auth.CSRFHeaderName, csrf)
	patch.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	patch.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	patched := httptest.NewRecorder()
	serve.ServeHTTP(patched, patch)
	if patched.Code != 200 {
		t.Fatalf("credential rotation failed: %d %s", patched.Code, patched.Body.String())
	}
	for _, cookie := range patched.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			session = cookie.Value
		}
	}
	var oldSessions, oldVisibility, oldDiscovery, oldAccess int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM tsw_mother_personal_sessions WHERE mother_account_id=$1),(SELECT count(*) FROM tsw_mother_workspace_visibility WHERE mother_account_id=$1),(SELECT count(*) FROM tsw_mother_discoveries WHERE mother_account_id=$1),(SELECT count(*) FROM tsw_mother_personal_access WHERE mother_account_id=$1)`, firstID).Scan(&oldSessions, &oldVisibility, &oldDiscovery, &oldAccess); err != nil || oldSessions != 0 || oldVisibility != 0 || oldDiscovery != 0 || oldAccess != 0 {
		t.Fatalf("credential rotation retained Personal facts: sessions=%d visibility=%d discovery=%d access=%d err=%v", oldSessions, oldVisibility, oldDiscovery, oldAccess, err)
	}
	if status, state := readDiscovery(http.MethodGet, firstID, true, false); status != 200 || state.Status != "not_verified" || len(state.Workspaces) != 0 {
		t.Fatalf("rotated material still discoverable: %d %+v", status, state)
	}
}
