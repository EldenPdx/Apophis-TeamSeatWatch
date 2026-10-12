//go:build integration

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/mothersecret"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/workspace"
)

type selectedRemovalFixture struct {
	pool                 *pgxpool.Pool
	h                    *OwnerAuthHandler
	batch, mother, space uuid.UUID
	session, csrf        string
}

func newSelectedRemovalFixture(t *testing.T) selectedRemovalFixture {
	t.Helper()
	pool, h, _, session, csrf := childReviewFixture(t)
	f := selectedRemovalFixture{pool: pool, h: h, session: session, csrf: csrf}
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `SELECT b.id,binding.mother_account_id,binding.workspace_id FROM tsw_batches b JOIN tsw_mother_workspace_bindings binding ON binding.id=b.binding_id`).Scan(&f.batch, &f.mother, &f.space); err != nil {
		t.Fatal(err)
	}
	run, generation, exchange := uuid.New(), uuid.New(), uuid.New()
	access := fixtureWorkspaceAccess("workspace-card")
	key, nonce, sealed, err := sealWorkspaceAccess(h.keyRing, workspaceAccessBinding{motherID: f.mother, workspaceID: f.space, run: run, generation: generation, revision: 1, attempt: 1, exchangeID: exchange}, access)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	password, err := mothersecret.Seal(h.keyRing, f.mother, 1, mothersecret.Password, []byte("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO tsw_mother_account_credentials(mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret) VALUES($1,'owner@example.test',decode(repeat('71',32),'hex'),1,$2)`, f.mother, password)
	exec(`INSERT INTO tsw_mother_personal_sessions(mother_account_id,secret_revision,generation,key_version,nonce,sealed_session,expires_at) VALUES($1,1,$2,1,decode(repeat('11',12),'hex'),decode(repeat('22',32),'hex'),now()+interval '1 day')`, f.mother, generation)
	exec(`INSERT INTO tsw_mother_discoveries(mother_account_id,run_id,secret_revision,session_generation,status) VALUES($1,$2,1,$3,'discovered')`, f.mother, run, generation)
	exec(`INSERT INTO tsw_mother_workspace_visibility(mother_account_id,workspace_id,run_id,access_status) VALUES($1,$2,$3,'readable')`, f.mother, f.space, run)
	exec(`INSERT INTO tsw_selected_workspace_tokens(mother_account_id,workspace_id,discovery_run_id,session_generation,secret_revision,attempt,exchange_id,status,key_version,nonce,sealed_access,expires_at) VALUES($1,$2,$3,$4,1,1,$5,'ready',$6,$7,$8,$9)`, f.mother, f.space, run, generation, exchange, key, nonce, sealed, access.ExpiresAt)
	exec(`INSERT INTO tsw_workspace_projections(workspace_id) VALUES($1)`, f.space)
	exec(`UPDATE tsw_batches SET planned_at=now()-interval '1 hour',service_started_at=now() WHERE id=$1`, f.batch)
	until := time.Now().Add(24 * time.Hour)
	seats, members, invites := 2, 2, 0
	facts := platform.SelectedWorkspaceFacts{Permission: "read", Result: platform.Result{Outcome: platform.OutcomeOperational, Completeness: platform.Complete, ObservedAt: time.Now().UTC(), ActiveUntil: &until, SeatLimit: &seats, MemberCount: &members, PendingInviteCount: &invites, SubscriptionStatus: "active", Members: []platform.Member{
		{Kind: "member", Identifier: "owner@example.test", PlatformMemberID: "owner-live", Role: "account-owner", Status: "active"},
		{Kind: "member", Identifier: "target@example.com", PlatformMemberID: "target-live", Role: "standard-user", Status: "active"},
	}}}
	h.selectedWorkspaceReader = &selectedFixture{facts: facts}
	response := httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{"motherAccountId": f.mother, "confirmed": true})
	request := httptest.NewRequest(http.MethodPost, "/verification", bytes.NewReader(raw))
	request.Header.Set("Origin", "https://owner.test")
	request.Header.Set(auth.CSRFHeaderName, csrf)
	request.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	h.VerifySelectedWorkspace(response, request, f.space, ownerapi.VerifySelectedWorkspaceParams{})
	if response.Code != 200 {
		t.Fatalf("verification failed: %d %s", response.Code, response.Body.String())
	}
	return f
}

func (f selectedRemovalFixture) preview(t *testing.T) ownerapi.RemovalPreview {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/remove-preview", nil)
	request.SetPathValue("batchId", f.batch.String())
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.session})
	response := httptest.NewRecorder()
	f.h.getBatchRemovalPreview(response, request, ownerapi.GetBatchRemovalPreviewParams{})
	if response.Code != 200 {
		t.Fatalf("preview status=%d body=%s", response.Code, response.Body.String())
	}
	var preview ownerapi.RemovalPreview
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	return preview
}

