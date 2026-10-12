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
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/workspace"
)

func manualRemovalRequest(t *testing.T, f selectedRemovalFixture, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://owner.test")
	r.Header.Set(auth.CSRFHeaderName, f.csrf)
	r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: f.csrf})
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.session})
	w := httptest.NewRecorder()
	ownerapi.Handler(f.h).ServeHTTP(w, r)
	return w
}

func seedSecondRemovalMember(t *testing.T, f selectedRemovalFixture) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := t.Context()
	var membership uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT id FROM tsw_batch_memberships WHERE batch_id=$1`, f.batch).Scan(&membership); err != nil {
		t.Fatal(err)
	}
	account, second, join := uuid.New(), uuid.New(), uuid.New()
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'second@example.test',decode(repeat('97',32),'hex'),1,'second')`, []any{account}},
		{`INSERT INTO tsw_operation_targets(id,operation_id,target_account_id,ordinal,status,completed_at) SELECT $1,operation_id,$2,2,'succeeded',now() FROM tsw_operation_targets WHERE membership_id=$3`, []any{join, account, membership}},
		{`INSERT INTO tsw_batch_memberships(id,batch_id,target_account_id,join_operation_target_id,joined_at) VALUES($1,$2,$3,$4,now()-interval '1 hour')`, []any{second, f.batch, account, join}},
	} {
		if _, err := f.pool.Exec(ctx, seed.query, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	return membership, second
}

func TestOwnerCanManuallyRemoveOnlySelectedBatchAccount(t *testing.T) {
	f := newSelectedRemovalFixture(t)
	membership, second := seedSecondRemovalMember(t, f)
	path := "/api/owner/v1/batches/" + f.batch.String() + "/remove"
	w := manualRemovalRequest(t, f, http.MethodPost, path, map[string]any{
		"confirm": true, "manual": true, "membershipIds": []uuid.UUID{membership}, "idempotencyKey": "manual-one-account",
	})
	var operation ownerapi.RemovalOperation
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &operation) != nil || operation.TargetTotal != 1 || len(operation.Targets) != 1 || operation.Targets[0].MembershipId != membership {
		t.Fatalf("selected account authorization status=%d body=%s", w.Code, w.Body.String())
	}
	conflict := manualRemovalRequest(t, f, http.MethodPost, path, map[string]any{
		"confirm": true, "manual": true, "membershipIds": []uuid.UUID{second}, "idempotencyKey": "manual-one-account",
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed account replay status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestManualRemovalCanFinishAcrossOperationsAndStartNextBatch(t *testing.T) {
	f := newSelectedRemovalFixture(t)
	first, _ := seedSecondRemovalMember(t, f)
	if _, err := f.pool.Exec(t.Context(), `UPDATE tsw_batches SET planned_at=now()+interval '2 days' WHERE id=$1`, f.batch); err != nil {
		t.Fatal(err)
	}
	members := []map[string]string{
		{"id": "owner-live", "email": "owner@example.test", "role": "account-owner", "seat_type": "default"},
		{"id": "first-live", "email": "target@example.com", "role": "standard-user", "seat_type": "prolite"},
		{"id": "second-live", "email": "second@example.test", "role": "standard-user", "seat_type": "prolite"},
	}
	client := &http.Client{Timeout: time.Second, Transport: selectedRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			for i, member := range members {
				if r.URL.Path == "/backend-api/accounts/workspace-card/users/"+member["id"] && member["role"] != "account-owner" {
					members = append(members[:i], members[i+1:]...)
					return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody}, nil
				}
			}
			t.Fatalf("unexpected removal: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected platform request: %s %s", r.Method, r.URL.Path)
		}
		var raw []byte
		switch r.URL.Path {
		case "/backend-api/accounts/workspace-card/users":
			raw, _ = json.Marshal(map[string]any{"total": len(members), "offset": 0, "limit": 100, "items": members})
		case "/backend-api/subscriptions":
			raw, _ = json.Marshal(map[string]any{"active_until": time.Now().Add(24 * time.Hour), "is_active": true, "is_delinquent": false, "seats_in_use": len(members), "seats_entitled": 3, "seat_capacity": []map[string]any{{"type": "default", "paid": 1}, {"type": "prolite", "paid": 2}}})
		case "/backend-api/accounts/workspace-card/users/seat_type_counts":
			raw, _ = json.Marshal(map[string]any{"seat_type_counts": map[string]int{"default": 1, "usage_based": 0, "automation": 0, "prolite": len(members) - 1}})
		case "/backend-api/accounts/workspace-card/invites":
			raw = []byte(`{"total":0,"offset":0,"limit":100,"items":[]}`)
		default:
			t.Fatalf("unexpected platform request: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	remover, err := f.h.operationRemover(t.Context(), client, task.RemovalTarget{BatchID: f.batch.String(), MotherID: f.mother.String(), WorkspaceID: f.space.String(), PlatformWorkspace: "workspace-card"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remover.SnapshotMembers(t.Context(), "workspace-card"); err != nil {
		t.Fatal(err)
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
	f.h.workspaceTasks = task.NewStore(f.pool, f.h.keyRing)
	worker := &task.Worker{Store: f.h.workspaceTasks, Facts: workspace.NewService(f.pool, f.h.keyRing), Egress: leases, ID: "manual-removal", LeaseTime: time.Minute,
		Remover: func(ctx context.Context, _ *http.Client, target task.RemovalTarget) (platform.Remover, error) {
			return f.h.operationRemover(ctx, client, target)
		},
	}
	path := "/api/owner/v1/batches/" + f.batch.String()
	for i, body := range []map[string]any{
		{"confirm": true, "manual": true, "membershipIds": []uuid.UUID{first}, "idempotencyKey": "manual-split-first"},
		{"confirm": true, "manual": true, "idempotencyKey": "manual-split-remaining"},
	} {
		w := manualRemovalRequest(t, f, http.MethodPost, path+"/remove", body)
		var operation ownerapi.RemovalOperation
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &operation) != nil || operation.TargetTotal != 1 {
			t.Fatalf("split authorization status=%d body=%s", w.Code, w.Body.String())
		}
		if worked, err := worker.RunOnce(t.Context()); !worked || err != nil {
			t.Fatalf("split execution worked=%v err=%v", worked, err)
		}
		preview := f.preview(t)
		if preview.Batch.Rotation.JoinedCount != 2 || preview.Batch.Rotation.RemovedCount != i+1 {
			t.Fatalf("split counts: %+v", preview.Batch.Rotation)
		}
		if i == 0 && (preview.Batch.Status != "serving" || preview.Batch.Rotation.State != "not_due") {
			t.Fatalf("single removal did not leave the batch serving: %+v", preview.Batch)
		}
		if i == 1 && (preview.Batch.Status != "ended" || preview.Batch.Rotation.State != "completed") {
			t.Fatalf("split removal did not finish the batch: %+v", preview.Batch)
		}
		replay := manualRemovalRequest(t, f, http.MethodPost, path+"/remove", body)
		var original ownerapi.RemovalOperation
		if replay.Code != http.StatusAccepted || json.Unmarshal(replay.Body.Bytes(), &original) != nil || original.Id != operation.Id {
			t.Fatalf("completed replay status=%d body=%s", replay.Code, replay.Body.String())
		}
	}
	w := manualRemovalRequest(t, f, http.MethodDelete, "/api/owner/v1/operation-draft?previous_batch_id="+f.batch.String(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("next batch admission status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestManualAccountRemovalRejectsNullScope(t *testing.T) {
	f := newSelectedRemovalFixture(t)
	w := manualRemovalRequest(t, f, http.MethodPost, "/api/owner/v1/batches/"+f.batch.String()+"/remove", map[string]any{
		"confirm": true, "manual": true, "membershipIds": nil, "idempotencyKey": "manual-null-account-scope",
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("null account scope widened to whole batch: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestOwnerCanManuallyRemoveBatchBeforePlannedTime(t *testing.T) {
	f := newSelectedRemovalFixture(t)
	if _, err := f.pool.Exec(t.Context(), `UPDATE tsw_batches SET planned_at=now()+interval '2 days' WHERE id=$1`, f.batch); err != nil {
		t.Fatal(err)
	}
	path := "/api/owner/v1/batches/" + f.batch.String()
	for _, mode := range []struct {
		query      string
		canProceed bool
	}{
		{query: "", canProceed: false},
		{query: "?manual=true", canProceed: true},
	} {
		w := manualRemovalRequest(t, f, http.MethodGet, path+"/remove-preview"+mode.query, nil)
		var preview ownerapi.RemovalPreview
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.CanProceed != mode.canProceed {
			t.Fatalf("preview %s status=%d body=%s", mode.query, w.Code, w.Body.String())
		}
	}
	w := manualRemovalRequest(t, f, http.MethodPost, path+"/remove", map[string]any{
		"confirm": true, "manual": true, "idempotencyKey": "manual-early-batch",
	})
	var operation ownerapi.RemovalOperation
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &operation) != nil || operation.TargetTotal != 1 || operation.Status != "queued" {
		t.Fatalf("manual authorization status=%d body=%s", w.Code, w.Body.String())
	}
	replay := manualRemovalRequest(t, f, http.MethodPost, path+"/remove", map[string]any{
		"confirm": true, "manual": true, "idempotencyKey": "manual-early-batch",
	})
	var original ownerapi.RemovalOperation
	if replay.Code != http.StatusAccepted || json.Unmarshal(replay.Body.Bytes(), &original) != nil || original.Id != operation.Id {
		t.Fatalf("manual replay status=%d body=%s", replay.Code, replay.Body.String())
	}
}

func TestManualRemovalWaitsForBatchJoinToFinish(t *testing.T) {
	f := newSelectedRemovalFixture(t)
	if _, err := f.pool.Exec(t.Context(), `UPDATE tsw_operation_targets SET status='running',completed_at=NULL WHERE operation_id IN (SELECT id FROM tsw_operations WHERE batch_id=$1 AND operation_type='join')`, f.batch); err != nil {
		t.Fatal(err)
	}
	path := "/api/owner/v1/batches/" + f.batch.String()
	w := manualRemovalRequest(t, f, http.MethodGet, path+"/remove-preview?manual=true", nil)
	var preview ownerapi.RemovalPreview
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.CanProceed {
		t.Fatalf("unfinished join admitted removal preview: %d %s", w.Code, w.Body.String())
	}
	w = manualRemovalRequest(t, f, http.MethodPost, path+"/remove", map[string]any{
		"confirm": true, "manual": true, "idempotencyKey": "manual-unfinished-join",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("unfinished join admitted removal: %d %s", w.Code, w.Body.String())
	}
}

func TestManualRemovalAuthorizationLocksAccountsBeforeBatch(t *testing.T) {
	f := newSelectedRemovalFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	accountLock, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer accountLock.Rollback(ctx)
	if _, err := accountLock.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('tsw.rotation.action.target_account/'||target_account_id::text,0)) FROM tsw_batch_memberships WHERE batch_id=$1`, f.batch); err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- manualRemovalRequest(t, f, http.MethodPost, "/api/owner/v1/batches/"+f.batch.String()+"/remove", map[string]any{
			"confirm": true, "manual": true, "idempotencyKey": "manual-authorization-order",
		})
	}()
	for {
		var waiting bool
		if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND pid<>pg_backend_pid())`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	batchLock, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer batchLock.Rollback(ctx)
	_, lockErr := batchLock.Exec(ctx, `SELECT id FROM tsw_batches WHERE id=$1 FOR UPDATE NOWAIT`, f.batch)
	if err := batchLock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := accountLock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	w := <-result
	if lockErr != nil || w.Code != http.StatusAccepted {
		t.Fatalf("authorization held batch while awaiting account fence: lock=%v status=%d body=%s", lockErr, w.Code, w.Body.String())
	}
}
