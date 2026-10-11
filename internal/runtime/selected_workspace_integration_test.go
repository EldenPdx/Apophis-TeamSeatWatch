//go:build integration

package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/identity"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/mothersecret"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/workspace"
)

type selectedRoundTrip func(*http.Request) (*http.Response, error)

func (fn selectedRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

type selectedFixture struct {
	facts platform.SelectedWorkspaceFacts
	err   error
}

func (f *selectedFixture) Source() string { return "injected_platform_reader" }
func (f *selectedFixture) VerifySelectedWorkspace(_ context.Context, _ platform.WorkspaceAccess, _, _ string) (platform.SelectedWorkspaceFacts, error) {
	return f.facts, f.err
}

type sequencedSelectedFixture struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
	success platform.SelectedWorkspaceFacts
}

func (f *sequencedSelectedFixture) Source() string { return "injected_platform_reader" }
func (f *sequencedSelectedFixture) VerifySelectedWorkspace(ctx context.Context, _ platform.WorkspaceAccess, _, _ string) (platform.SelectedWorkspaceFacts, error) {
	if f.calls.Add(1) != 1 {
		return platform.SelectedWorkspaceFacts{Permission: "denied", Result: platform.Result{ObservedAt: time.Now().UTC(), Outcome: platform.OutcomeForbidden}}, nil
	}
	close(f.entered)
	select {
	case <-f.release:
		return f.success, nil
	case <-ctx.Done():
		return platform.SelectedWorkspaceFacts{}, ctx.Err()
	}
}

type workspaceExchangeFailureFixture struct{ failure error }

func (f workspaceExchangeFailureFixture) ExchangeWorkspace(context.Context, platform.PersonalSession, string) (platform.WorkspaceAccess, error) {
	return platform.WorkspaceAccess{}, f.failure
}

type sequencedWorkspaceExchange struct {
	entered, release chan struct{}
	calls            atomic.Int32
}

func (f *sequencedWorkspaceExchange) ExchangeWorkspace(ctx context.Context, _ platform.PersonalSession, id string) (platform.WorkspaceAccess, error) {
	if f.calls.Add(1) != 1 {
		return platform.WorkspaceAccess{}, platform.ErrWorkspaceExchangeUnavailable
	}
	close(f.entered)
	select {
	case <-f.release:
		return fixtureWorkspaceAccess(id), nil
	case <-ctx.Done():
		return platform.WorkspaceAccess{}, ctx.Err()
	}
}

func fixtureWorkspaceJWT(id string) string {
	claims := fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":%q,"chatgpt_plan_type":"team","scp":["organization.read"]}}`, time.Now().Add(2*time.Hour).Unix(), id)
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".signature"
}
func fixtureWorkspaceAccess(id string) platform.WorkspaceAccess {
	return platform.WorkspaceAccess{WorkspaceID: id, AccessToken: fixtureWorkspaceJWT(id), DeviceID: "fixture-device", SessionID: uuid.NewString(), ExpiresAt: time.Now().Add(30 * time.Minute), Cookies: []platform.SessionCookie{
		{Name: "__Secure-next-auth.session-token", Value: "workspace-cookie"}, {Name: "_account", Value: id}, {Name: "oai-workspace", Value: id}}}
}

