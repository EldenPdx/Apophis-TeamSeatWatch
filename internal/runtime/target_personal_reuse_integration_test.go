//go:build integration

package runtime

import (
	"context"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func targetReuseFixture(t *testing.T) (*pgxpool.Pool, *OwnerAuthHandler, uuid.UUID, func() *httptest.ResponseRecorder) {
	t.Helper()
	pool, h, _, token, csrf := childReviewFixture(t)
	ctx := context.Background()
	if err := sealExistingTargetMaterials(ctx, pool, cardIntegrationKeyRing{}); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_target_accounts WHERE identifier='target@example.com'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/owner/v1/target-accounts/"+id.String()+"/personal-session", nil)
		req.Header.Set("Origin", "https://owner.test")
		req.Header.Set(auth.CSRFHeaderName, csrf)
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		rec := httptest.NewRecorder()
		h.RefreshTargetPersonalAccess(rec, req, id, ownerapi.RefreshTargetPersonalAccessParams{})
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == auth.SessionCookieName {
				token = cookie.Value
			}
		}
		return rec
	}
	return pool, h, id, call
}

func TestTargetPersonalSavedGenerationIsReused(t *testing.T) {
	pool, h, id, call := targetReuseFixture(t)
	ctx := context.Background()
	fixture := &targetRefreshFixture{session: platform.PersonalSession{AccessToken: "saved-at", DeviceID: "device", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie"}}, ExpiresAt: time.Now().Add(7 * 24 * time.Hour)}}
	h.personalRefresh = fixture
	if rec := call(); rec.Code != 200 {
		t.Fatalf("first=%d %s", rec.Code, rec.Body.String())
	}
	var generation uuid.UUID
	var attempt int64
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT generation,attempt,sealed_session FROM tsw_target_personal_sessions WHERE target_account_id=$1`, id).Scan(&generation, &attempt, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if rec := call(); rec.Code != 200 {
		t.Fatalf("reuse=%d %s", rec.Code, rec.Body.String())
	}
	var same bool
	if err := pool.QueryRow(ctx, `SELECT generation=$2 AND attempt=$3 AND sealed_session=$4 FROM tsw_target_personal_sessions WHERE target_account_id=$1`, id, generation, attempt, ciphertext).Scan(&same); err != nil || !same || fixture.calls != 1 {
		t.Fatalf("repeat login: same=%v calls=%d err=%v", same, fixture.calls, err)
	}
}

type targetRenewFixture struct {
	targetRefreshFixture
	renewal    platform.PersonalRefreshResult
	renewCalls int
}

func (f *targetRenewFixture) RenewPersonal(context.Context, platform.PersonalSession, browserenv.Environment) (platform.PersonalRefreshResult, error) {
	f.renewCalls++
	return f.renewal, nil
}

func TestTargetPersonalFailedRenewalRetainsCiphertextAndThenReplaces(t *testing.T) {
	pool, h, id, call := targetReuseFixture(t)
	ctx := context.Background()
	fixture := &targetRenewFixture{targetRefreshFixture: targetRefreshFixture{session: platform.PersonalSession{AccessToken: "old-at", DeviceID: "device", ExpiresAt: time.Now().Add(time.Hour), SessionExpiresAt: time.Now().Add(30 * 24 * time.Hour), Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "old-cookie"}}}}, renewal: platform.PersonalRefreshResult{Status: "refresh_failed"}}
	h.personalRefresh = fixture
	if rec := call(); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	old := fixture.session
	old.ExpiresAt = time.Now().Add(-time.Minute)
	version, nonce, ciphertext, err := sealSessionFor("target", h.keyRing, id, 1, old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_personal_sessions SET key_version=$2,nonce=$3,sealed_session=$4,expires_at=$5 WHERE target_account_id=$1`, id, version, nonce, ciphertext, old.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if rec := call(); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var same bool
	if err = pool.QueryRow(ctx, `SELECT sealed_session=$2 FROM tsw_target_personal_sessions WHERE target_account_id=$1`, id, ciphertext).Scan(&same); err != nil || !same || fixture.calls != 1 || fixture.renewCalls != 1 {
		t.Fatalf("failed renewal deleted old state: same=%v login=%d renewal=%d err=%v", same, fixture.calls, fixture.renewCalls, err)
	}
	renewed := fixture.session
	renewed.AccessToken = "renewed-at"
	renewed.ExpiresAt = time.Now().Add(7 * 24 * time.Hour)
	fixture.renewal = platform.PersonalRefreshResult{Status: "ready", Session: renewed}
	if rec := call(); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if err = pool.QueryRow(ctx, `SELECT session.sealed_session<>$2 AND session.attempt=access.attempt AND access.status='ready' FROM tsw_target_personal_sessions session JOIN tsw_target_personal_access access USING(target_account_id) WHERE session.target_account_id=$1`, id, ciphertext).Scan(&same); err != nil || !same || fixture.calls != 1 || fixture.renewCalls != 2 {
		t.Fatalf("replacement not published: replaced=%v login=%d renewal=%d err=%v", same, fixture.calls, fixture.renewCalls, err)
	}
}

func TestTargetPersonalLiveRefreshDeduplicatesSecondClick(t *testing.T) {
	_, h, _, call := targetReuseFixture(t)
	fixture := &targetRefreshFixture{session: platform.PersonalSession{AccessToken: "saved-at", DeviceID: "device", ExpiresAt: time.Now().Add(time.Hour), Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie"}}}, entered: make(chan struct{}), release: make(chan struct{})}
	h.personalRefresh = fixture
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- call() }()
	<-fixture.entered
	second := call()
	if second.Code != 200 || !strings.Contains(second.Body.String(), "verifying") || fixture.calls != 1 {
		close(fixture.release)
		t.Fatalf("duplicate click: code=%d calls=%d", second.Code, fixture.calls)
	}
	close(fixture.release)
	if rec := <-first; rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
}
