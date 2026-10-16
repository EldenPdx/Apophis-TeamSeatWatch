//go:build integration

package runtime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/accountsession"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/mothersecret"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
)

type environmentPersonalFixture struct{ seen []browserenv.Environment }

func (f *environmentPersonalFixture) RefreshPersonal(_ context.Context, m platform.MotherMaterial) (platform.PersonalRefreshResult, error) {
	if err := m.Environment.Validate(); err != nil {
		return platform.PersonalRefreshResult{}, err
	}
	f.seen = append(f.seen, m.Environment)
	return platform.PersonalRefreshResult{Status: "ready", Session: platform.PersonalSession{AccessToken: "environment-at", DeviceID: m.Environment.DeviceID, Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "environment-cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}}, nil
}

func TestTargetEnvironmentQueueRetainsReservationBeyondOldTimeouts(t *testing.T) {
	pool, h, id, call := targetReuseFixture(t)
	router, _ := egress.NewManaged("http://platform.test", "http://exit.test", []byte("queue-fixture"), "1")
	defer router.CloseIdleConnections()
	leases, _ := egress.NewLeaseManager(router, egress.Admission{})
	manager := egress.NewManager(leases, router.Status(egress.Admission{}))
	manager.SetMode(egress.ModeDirect)
	manager.SetConcurrency(1)
	held, err := manager.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	queued := make(chan struct{})
	execution := make(chan time.Duration, 1)
	h.personalRefresh = platform.PersonalWebRefresher{Client: func(ctx context.Context) (*http.Client, func(), error) {
		close(queued)
		lease, err := manager.Acquire(ctx)
		if err != nil {
			return nil, nil, err
		}
		return lease.Client(), lease.Release, nil
	}, Browser: func(ctx context.Context, _ *http.Client, _ http.CookieJar, e browserenv.Environment) (http.RoundTripper, func(), error) {
		if err := e.Validate(); err != nil {
			return nil, nil, err
		}
		deadline, _ := ctx.Deadline()
		execution <- time.Until(deadline)
		return &targetChallengeTransport{}, func() {}, nil
	}}
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { finished <- call() }()
	<-queued
	// This crosses the old 30s admission/45s whole-operation deadlines and the
	// existing one-minute stale reservation threshold in one bounded fixture.
	select {
	case result := <-finished:
		t.Fatalf("queue expired early: %d %s", result.Code, result.Body.String())
	case <-time.After(61 * time.Second):
	}
	if result := call(); result.Code != 200 || !strings.Contains(result.Body.String(), "verifying") {
		t.Fatal("queued reservation became stale", result.Body.String())
	}
	var fresh bool
	if err := pool.QueryRow(t.Context(), `SELECT checked_at>now()-interval '20 seconds' FROM tsw_target_personal_access WHERE target_account_id=$1`, id).Scan(&fresh); err != nil || !fresh {
		t.Fatal("reservation heartbeat absent", err)
	}
	select {
	case <-execution:
		t.Fatal("queued account owned a browser")
	default:
	}
	held.Release()
	select {
	case remaining := <-execution:
		if remaining < 119*time.Second {
			t.Fatal("waiting consumed browser execution budget", remaining)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("released capacity did not admit queued account")
	}
	select {
	case result := <-finished:
		if result.Code != 200 {
			t.Fatal(result.Code, result.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admitted fixture did not finish")
	}
}

func TestOwnerAccountEnvironmentsAreLazyIndependentAndRetainedAcrossCredentialChanges(t *testing.T) {
	pool, h, _, token, csrf := childReviewFixture(t)
	ctx := t.Context()
	if err := sealExistingTargetMaterials(ctx, pool, cardIntegrationKeyRing{}); err != nil {
		t.Fatal(err)
	}
	var first uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_target_accounts WHERE identifier='target@example.com'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	ids := []uuid.UUID{first}
	for i := 1; i < 10; i++ {
		id := uuid.New()
		sum := sha256.Sum256(id[:])
		ids = append(ids, id)
		if _, err := pool.Exec(ctx, `INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,$2,$3,1,'environment fixture')`, id, fmt.Sprintf("environment-%d@example.test", i), sum[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) SELECT $1,password_secret,totp_secret,'complete',true FROM tsw_target_credentials WHERE target_account_id=$2`, id, first); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_account_browser_environments`).Scan(&count); err != nil || count != 0 {
		t.Fatal("import eagerly created environments", count, err)
	}
	f := &environmentPersonalFixture{}
	h.personalRefresh = f
	call := func(kind string, id uuid.UUID) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/owner/v1/"+kind+"-accounts/"+id.String()+"/personal-session", nil)
		r.Header.Set("Origin", "https://owner.test")
		r.Header.Set(auth.CSRFHeaderName, csrf)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
		result := httptest.NewRecorder()
		if kind == "target" {
			h.RefreshTargetPersonalAccess(result, r, id, ownerapi.RefreshTargetPersonalAccessParams{})
		} else {
			h.RefreshMotherPersonalAccess(result, r, id, ownerapi.RefreshMotherPersonalAccessParams{})
		}
		if result.Code != 200 {
			t.Fatalf("%s login=%d %s", kind, result.Code, result.Body.String())
		}
		return result
	}
	for _, id := range ids {
		call("target", id)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_account_browser_environments`).Scan(&count); err != nil || count != 10 {
		t.Fatal("ten fixture environments", count, err)
	}
	seen := map[uuid.UUID]bool{}
	seeds := map[uint64]bool{}
	devices := map[string]bool{}
	for _, e := range f.seen {
		if seen[e.ProfileID] || seeds[e.Seed] || devices[e.DeviceID] {
			t.Fatal("accounts shared an environment")
		}
		seen[e.ProfileID] = true
		seeds[e.Seed] = true
		devices[e.DeviceID] = true
	}
	original := f.seen[0]
	call("target", first)
	if len(f.seen) != 10 {
		t.Fatal("valid session was logged in again")
	}
	// A new Store instance models service restart; it resolves the same profile.
	e, err := (accountsession.Store{Pool: pool, KeyRing: h.keyRing}).Environment(ctx, first)
	if err != nil || !reflect.DeepEqual(original, e) {
		t.Fatal("restart changed environment", err)
	}
	rotated, err := targetdomain.SealMaterial("rotated-password", cardIntegrationKeyRing{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_credentials SET password_secret=$2,secret_revision=secret_revision+1,version=version+1 WHERE target_account_id=$1`, first, rotated); err != nil {
		t.Fatal(err)
	}
	call("target", first)
	if !reflect.DeepEqual(original, f.seen[len(f.seen)-1]) {
		t.Fatal("credential revision changed identity")
	}
	mother := uuid.New()
	pw, _ := mothersecret.Seal(h.keyRing, mother, 1, mothersecret.Password, []byte("password"))
	totp, _ := mothersecret.Seal(h.keyRing, mother, 1, mothersecret.TOTP, []byte("JBSWY3DPEHPK3PXP"))
	sum := sha256.Sum256(mother[:])
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_mother_accounts(id,display_name) VALUES($1,'environment mother fixture')`, mother); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_mother_account_credentials(mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret,totp_secret) VALUES($1,'mother-environment@example.test',$2,1,$3,$4)`, mother, sum[:], pw, totp); err != nil {
		t.Fatal(err)
	}
	call("mother", mother)
	motherEnv := f.seen[len(f.seen)-1]
	call("mother", mother)
	if !reflect.DeepEqual(motherEnv, f.seen[len(f.seen)-1]) || seen[motherEnv.ProfileID] || seeds[motherEnv.Seed] || devices[motherEnv.DeviceID] {
		t.Fatal("mother identity was not independently retained")
	}
}

type environmentLifetimeFixture struct {
	entered chan context.Context
	release chan struct{}
}

func (f *environmentLifetimeFixture) RefreshPersonal(ctx context.Context, material platform.MotherMaterial) (platform.PersonalRefreshResult, error) {
	f.entered <- ctx
	select {
	case <-f.release:
		return (&environmentPersonalFixture{}).RefreshPersonal(ctx, material)
	case <-ctx.Done():
		return platform.PersonalRefreshResult{}, ctx.Err()
	}
}

func TestTargetEnvironmentReservationSurvivesDisconnectButStopsWithService(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprintf("shutdown=%v", shutdown), func(t *testing.T) {
			pool, h, _, token, csrf := childReviewFixture(t)
			if err := sealExistingTargetMaterials(t.Context(), pool, cardIntegrationKeyRing{}); err != nil {
				t.Fatal(err)
			}
			var id uuid.UUID
			if err := pool.QueryRow(t.Context(), `SELECT id FROM tsw_target_accounts WHERE identifier='target@example.com'`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			service, stopService := context.WithCancel(t.Context())
			defer stopService()
			h.serviceContext = service
			fixture := &environmentLifetimeFixture{entered: make(chan context.Context, 1), release: make(chan struct{})}
			h.personalRefresh = fixture
			requestCtx, disconnect := context.WithCancel(t.Context())
			defer disconnect()
			req := httptest.NewRequestWithContext(requestCtx, "POST", "/api/owner/v1/target-accounts/"+id.String()+"/personal-session", nil)
			req.Header.Set("Origin", "https://owner.test")
			req.Header.Set(auth.CSRFHeaderName, csrf)
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				h.RefreshTargetPersonalAccess(httptest.NewRecorder(), req, id, ownerapi.RefreshTargetPersonalAccessParams{})
			}()
			var taskCtx context.Context
			select {
			case taskCtx = <-fixture.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("reserved action did not start")
			}
			disconnect()
			if shutdown {
				stopService()
			} else {
				select {
				case <-taskCtx.Done():
					t.Fatal("disconnect canceled the service-owned reservation")
				case <-time.After(50 * time.Millisecond):
				}
				access, err := h.targetPersonalAccess(t.Context(), id)
				if err != nil || access.Status != ownerapi.TargetPersonalAccessStatusVerifying {
					t.Fatal("pending result unavailable", access.Status, err)
				}
				close(fixture.release)
			}
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("reservation did not settle")
			}
			access, err := h.targetPersonalAccess(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			wanted := ownerapi.TargetPersonalAccessStatusReady
			if shutdown {
				wanted = ownerapi.TargetPersonalAccessStatusRefreshFailed
			}
			if access.Status != wanted {
				t.Fatal("GET did not recover terminal result", access.Status, wanted)
			}
		})
	}
}