func TestSelectedVerificationFeedsBatchRemovalPreview(t *testing.T) {
	f := newSelectedRemovalFixture(t)
	preview := f.preview(t)
	if !preview.CanProceed || preview.SnapshotCompleteness != "complete" || len(preview.Targets) != 1 || preview.Targets[0].State != ownerapi.Present {
		t.Fatalf("selected complete Owner roster did not reach removal preview: completeness=%s blockers=%+v targets=%+v", preview.SnapshotCompleteness, preview.Blockers, preview.Targets)
	}
	var operations int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM tsw_operations WHERE operation_type='remove'`).Scan(&operations); err != nil || operations != 0 {
		t.Fatal("preview created a removal task")
	}
	for _, change := range []struct {
		name      string
		facts     map[string]any
		ownerRole string
		revoke    bool
	}{
		{name: "partial", facts: map[string]any{"outcome": "partial", "completeness": "partial", "active_until": nil, "seat_limit": nil, "member_count": nil, "pending_invite_count": nil, "subscription_status": "unknown"}},
		{name: "expired", facts: map[string]any{"observed_at": time.Now().Add(-8 * 24 * time.Hour), "expires_at": time.Now().Add(-2 * 24 * time.Hour)}},
		{name: "owner_missing", ownerRole: "standard-user"},
		{name: "count_mismatch", facts: map[string]any{"member_count": 3}},
		{name: "pre_join_snapshot", facts: map[string]any{"observed_at": time.Now().Add(-time.Hour), "expires_at": time.Now().Add(time.Hour)}},
		{name: "access_revoked", revoke: true},
	} {
		t.Run(change.name, func(t *testing.T) {
			f := newSelectedRemovalFixture(t)
			if change.revoke {
				if _, err := f.pool.Exec(context.Background(), `DELETE FROM tsw_selected_workspace_tokens`); err != nil {
					t.Fatal(err)
				}
			} else {
				if change.facts == nil {
					change.facts = map[string]any{}
				}
				raw, _ := json.Marshal(change.facts)
				var oldID, newID int64
				if err := f.pool.QueryRow(context.Background(), `SELECT max(id) FROM tsw_workspace_verifications`).Scan(&oldID); err != nil {
					t.Fatal(err)
				}
				err := f.pool.QueryRow(context.Background(), `INSERT INTO tsw_workspace_verifications OVERRIDING SYSTEM VALUE SELECT (jsonb_populate_record(NULL::tsw_workspace_verifications,to_jsonb(f)||$1::jsonb||jsonb_build_object('id',nextval(pg_get_serial_sequence('tsw_workspace_verifications','id'))))).* FROM tsw_workspace_verifications f WHERE id=$2 RETURNING id`, raw, oldID).Scan(&newID)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.pool.Exec(context.Background(), `INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,role,platform_member_id,seat_type) SELECT $1,kind,identifier,identifier_hmac,identifier_key_version,status,CASE WHEN identifier='owner@example.test' AND $3<>'' THEN $3 ELSE role END,platform_member_id,seat_type FROM tsw_workspace_verification_entries WHERE verification_id=$2`, newID, oldID, change.ownerRole)
				if err != nil {
					t.Fatal(err)
				}
			}
			if preview := f.preview(t); preview.CanProceed {
				t.Fatalf("unsafe facts admitted removal: %s", change.name)
			}
		})
	}
}

func TestSelectedBatchRemovalWorkerUsesScopedBearerAndLiveRoster(t *testing.T) {
	f := newSelectedRemovalFixture(t)
	ctx := context.Background()
	if err := mothersecret.Migrate(ctx, f.pool, f.h.keyRing); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE tsw_batch_memberships SET platform_member_id='target-live',seat_type='prolite'; UPDATE tsw_operation_targets SET outcome_code='member_confirmed' WHERE status='succeeded'`); err != nil {
		t.Fatal(err)
	}
	removed, deletes := false, 0
	client := &http.Client{Timeout: time.Second, Transport: selectedRoundTrip(func(r *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || r.Header.Get("Cookie") != "" {
			t.Fatal("removal lost scoped bearer identity")
		}
		if _, _, basic := r.BasicAuth(); basic {
			t.Fatal("removal used password instead of exchanged bearer")
		}
		body := `{"total":2,"offset":0,"limit":100,"items":[{"id":"owner-live","email":"owner@example.test","role":"account-owner","seat_type":"default"},{"id":"target-live","role":"standard-user","seat_type":"prolite"}]}`
		status := 200
		if r.Method == http.MethodDelete {
			if r.Header.Get("Chatgpt-Account-Id") != "workspace-card" {
				t.Fatal("delete scope header missing")
			}
			if r.URL.Path != "/backend-api/accounts/workspace-card/users/target-live" {
				t.Fatal("removal escaped the frozen target")
			}
			removed = true
			deletes++
			status = 204
			body = ""
		} else if r.Method != http.MethodGet {
			t.Fatal("unexpected removal protocol request")
		}
		if r.Method == http.MethodGet {
			switch r.URL.Path {
			case "/backend-api/accounts/workspace-card/users":
				if removed {
					body = `{"total":1,"offset":0,"limit":100,"items":[{"id":"owner-live","email":"owner@example.test","role":"account-owner","seat_type":"default"}]}`
				}
			case "/backend-api/subscriptions":
				body = `{"active_until":"2027-11-02T00:00:00Z","is_active":true,"is_delinquent":false,"seats_in_use":2,"seats_entitled":2,"seat_capacity":[{"type":"default","paid":1},{"type":"prolite","paid":1}]}`
			case "/backend-api/accounts/workspace-card/users/seat_type_counts":
				occupied := 1
				if removed {
					occupied = 0
				}
				raw, _ := json.Marshal(map[string]any{"seat_type_counts": map[string]int{"default": 1, "usage_based": 0, "automation": 0, "prolite": occupied}})
				body = string(raw)
			case "/backend-api/accounts/workspace-card/invites":
				body = `{"total":0,"offset":0,"limit":100,"items":[]}`
			default:
				t.Fatal("unexpected removal protocol request")
			}
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	f.h.workspaceTasks = task.NewStore(f.pool, f.h.keyRing)
	request := httptest.NewRequest(http.MethodPost, "/remove", strings.NewReader(`{"confirm":true,"idempotencyKey":"selected-removal-regression","concurrency":1}`))
	request.SetPathValue("batchId", f.batch.String())
	request.Header.Set("Origin", "https://owner.test")
	request.Header.Set(auth.CSRFHeaderName, f.csrf)
	request.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: f.csrf})
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.session})
	response := httptest.NewRecorder()
	f.h.createRemovalOperation(response, request)
	if response.Code != 202 {
		t.Fatalf("authorization rejected: %d %s", response.Code, response.Body.String())
	}
	router, err := egress.New(egress.Config{Mode: egress.ModeDirect})
	if err != nil {
		t.Fatal(err)
	}
	defer router.CloseIdleConnections()
	leases, err := egress.NewLeaseManager(router, egress.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	worker := &task.Worker{Store: f.h.workspaceTasks, Facts: workspace.NewService(f.pool, f.h.keyRing), Egress: leases, ID: "selected-removal-test", LeaseTime: time.Minute, Remover: func(ctx context.Context, _ *http.Client, target task.RemovalTarget) (platform.Remover, error) {
		return f.h.operationRemover(ctx, client, target)
	}}
	if worked, err := worker.RunOnce(ctx); !worked || err != nil {
		t.Fatalf("scoped removal failed: worked=%v err=%v", worked, err)
	}
	var state, batchState string
	if err := f.pool.QueryRow(ctx, `SELECT m.state,b.status FROM tsw_batch_memberships m JOIN tsw_batches b ON b.id=m.batch_id WHERE b.id=$1`, f.batch).Scan(&state, &batchState); err != nil {
		t.Fatal(err)
	}
	if deletes != 1 || state != "removed" || batchState != "ended" {
		t.Fatalf("removal was not verified: deletes=%d state=%s batch=%s", deletes, state, batchState)
	}
	request = httptest.NewRequest(http.MethodGet, "/verification", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.session})
	response = httptest.NewRecorder()
	f.h.GetSelectedWorkspaceVerification(response, request, f.space, ownerapi.GetSelectedWorkspaceVerificationParams{MotherAccountId: f.mother})
	var verification ownerapi.SelectedWorkspaceVerification
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &verification) != nil || verification.Status != "verified" || verification.MemberCount == nil || *verification.MemberCount != 1 || len(verification.Members) != 1 || verification.SeatTypeCounts == nil || (*verification.SeatTypeCounts)["prolite"] != 0 {
		t.Fatalf("clear left stale workspace roster/capacity: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSelectedBatchRemovalAdapterRejectsChangedScope(t *testing.T) {
	f := newSelectedRemovalFixture(t)
	calls := 0
	client := &http.Client{Timeout: time.Second, Transport: selectedRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		t.Fatal("unauthorized scope sent a platform request")
		return nil, nil
	})}
	target := task.RemovalTarget{BatchID: f.batch.String(), MotherID: f.mother.String(), WorkspaceID: f.space.String(), PlatformWorkspace: "workspace-card"}
	remover, err := f.h.operationRemover(context.Background(), client, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remover.SnapshotMembers(context.Background(), "another-space"); err == nil {
		t.Fatal("cross-workspace snapshot accepted")
	}
	if _, err := remover.RemoveMember(context.Background(), "another-space", "owner-live"); err == nil {
		t.Fatal("cross-workspace deletion accepted")
	}
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM tsw_selected_workspace_tokens`); err != nil {
		t.Fatal(err)
	}
	if _, err := remover.RemoveMember(context.Background(), "workspace-card", "target-live"); err == nil || calls != 0 {
		t.Fatal("revoked scope dispatched deletion")
	}
}
