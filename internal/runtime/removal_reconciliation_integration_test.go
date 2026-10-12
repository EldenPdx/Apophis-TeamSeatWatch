//go:build integration

package runtime

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

func TestAcceptedRemovalReconciliationKeepsPollingAndReplaysIdempotently(t *testing.T) {
	f := newSelectedRemovalFixture(t)
	f.h.workspaceTasks = task.NewStore(f.pool, f.h.keyRing)
	path := "/api/owner/v1/batches/" + f.batch.String()
	w := manualRemovalRequest(t, f, http.MethodPost, path+"/remove", map[string]any{"confirm": true, "manual": true, "idempotencyKey": "reconcile-original"})
	var operation ownerapi.RemovalOperation
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &operation) != nil {
		t.Fatalf("remove authorization failed: %d %s", w.Code, w.Body.String())
	}
	for _, query := range []string{
		`UPDATE tsw_operation_targets SET status='unknown',completed_at=now() WHERE operation_id=$1`,
		`UPDATE tsw_operations SET status='blocked',completed_at=now() WHERE id=$1`,
	} {
		if _, err := f.pool.Exec(t.Context(), query, operation.Id); err != nil {
			t.Fatal(err)
		}
	}
	request := map[string]any{"idempotencyKey": "reconcile-replay"}
	w = manualRemovalRequest(t, f, http.MethodPost, path+"/remove-reconcile", request)
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &operation) != nil || operation.Status != "queued" {
		t.Fatalf("accepted reconciliation does not keep polling: %d %s", w.Code, w.Body.String())
	}
	w = manualRemovalRequest(t, f, http.MethodPost, path+"/remove-reconcile", request)
	if w.Code != http.StatusAccepted {
		t.Fatalf("same reconciliation request did not replay: %d %s", w.Code, w.Body.String())
	}
	w = manualRemovalRequest(t, f, http.MethodPost, path+"/remove-reconcile", map[string]any{"idempotencyKey": "reconcile-conflicting"})
	if w.Code != http.StatusConflict {
		t.Fatalf("queued reconciliation accepted redundant request: %d %s", w.Code, w.Body.String())
	}
}

func TestRemovalReconciliationAtMutationLimitStaysBlocked(t *testing.T) {
	f := newSelectedRemovalFixture(t)
	f.h.workspaceTasks = task.NewStore(f.pool, f.h.keyRing)
	path := "/api/owner/v1/batches/" + f.batch.String()
	w := manualRemovalRequest(t, f, http.MethodPost, path+"/remove", map[string]any{"confirm": true, "manual": true, "idempotencyKey": "exhausted-original"})
	var operation ownerapi.RemovalOperation
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &operation) != nil {
		t.Fatalf("remove authorization failed: %d %s", w.Code, w.Body.String())
	}
	for _, query := range []string{
		`UPDATE tsw_operation_targets SET status='unknown',completed_at=now(),remove_attempt_count=3,platform_request_may_have_reached=true,platform_request_stage='remove_member',platform_request_started_at=now() WHERE operation_id=$1`,
		`UPDATE tsw_operations SET status='blocked',completed_at=now() WHERE id=$1`,
		`UPDATE tsw_tasks SET status='failed',finished_at=now() WHERE operation_target_id IN (SELECT id FROM tsw_operation_targets WHERE operation_id=$1)`,
	} {
		if _, err := f.pool.Exec(t.Context(), query, operation.Id); err != nil {
			t.Fatal(err)
		}
	}
	w = manualRemovalRequest(t, f, http.MethodPost, path+"/remove-reconcile", map[string]any{"idempotencyKey": "exhausted-reconcile"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("reconciliation authorization failed: %d %s", w.Code, w.Body.String())
	}
	item, err := f.h.workspaceTasks.ClaimRemovalReconciliation(t.Context(), "exhausted-worker", time.Minute, task.AttemptRoute{Mode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.h.workspaceTasks.RemovalTarget(t.Context(), item)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.h.workspaceTasks.FinishRemovalReconciliation(t.Context(), item, target, nil, true); err != nil {
		t.Fatal(err)
	}
	w = manualRemovalRequest(t, f, http.MethodGet, path+"/remove-operation", nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &operation) != nil || operation.Status != "blocked" || operation.CompletedAt == nil || len(operation.Targets) != 1 || operation.Targets[0].Status != "blocked" || operation.Targets[0].DiagnosticCode == nil || *operation.Targets[0].DiagnosticCode != "remove_attempts_exhausted" {
		t.Fatalf("mutation budget exhaustion left permanent running state: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.h.workspaceTasks.ClaimRemoval(t.Context(), "exhausted-worker", time.Minute, task.AttemptRoute{Mode: "direct"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("exhausted reconciliation scheduled a DELETE: %v", err)
	}
}