func TestSelectedWorkspaceFactsIsolatedAcrossMothersAndWorkspaces(t *testing.T) {
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
	ownerID, motherA, motherB, spaceA, spaceB, childID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	token, tokenHash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	csrf := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed %s: %v", query, err)
		}
	}
	seed(`INSERT INTO tsw_owners(id,username,password_hash) VALUES ($1,'selected-owner','hash')`, ownerID)
	seed(`INSERT INTO tsw_owner_sessions(owner_id,token_hash,auth_version,idle_expires_at,absolute_expires_at) VALUES ($1,$2,1,now()+interval '1 hour',now()+interval '2 hours')`, ownerID, tokenHash[:])
	for i, mother := range []uuid.UUID{motherA, motherB} {
		seed(`INSERT INTO tsw_mother_accounts(id,display_name) VALUES ($1,$2)`, mother, []string{"Mother A", "Mother B"}[i])
		seed(`INSERT INTO tsw_mother_account_credentials(mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret) VALUES ($1,$2,$3,1,'sealed-test')`, mother, []string{"a@example.test", "b@example.test"}[i], bytes.Repeat([]byte{byte(i + 1)}, 32))
		generation, run := uuid.New(), uuid.New()
		personal := platform.PersonalSession{AccessToken: "fixture-personal-token", DeviceID: "fixture-device", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "fixture-cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}
		keyVersion, nonce, sealed, sealErr := sealPersonalSession(cardIntegrationKeyRing{}, mother, 1, personal)
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		seed(`INSERT INTO tsw_mother_personal_sessions(mother_account_id,secret_revision,generation,key_version,nonce,sealed_session,expires_at) VALUES ($1,1,$2,$3,$4,$5,$6)`, mother, generation, keyVersion, nonce, sealed, personal.ExpiresAt)
		seed(`INSERT INTO tsw_mother_discoveries(mother_account_id,run_id,secret_revision,session_generation,status) VALUES ($1,$2,1,$3,'discovered')`, mother, run, generation)
	}
	seed(`INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES ($1,'canonical-a','A'),($2,'canonical-b','B')`, spaceA, spaceB)
	seed(`INSERT INTO tsw_workspace_projections(workspace_id) VALUES ($1),($2)`, spaceA, spaceB)
	seed(`INSERT INTO tsw_mother_workspace_visibility(mother_account_id,workspace_id,run_id,access_status) SELECT $1,$2,run_id,'readable' FROM tsw_mother_discoveries WHERE mother_account_id=$1`, motherA, spaceA)
	seed(`INSERT INTO tsw_mother_workspace_visibility(mother_account_id,workspace_id,run_id,access_status) SELECT $1,$2,run_id,'readable' FROM tsw_mother_discoveries WHERE mother_account_id=$1`, motherA, spaceB)
	seed(`INSERT INTO tsw_mother_workspace_visibility(mother_account_id,workspace_id,run_id,access_status) SELECT $1,$2,run_id,'readable' FROM tsw_mother_discoveries WHERE mother_account_id=$1`, motherB, spaceA)
	for _, pair := range []struct {
		mother, space uuid.UUID
		platformID    string
	}{{motherA, spaceA, "canonical-a"}, {motherB, spaceA, "canonical-a"}, {motherA, spaceB, "canonical-b"}} {
		var run, generation uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT discovery.run_id,session.generation FROM tsw_mother_discoveries discovery JOIN tsw_mother_personal_sessions session ON session.mother_account_id=discovery.mother_account_id WHERE discovery.mother_account_id=$1`, pair.mother).Scan(&run, &generation); err != nil {
			t.Fatal(err)
		}
		b := workspaceAccessBinding{motherID: pair.mother, workspaceID: pair.space, run: run, generation: generation, revision: 1, attempt: 1, exchangeID: uuid.New()}
		access := fixtureWorkspaceAccess(pair.platformID)
		keyVersion, nonce, sealed, sealErr := sealWorkspaceAccess(cardIntegrationKeyRing{}, b, access)
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		seed(`INSERT INTO tsw_selected_workspace_tokens(mother_account_id,workspace_id,discovery_run_id,session_generation,secret_revision,attempt,exchange_id,status,key_version,nonce,sealed_access,expires_at) VALUES($1,$2,$3,$4,1,1,$5,'ready',$6,$7,$8,$9)`, pair.mother, pair.space, run, generation, b.exchangeID, keyVersion, nonce, sealed, access.ExpiresAt)
	}
	identifier, version, fingerprint, err := identity.Fingerprint(cardIntegrationKeyRing{}, identity.TargetLogin, "child@example.test")
	if err != nil {
		t.Fatal(err)
	}
	seed(`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES ($1,$2,$3,$4,'Child')`, childID, identifier, fingerprint[:], version)
	origins, _ := auth.ParseOriginPolicy("https://owner.test")
	fixture := &selectedFixture{}
	h := &OwnerAuthHandler{pool: pool, keyRing: cardIntegrationKeyRing{}, origins: origins, selectedWorkspaceReader: fixture}
	serve := ownerapi.Handler(h)
	call := func(method string, workspace, mother uuid.UUID, confirmed bool, query ...string) (int, ownerapi.SelectedWorkspaceVerification) {
		t.Helper()
		path := "/api/owner/v1/workspaces/" + workspace.String() + "/verification"
		var body []byte
		if method == http.MethodGet {
			path += "?motherAccountId=" + mother.String()
			for _, value := range query {
				path += "&" + value
			}
		} else {
			body, _ = json.Marshal(map[string]any{"motherAccountId": mother, "confirmed": confirmed})
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Origin", "https://owner.test")
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		if method == http.MethodPost {
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			req.Header.Set(auth.CSRFHeaderName, csrf)
		}
		rec := httptest.NewRecorder()
		serve.ServeHTTP(rec, req)
		var result ownerapi.SelectedWorkspaceVerification
		if rec.Code == 200 && json.Unmarshal(rec.Body.Bytes(), &result) != nil {
			t.Fatalf("invalid response: %s", rec.Body.String())
		}
		return rec.Code, result
	}
	callAccess := func(method string, workspace, mother uuid.UUID, confirmed bool) (int, ownerapi.SelectedWorkspaceAccessStatus, string) {
		t.Helper()
		path := "/api/owner/v1/workspaces/" + workspace.String() + "/access"
		var body []byte
		if method == http.MethodGet {
			path += "?motherAccountId=" + mother.String()
		} else {
			body, _ = json.Marshal(map[string]any{"motherAccountId": mother, "confirmed": confirmed})
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Origin", "https://owner.test")
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		if method == http.MethodPost {
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			req.Header.Set(auth.CSRFHeaderName, csrf)
		}
		rec := httptest.NewRecorder()
		serve.ServeHTTP(rec, req)
		var result ownerapi.SelectedWorkspaceAccessStatus
		if rec.Code == 200 && json.Unmarshal(rec.Body.Bytes(), &result) != nil {
			t.Fatalf("invalid token status: %s", rec.Body.String())
		}
		return rec.Code, result, rec.Body.String()
	}
	fact := func() platform.SelectedWorkspaceFacts {
		until := time.Now().Add(30 * 24 * time.Hour)
		seat, member, invite := 5, 1, 1
		return platform.SelectedWorkspaceFacts{Permission: "read", Result: platform.Result{Outcome: platform.OutcomeOperational, ObservedAt: time.Now().Add(-time.Second), Completeness: platform.Complete, SubscriptionStatus: "delinquent", ActiveUntil: &until, SeatLimit: &seat, MemberCount: &member, PendingInviteCount: &invite, Members: []platform.Member{{Kind: "member", Identifier: "child@example.test", PlatformMemberID: "member-1", Status: "active"}, {Kind: "pending_invite", Identifier: "guest@example.test", Status: "pending"}}}, Sources: []platform.ReadEvidence{{Source: "subscriptions", ObservedAt: time.Now().Add(-time.Second), Completeness: platform.Complete, Permission: "read", Outcome: platform.OutcomeOperational}}}
	}
	seed(`INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at)
		SELECT $1,$2,token.discovery_run_id,token.session_generation,1,token.attempt,token.exchange_id,'injected_platform_reader','failed','unknown','unknown',now()-interval '8 days',now()-interval '1 day'
		FROM tsw_selected_workspace_tokens token WHERE token.mother_account_id=$2 AND token.workspace_id=$1`, spaceA, motherA)
	if code, stale := call(http.MethodGet, spaceA, motherA, true); code != 200 || stale.Status != "stale" || stale.ActiveUntil != nil {
		t.Fatalf("stale read became current: %d %+v", code, stale)
	}
	if count, err := workspace.NewService(pool, cardIntegrationKeyRing{}).DeleteExpired(ctx, 100); err != nil || count != 1 {
		t.Fatalf("expired verification cleanup count=%d err=%v", count, err)
	}
	if _, pending := call(http.MethodGet, spaceA, motherA, true); pending.Status != "pending" {
		t.Fatalf("cleanup manufactured facts: %+v", pending)
	}
	if code, _ := call(http.MethodGet, spaceB, motherB, true); code != 409 {
		t.Fatalf("unselected mother/space became visible: %d", code)
	}
	for _, invalid := range []struct {
		origin string
		csrf   bool
	}{{"https://foreign.test", true}, {"https://owner.test", false}} {
		body, _ := json.Marshal(map[string]any{"motherAccountId": motherA, "confirmed": true})
		req := httptest.NewRequest(http.MethodPost, "/api/owner/v1/workspaces/"+spaceA.String()+"/verification", bytes.NewReader(body))
		req.Header.Set("Origin", invalid.origin)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		if invalid.csrf {
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			req.Header.Set(auth.CSRFHeaderName, csrf)
		}
		rec := httptest.NewRecorder()
		serve.ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Fatalf("invalid Origin/CSRF admitted: %+v", invalid)
		}
	}
	fixture.facts = fact()
	for _, pair := range [][2]uuid.UUID{{spaceA, motherA}, {spaceA, motherB}, {spaceB, motherA}} {
		code, result := call(http.MethodPost, pair[0], pair[1], true)
		if code != 200 || result.Status != "verified" || len(result.Members) != 1 || result.Members[0].ChildAccountId == nil || *result.Members[0].ChildAccountId != childID {
			t.Fatalf("verified %v: %d %+v", pair, code, result)
		}
		if result.SubscriptionStatus == nil || *result.SubscriptionStatus != "delinquent" {
			t.Fatalf("scoped billing state missing: %+v", result)
		}
	}
	// Each page reads only the selected entry kind from the same mother snapshot.
	fixture.facts = fact()
	fixture.facts.Result.Members = []platform.Member{
		{Kind: "member", Identifier: "child@example.test", PlatformMemberID: "child", Role: "member", Status: "active"},
		{Kind: "member", Identifier: "a@example.test", PlatformMemberID: "mother-a", Role: "owner", Status: "active"},
	}
	for i := 0; i < 99; i++ {
		fixture.facts.Result.Members = append(fixture.facts.Result.Members, platform.Member{Kind: "member", Identifier: fmt.Sprintf("member-%03d@example.test", i), PlatformMemberID: fmt.Sprintf("member-%03d", i), Status: "active"})
	}
	for i := 0; i < 51; i++ {
		fixture.facts.Result.Members = append(fixture.facts.Result.Members, platform.Member{Kind: "pending_invite", Identifier: fmt.Sprintf("guest-%03d@example.test", i), Status: "pending"})
	}
	memberTotal, inviteTotal := 101, 51
	fixture.facts.Result.MemberCount, fixture.facts.Result.PendingInviteCount = &memberTotal, &inviteTotal
	fixture.facts.Result.SeatTypeCounts = map[string]int{"default": 2, "prolite": 99}
	fixture.facts.Result.SeatEntitlements = map[string]int{"default": 2, "prolite": 9}
	if code, result := call(http.MethodPost, spaceA, motherA, true); code != 200 || len(result.Members) != 20 || result.Total == nil || *result.Total != 101 || result.MotherRole == nil || *result.MotherRole != "owner" || !result.CanManage || result.SeatTypeCounts == nil || (*result.SeatTypeCounts)["default"] != 2 {
		t.Fatalf("default member page/facts: %d %+v", code, result)
	}
	if _, result := call(http.MethodGet, spaceA, motherA, true); result.SeatEntitlements == nil || (*result.SeatEntitlements)["default"] != 2 || (*result.SeatEntitlements)["prolite"] != 9 {
		t.Fatalf("explicit amounts were lost or confused with occupancy: %+v", result)
	}
	for _, expected := range []struct {
		kind                     string
		page, size, count, total int
	}{
		{"member", 2, 20, 20, 101}, {"member", 6, 20, 1, 101}, {"member", 3, 50, 1, 101}, {"member", 2, 100, 1, 101},
		{"pending_invite", 2, 20, 20, 51}, {"pending_invite", 3, 20, 11, 51}, {"pending_invite", 2, 50, 1, 51}, {"pending_invite", 1, 100, 51, 51},
	} {
		query := fmt.Sprintf("entry_kind=%s&page=%d&page_size=%d", expected.kind, expected.page, expected.size)
		code, result := call(http.MethodGet, spaceA, motherA, true, query)
		if code != 200 || len(result.Members) != expected.count || result.Total == nil || *result.Total != expected.total || result.Page == nil || *result.Page != expected.page || result.PageSize == nil || *result.PageSize != expected.size {
			t.Fatalf("page %s: %d %+v", query, code, result)
		}
		for _, entry := range result.Members {
			if string(entry.Kind) != expected.kind {
				t.Fatalf("mixed entry kinds: %+v", entry)
			}
		}
	}
	if _, result := call(http.MethodGet, spaceA, motherB, true, "entry_kind=pending_invite"); result.Total == nil || *result.Total != 1 || len(result.Members) != 1 || result.Members[0].Identifier != "guest@example.test" || result.CanManage {
		t.Fatalf("page leaked another mother's snapshot: %+v", result)
	}
	if _, result := call(http.MethodGet, spaceB, motherA, true); result.Total == nil || *result.Total != 1 {
		t.Fatalf("page leaked another workspace: %+v", result)
	}
	for _, query := range []string{"page=0", "page_size=101", "entry_kind=unknown"} {
		if code, _ := call(http.MethodGet, spaceA, motherA, true, query); code != 422 {
			t.Fatalf("invalid page admitted %s: %d", query, code)
		}
	}
	fixture.facts = fact()
	sequenced := &sequencedSelectedFixture{entered: make(chan struct{}), release: make(chan struct{}), success: fact()}
	h.selectedWorkspaceReader = sequenced
	first := make(chan int, 1)
	go func() { code, _ := call(http.MethodPost, spaceA, motherA, true); first <- code }()
	select {
	case <-sequenced.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first read never started")
	}
	if _, pending := call(http.MethodGet, spaceA, motherA, true); pending.Status != "verifying" || pending.ActiveUntil != nil || len(pending.Members) != 0 {
		t.Fatalf("in-flight read exposed previous success: %+v", pending)
	}
	if _, denied := call(http.MethodPost, spaceA, motherA, true); denied.Status != "permission_denied" {
		t.Fatalf("newer denial not committed: %+v", denied)
	}
	close(sequenced.release)
	select {
	case code := <-first:
		if code != 409 {
			t.Fatalf("superseded success status=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("superseded read did not finish")
	}
	if _, denied := call(http.MethodGet, spaceA, motherA, true); denied.Status != "permission_denied" || denied.ActiveUntil != nil {
		t.Fatalf("slow success restored facts: %+v", denied)
	}
	h.selectedWorkspaceReader = fixture
	fixture.facts = fact()
	fixture.facts.Result.Completeness = platform.Partial
	fixture.facts.Result.MemberCount = nil
	fixture.facts.Result.SeatEntitlements = map[string]int{"default": 2, "prolite": 9}
	fixture.facts.Sources = []platform.ReadEvidence{{Source: "subscriptions", ObservedAt: time.Now().UTC(), Completeness: platform.Complete, Permission: "read", Outcome: platform.OutcomeOperational}}
	code, partial := call(http.MethodPost, spaceA, motherA, true)
	if code != 200 || partial.Status != "partial" || partial.MemberCount != nil || len(partial.Members) != 0 {
		t.Fatalf("partial: %d %+v", code, partial)
	}
	if partial.CanManage || partial.SeatEntitlements == nil || (*partial.SeatEntitlements)["prolite"] != 9 {
		t.Fatalf("partial subscription amounts missing or granted roster authority: %+v", partial)
	}
	if partial.SubscriptionStatus == nil || *partial.SubscriptionStatus != "delinquent" || partial.ActiveUntil == nil {
		t.Fatalf("partial billing state missing: %+v", partial)
	}
	if _, saved := call(http.MethodGet, spaceA, motherA, true); saved.SubscriptionStatus == nil || *saved.SubscriptionStatus != "delinquent" || saved.ActiveUntil == nil {
		t.Fatalf("saved billing state missing: %+v", saved)
	}
	if _, independent := call(http.MethodGet, spaceB, motherA, true); independent.Status != "verified" || independent.MemberCount == nil {
		t.Fatalf("other workspace changed: %+v", independent)
	}
	fixture.err = errors.New("network down")
	_, failed := call(http.MethodPost, spaceA, motherA, true)
	if failed.Status != "failed" || failed.ActiveUntil != nil || failed.SeatEntitlements != nil || failed.SubscriptionStatus != nil {
		t.Fatalf("failure retained old facts: %+v", failed)
	}
	if _, other := call(http.MethodGet, spaceA, motherB, true); other.Status != "verified" {
		t.Fatalf("other mother changed: %+v", other)
	}
	fixture.err, fixture.facts = nil, fact()
	fixture.facts.Permission = "read"
	if _, readOnly := call(http.MethodPost, spaceA, motherA, true); readOnly.Status != "verified" || readOnly.Permission != "read" || readOnly.ActiveUntil == nil {
		t.Fatalf("read facts incorrectly granted management or suppressed complete facts: %+v", readOnly)
	}
	fixture.facts.Permission = "denied"
	if _, denied := call(http.MethodPost, spaceA, motherA, true); denied.Status != "permission_denied" || len(denied.Members) != 0 {
		t.Fatalf("adapter downgrade leaked facts: %+v", denied)
	}
	seed(`UPDATE tsw_mother_workspace_visibility SET access_status='permission_denied' WHERE mother_account_id=$1 AND workspace_id=$2`, motherB, spaceA)
	if _, denied := call(http.MethodGet, spaceA, motherB, true); denied.Status != "permission_denied" || len(denied.Members) != 0 {
		t.Fatalf("downgrade leaked facts: %+v", denied)
	}
	if code, _ := call(http.MethodPost, spaceA, motherB, true); code != 403 {
		t.Fatalf("downgraded refresh=%d", code)
	}
	// A new discovery and Personal generation must not revive a previous
	// verified observation even when canonical workspace and mother are unchanged.
	newRun, newGeneration := uuid.New(), uuid.New()
	seed(`UPDATE tsw_mother_discoveries SET run_id=$2 WHERE mother_account_id=$1`, motherA, newRun)
	seed(`UPDATE tsw_mother_workspace_visibility SET run_id=$2 WHERE mother_account_id=$1`, motherA, newRun)
	if _, pending := call(http.MethodGet, spaceB, motherA, true); pending.Status != "pending" || pending.ActiveUntil != nil || len(pending.Members) != 0 {
		t.Fatalf("rediscovery revived facts: %+v", pending)
	}
	seed(`UPDATE tsw_mother_personal_sessions SET generation=$2 WHERE mother_account_id=$1`, motherA, newGeneration)
	seed(`UPDATE tsw_mother_discoveries SET session_generation=$2 WHERE mother_account_id=$1`, motherA, newGeneration)
	if _, pending := call(http.MethodGet, spaceB, motherA, true); pending.Status != "pending" || len(pending.Members) != 0 {
		t.Fatalf("Personal refresh revived facts: %+v", pending)
	}
	// Fact reads must refuse the Personal bearer until a separate confirmed
	// exchange has produced a Workspace-scoped credential.
	if code, _ := call(http.MethodPost, spaceB, motherA, true); code != 409 {
		t.Fatalf("facts without explicit exchange=%d", code)
	}
	if code, status, _ := callAccess(http.MethodGet, spaceB, motherA, false); code != 200 || status.Status != "required" {
		t.Fatalf("unexchanged status=%d %+v", code, status)
	}
	// Even a sealed row claiming ready cannot admit a Personal-plan JWT with
	// this canonical ID; the same validator gates status and fact reservation.
	badAccess := fixtureWorkspaceAccess("canonical-b")
	badClaims := fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"canonical-b","chatgpt_plan_type":"free","scp":["organization.read"]}}`, time.Now().Add(2*time.Hour).Unix())
	badAccess.AccessToken = "header." + base64.RawURLEncoding.EncodeToString([]byte(badClaims)) + ".signature"
	badBinding := workspaceAccessBinding{motherID: motherA, workspaceID: spaceB, run: newRun, generation: newGeneration, revision: 1, attempt: 2, exchangeID: uuid.New()}
	badVersion, badNonce, badSealed, sealErr := sealWorkspaceAccess(cardIntegrationKeyRing{}, badBinding, badAccess)
	if sealErr != nil {
		t.Fatal(sealErr)
	}
	seed(`UPDATE tsw_selected_workspace_tokens SET discovery_run_id=$3,session_generation=$4,attempt=2,exchange_id=$5,key_version=$6,nonce=$7,sealed_access=$8,expires_at=$9,status='ready' WHERE mother_account_id=$1 AND workspace_id=$2`, motherA, spaceB, newRun, newGeneration, badBinding.exchangeID, badVersion, badNonce, badSealed, badAccess.ExpiresAt)
	if code, status, _ := callAccess(http.MethodGet, spaceB, motherA, false); code != 200 || status.Status != "required" || status.ExchangeId != nil {
		t.Fatalf("Personal-plan sealed bearer became ready: %d %+v", code, status)
	}
	if code, _ := call(http.MethodPost, spaceB, motherA, true); code != 409 {
		t.Fatalf("Personal-plan sealed bearer reached facts: %d", code)
	}
	var originalPersonal []byte
	if err := pool.QueryRow(ctx, `SELECT sealed_session FROM tsw_mother_personal_sessions WHERE mother_account_id=$1`, motherA).Scan(&originalPersonal); err != nil {
		t.Fatal(err)
	}
	issuedToken := fixtureWorkspaceJWT("canonical-b")
	var exchangeCalls atomic.Int32
	exchangeTransport := selectedRoundTrip(func(req *http.Request) (*http.Response, error) {
		exchangeCalls.Add(1)
		if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Host != "chatgpt.com" || req.URL.Path != "/api/auth/session" || req.URL.RawQuery != "exchange_workspace_token=true&workspace_id=canonical-b&reason=setCurrentAccountWithoutRedirect" || req.Header.Get("Authorization") != "" || !strings.Contains(req.Header.Get("Cookie"), "_account=canonical-b") || !strings.Contains(req.Header.Get("Cookie"), "fixture-cookie") {
			return nil, errors.New("unexpected exchange route or Personal session")
		}
		body, _ := json.Marshal(map[string]any{"accessToken": issuedToken, "account": map[string]any{"id": "canonical-b"}, "expires": time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{"Set-Cookie": []string{"__Secure-next-auth.session-token=workspace-cookie; Path=/; Secure"}}, Request: req}, nil
	})
	officialExchanger := platform.OfficialWorkspaceTokenExchanger{Client: func(context.Context) (*http.Client, func(), error) {
		return &http.Client{Transport: exchangeTransport, Timeout: time.Second}, func() {}, nil
	}}
	h.workspaceTokenExchanger = officialExchanger
	for _, invalid := range []struct {
		origin string
		csrf   bool
	}{{"https://foreign.test", true}, {"https://owner.test", false}} {
		body, _ := json.Marshal(map[string]any{"motherAccountId": motherA, "confirmed": true})
		req := httptest.NewRequest(http.MethodPost, "/api/owner/v1/workspaces/"+spaceB.String()+"/access", bytes.NewReader(body))
		req.Header.Set("Origin", invalid.origin)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		if invalid.csrf {
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			req.Header.Set(auth.CSRFHeaderName, csrf)
		}
		rec := httptest.NewRecorder()
		serve.ServeHTTP(rec, req)
		if rec.Code == 200 || exchangeCalls.Load() != 0 {
			t.Fatalf("unsafe exchange request admitted: %d %+v", rec.Code, invalid)
		}
	}
	if code, status, body := callAccess(http.MethodPost, spaceB, motherA, true); code != 200 || status.Status != "ready" || strings.Contains(body, issuedToken) || strings.Contains(body, "workspace-cookie") {
		t.Fatalf("explicit exchange failed/leaked: %d %+v %s", code, status, body)
	}
	if exchangeCalls.Load() != 1 {
		t.Fatalf("exchange called %d times", exchangeCalls.Load())
	}
	var afterPersonal []byte
	if err := pool.QueryRow(ctx, `SELECT sealed_session FROM tsw_mother_personal_sessions WHERE mother_account_id=$1`, motherA).Scan(&afterPersonal); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(originalPersonal, afterPersonal) {
		t.Fatal("exchange overwrote saved Personal session")
	}
	// All fact endpoints now use the exchanged Workspace bearer, never Personal.
	var httpCalls, httpMode atomic.Int32
	transport := selectedRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Host != "chatgpt.com" || req.Header.Get("Authorization") != "Bearer "+issuedToken || !strings.Contains(req.Header.Get("Cookie"), "workspace-cookie") || (strings.HasSuffix(req.URL.Path, "/invites") && req.Header.Get("Oai-Session-Id") == "") {
			return nil, errors.New("unexpected platform operation")
		}
		httpCalls.Add(1)
		if httpMode.Load() == 1 && strings.HasSuffix(req.URL.Path, "/invites") {
			return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header), Request: req}, nil
		}
		var body string
		switch {
		case req.URL.Path == "/backend-api/subscriptions" && req.URL.Query().Get("account_id") == "canonical-b":
			body = `{"active_until":"2027-01-01T00:00:00Z","seats_in_use":2,"seats_entitled":3,"seat_capacity":[{"type":"default","paid":3}]}`
		case req.URL.Path == "/backend-api/accounts/canonical-b/users/seat_type_counts":
			body = `{"seat_type_counts":{"default":2,"usage_based":0,"automation":0,"prolite":0}}`
		case req.URL.Path == "/backend-api/accounts/canonical-b/users" && req.URL.Query().Get("offset") == "0":
			body = `{"total":2,"limit":100,"offset":0,"items":[{"id":"user-a","email":"child@example.test","role":"owner","seat_type":"default"}]}`
		case req.URL.Path == "/backend-api/accounts/canonical-b/users" && req.URL.Query().Get("offset") == "1":
			if httpMode.Load() == 2 {
				body = `{"total":2,"limit":100,"offset":1,"items":[]}`
			} else {
				body = `{"total":2,"limit":100,"offset":1,"items":[{"id":"user-b","email":"other@example.test","role":"standard-user","seat_type":"default"}]}`
			}
		case req.URL.Path == "/backend-api/accounts/canonical-b/invites" && req.URL.Query().Get("offset") == "0":
			body = `{"total":1,"limit":100,"offset":0,"items":[{"email_address":"invited@example.test","status":2,"seat_type":"default"}]}`
		default:
			return nil, errors.New("endpoint not allowlisted")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
	})
	reader := platform.OfficialSelectedWorkspaceReader{Client: func(context.Context) (*http.Client, func(), error) {
		return &http.Client{Transport: transport, Timeout: time.Second}, func() {}, nil
	}}
	h.selectedWorkspaceReader = reader
	if code, observed := call(http.MethodPost, spaceB, motherA, true); code != 200 || observed.Status != "verified" || observed.Permission != "read" || observed.SeatLimit == nil || *observed.SeatLimit != 3 || len(observed.Members) != 2 || observed.ReadSources == nil || len(*observed.ReadSources) != 4 {
		t.Fatalf("saved Personal/egress read: %d %+v", code, observed)
	}
	if httpCalls.Load() != 5 {
		t.Fatalf("expected five official GETs, got %d", httpCalls.Load())
	}
	httpMode.Store(1)
	if _, denied := call(http.MethodPost, spaceB, motherA, true); denied.Status != "permission_denied" || denied.ActiveUntil != nil {
		t.Fatalf("official 403 kept old facts: %+v", denied)
	}
	httpMode.Store(2)
	if _, partial := call(http.MethodPost, spaceB, motherA, true); partial.Status != "partial" || partial.MemberCount != nil {
		t.Fatalf("official incomplete page became complete: %+v", partial)
	}
	httpMode.Store(0)
	if _, repaired := call(http.MethodPost, spaceB, motherA, true); repaired.Status != "verified" {
		t.Fatalf("official complete retry: %+v", repaired)
	}
	// Two independent Owner GETs may straddle a cross-tab re-exchange. Both
	// responses must carry non-secret IDs so old facts never pair with new access.
	_, oldFacts := call(http.MethodGet, spaceB, motherA, true)
	if oldFacts.ExchangeId == nil {
		t.Fatal("verified facts omitted exchange identity")
	}
	_, newAccess, _ := callAccess(http.MethodPost, spaceB, motherA, true)
	if newAccess.Status != "ready" || newAccess.ExchangeId == nil || *newAccess.ExchangeId == *oldFacts.ExchangeId {
		t.Fatalf("new exchange did not advance non-secret identity: %+v", newAccess)
	}
	if _, pendingFacts := call(http.MethodGet, spaceB, motherA, true); pendingFacts.Status != "pending" || pendingFacts.ExchangeId == nil || *pendingFacts.ExchangeId != *newAccess.ExchangeId {
		t.Fatalf("current exchange was paired with older facts: %+v", pendingFacts)
	}
	if _, currentFacts := call(http.MethodPost, spaceB, motherA, true); currentFacts.Status != "verified" || currentFacts.ExchangeId == nil || *currentFacts.ExchangeId != *newAccess.ExchangeId {
		t.Fatalf("new facts lack current exchange identity: %+v", currentFacts)
	}
	// A fresh handler sees only current-generation evidence for its reader.
	h = &OwnerAuthHandler{pool: pool, keyRing: cardIntegrationKeyRing{}, origins: origins, selectedWorkspaceReader: reader}
	serve = ownerapi.Handler(h)
	if _, persisted := call(http.MethodGet, spaceB, motherA, true); persisted.Status != "verified" || persisted.Permission != "read" || len(persisted.Members) != 2 {
		t.Fatalf("restart lost current evidence: %+v", persisted)
	}
	h = &OwnerAuthHandler{pool: pool, keyRing: cardIntegrationKeyRing{}, origins: origins}
	serve = ownerapi.Handler(h)
	if _, unsupported := call(http.MethodGet, spaceB, motherA, true); unsupported.Status != "pending" || unsupported.ActiveUntil != nil {
		t.Fatalf("missing protocol exposed mock facts: %+v", unsupported)
	}
	if code, _ := call(http.MethodPost, spaceB, motherA, true); code != 501 {
		t.Fatalf("missing protocol=%d", code)
	}
	if code, _ := call(http.MethodPost, spaceB, motherA, false); code != 422 {
		t.Fatalf("unconfirmed selection=%d", code)
	}
	// Overlapping explicit exchanges fence slow old success without holding a
	// database lock through platform I/O. Starting an exchange hides old facts.
	sequencedExchange := &sequencedWorkspaceExchange{entered: make(chan struct{}), release: make(chan struct{})}
	h = &OwnerAuthHandler{pool: pool, keyRing: cardIntegrationKeyRing{}, origins: origins, selectedWorkspaceReader: reader, workspaceTokenExchanger: sequencedExchange}
	serve = ownerapi.Handler(h)
	firstExchange := make(chan int, 1)
	go func() { code, _, _ := callAccess(http.MethodPost, spaceB, motherA, true); firstExchange <- code }()
	select {
	case <-sequencedExchange.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first exchange never entered")
	}
	if _, pending := call(http.MethodGet, spaceB, motherA, true); pending.Status != "pending" || pending.ActiveUntil != nil {
		t.Fatalf("exchanging exposed previous facts: %+v", pending)
	}
	if _, state, _ := callAccess(http.MethodGet, spaceB, motherA, false); state.Status != "exchanging" {
		t.Fatalf("exchange reservation missing: %+v", state)
	}
	if _, state, _ := callAccess(http.MethodPost, spaceB, motherA, true); state.Status != "failed" {
		t.Fatalf("newer failed exchange not recorded: %+v", state)
	}
	close(sequencedExchange.release)
	select {
	case code := <-firstExchange:
		if code != 409 {
			t.Fatalf("superseded exchange=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("superseded exchange held DB lock")
	}
	if _, state, _ := callAccess(http.MethodGet, spaceB, motherA, false); state.Status != "failed" {
		t.Fatalf("slow success revived token: %+v", state)
	}
	h.workspaceTokenExchanger = officialExchanger
	if _, state, _ := callAccess(http.MethodPost, spaceB, motherA, true); state.Status != "ready" {
		t.Fatalf("exchange retry failed: %+v", state)
	}
	if _, pending := call(http.MethodGet, spaceB, motherA, true); pending.Status != "pending" {
		t.Fatalf("old read revived after token rotation: %+v", pending)
	}
	if _, restored := call(http.MethodPost, spaceB, motherA, true); restored.Status != "verified" {
		t.Fatalf("facts retry failed: %+v", restored)
	}

	// The persisted cause survives a GET and a handler restart. A confirmed
	// remote 401 invalidates this generation, without changing saved materials.
	seed(`INSERT INTO tsw_mother_personal_access(mother_account_id,secret_revision,status) VALUES($1,1,'ready') ON CONFLICT(mother_account_id) DO UPDATE SET status='ready'`, motherA)
	h.workspaceTokenExchanger = workspaceExchangeFailureFixture{&platform.WorkspaceExchangeFailure{Code: "workspace_token_revoked", HTTPStatus: 401, UpstreamCode: "token_revoked", RouteMode: "required", ProxyScheme: "socks5h"}}
	code, diagnosed, publicBody := callAccess(http.MethodPost, spaceB, motherA, true)
	if code != 200 || diagnosed.Status != "failed" || diagnosed.Failure == nil || diagnosed.Failure.Code != "workspace_token_revoked" || diagnosed.CheckedAt == nil || strings.Contains(publicBody, "fixture-personal-token") || strings.Contains(publicBody, "fixture-cookie") {
		t.Fatalf("missing/leaked persisted diagnosis: %d %+v", code, diagnosed)
	}
	_, persisted, _ := callAccess(http.MethodGet, spaceB, motherA, false)
	if persisted.Failure == nil || persisted.Failure.UpstreamCode == nil || *persisted.Failure.UpstreamCode != "token_revoked" || !persisted.CheckedAt.Equal(*diagnosed.CheckedAt) {
		t.Fatalf("GET lost revocation: %+v", persisted)
	}
	personalState, stateErr := h.motherPersonalAccess(httptest.NewRequest(http.MethodGet, "/", nil), motherA)
	if stateErr != nil || personalState.Status != "session_expired" {
		t.Fatalf("revoked generation still says ready: %+v %v", personalState, stateErr)
	}
	// A delayed old diagnosis cannot mark a newly logged-in generation invalid.
	reserved, reserveErr := h.reserveWorkspaceTokenExchange(ctx, spaceB, motherA)
	if reserveErr != nil {
		t.Fatal(reserveErr)
	}
	freshGeneration := uuid.New()
	seed(`UPDATE tsw_mother_personal_sessions SET generation=$2 WHERE mother_account_id=$1`, motherA, freshGeneration)
	seed(`UPDATE tsw_mother_discoveries SET session_generation=$2 WHERE mother_account_id=$1`, motherA, freshGeneration)
	seed(`UPDATE tsw_mother_personal_access SET status='ready' WHERE mother_account_id=$1`, motherA)
	finishErr := h.finishWorkspaceTokenExchange(ctx, reserved, platform.WorkspaceAccess{}, &platform.WorkspaceExchangeFailure{Code: "workspace_token_revoked", HTTPStatus: 401, UpstreamCode: "token_revoked"})
	if !errors.Is(finishErr, errSelectedAttemptSuperseded) {
		t.Fatalf("stale revocation published: %v", finishErr)
	}
	personalState, stateErr = h.motherPersonalAccess(httptest.NewRequest(http.MethodGet, "/", nil), motherA)
	if stateErr != nil || personalState.Status != "ready" {
		t.Fatal("stale failure invalidated fresh login")
	}
	h.workspaceTokenExchanger = officialExchanger
	_, repairedAccess, _ := callAccess(http.MethodPost, spaceB, motherA, true)
	if repairedAccess.Status != "ready" || repairedAccess.Failure != nil {
		t.Fatalf("successful retry retained failure: %+v", repairedAccess)
	}
	// Revision change during network I/O revokes the reserved token publication.
	lateExchange := &sequencedWorkspaceExchange{entered: make(chan struct{}), release: make(chan struct{})}
	h.workspaceTokenExchanger = lateExchange
	late := make(chan int, 1)
	go func() { code, _, _ := callAccess(http.MethodPost, spaceB, motherA, true); late <- code }()
	select {
	case <-lateExchange.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("revision exchange never entered")
	}
	sealedPassword, sealErr := mothersecret.Seal(cardIntegrationKeyRing{}, motherA, 2, mothersecret.Password, []byte("rotated-password"))
	if sealErr != nil {
		t.Fatal(sealErr)
	}
	seed(`UPDATE tsw_mother_account_credentials SET password_secret=$2,secret_revision=secret_revision+1,version=version+1 WHERE mother_account_id=$1`, motherA, sealedPassword)
	close(lateExchange.release)
	select {
	case code := <-late:
		if code != 409 {
			t.Fatalf("revision-fenced exchange=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revision exchange held DB lock")
	}
	if code, _ := call(http.MethodGet, spaceB, motherA, true); code != 409 {
		t.Fatalf("old revision kept facts visible: %d", code)
	}
	if _, independent := call(http.MethodGet, spaceA, motherB, true); independent.Status != "permission_denied" {
		t.Fatalf("other mother's visibility changed: %+v", independent)
	}
}
