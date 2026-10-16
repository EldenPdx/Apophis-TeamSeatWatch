//go:build integration

package runtime

import (
	"context"
	"crypto/sha256"
	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type parallelPersonalFixture struct {
	entered chan struct{}
	release chan struct{}
}

func (f *parallelPersonalFixture) RefreshPersonal(ctx context.Context, _ platform.MotherMaterial) (platform.PersonalRefreshResult, error) {
	f.entered <- struct{}{}
	select {
	case <-f.release:
	case <-ctx.Done():
		return platform.PersonalRefreshResult{}, ctx.Err()
	}
	return platform.PersonalRefreshResult{Status: "ready", Session: platform.PersonalSession{AccessToken: "fixture-at", DeviceID: "fixture-device", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "fixture-cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}}, nil
}

func (f *parallelPersonalFixture) RenewPersonal(ctx context.Context, _ platform.PersonalSession, _ browserenv.Environment) (platform.PersonalRefreshResult, error) {
	return f.RefreshPersonal(ctx, platform.MotherMaterial{})
}

func TestProxyConcurrentAccountPublicationPreservesOwnerFencing(t *testing.T) {
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	u, err := url.Parse(dsn)
	if dsn == "" {
		t.Skip("isolated database required")
	}
	if err != nil || u.Hostname() != "127.0.0.1" || !strings.HasPrefix(u.Path, "/tsw_proxy_verify_") {
		t.Fatal("requires isolated proxy verification database")
	}
	pool, h, ownerID, token, csrf := childReviewFixture(t)
	ctx := context.Background()
	if err := sealExistingTargetMaterials(ctx, pool, cardIntegrationKeyRing{}); err != nil {
		t.Fatal(err)
	}
	var first uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_target_accounts WHERE identifier='target@example.com'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	second := uuid.New()
	digest := sha256.Sum256(second[:])
	if _, err := pool.Exec(ctx, `INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'parallel@example.test',$2,1,'parallel')`, second, digest[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) SELECT $1,password_secret,totp_secret,'complete',true FROM tsw_target_credentials WHERE target_account_id=$2`, second, first); err != nil {
		t.Fatal(err)
	}
	fixture := &parallelPersonalFixture{entered: make(chan struct{}, 2), release: make(chan struct{})}
	h.personalRefresh = fixture
	call := func(id uuid.UUID) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/owner/v1/target-accounts/"+id.String()+"/personal-session", nil)
		req.Header.Set("Origin", "https://owner.test")
		req.Header.Set(auth.CSRFHeaderName, csrf)
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		rec := httptest.NewRecorder()
		h.RefreshTargetPersonalAccess(rec, req, id, ownerapi.RefreshTargetPersonalAccessParams{})
		return rec
	}
	responses := make(chan *httptest.ResponseRecorder, 2)
	go func() { responses <- call(first) }()
	go func() { responses <- call(second) }()
	for i := 0; i < 2; i++ {
		select {
		case <-fixture.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("parallel request failed to reach platform")
		}
	}
	close(fixture.release)
	for i := 0; i < 2; i++ {
		rec := <-responses
		if rec.Code != 200 {
			t.Fatalf("concurrent publication=%d %s", rec.Code, rec.Body.String())
		}
	}
	var ready, live int
	pool.QueryRow(ctx, `SELECT count(*) FROM tsw_target_personal_access WHERE status='ready'`).Scan(&ready)
	pool.QueryRow(ctx, `SELECT count(*) FROM tsw_owner_sessions WHERE owner_id=$1 AND revoked_at IS NULL`, ownerID).Scan(&live)
	if ready != 2 || live != 1 {
		t.Fatalf("publication/owner state=%d/%d", ready, live)
	}
	// Logout between authentication and publication still fences the new credential generation.
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_personal_access SET status='refresh_failed' WHERE target_account_id=$1`, first); err != nil {
		t.Fatal(err)
	}
	fixture.release = make(chan struct{})
	go func() { responses <- call(first) }()
	select {
	case <-fixture.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("renewal did not reach platform")
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_owner_sessions SET revoked_at=now(),revocation_reason='owner_request' WHERE owner_id=$1 AND revoked_at IS NULL`, ownerID); err != nil {
		t.Fatal(err)
	}
	close(fixture.release)
	rec := <-responses
	if rec.Code != 401 {
		t.Fatalf("revoked session published=%d %s", rec.Code, rec.Body.String())
	}
	var status string
	pool.QueryRow(ctx, `SELECT status FROM tsw_target_personal_access WHERE target_account_id=$1`, first).Scan(&status)
	if status == "ready" {
		t.Fatal("revoked Owner published ready credentials")
	}
}
