//go:build integration

package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	oauthdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/oauth"
)

func TestPublicFirstClaimWaitsForMaterialRepairButOriginalRestoreAndDownloadSurvive(t *testing.T) {
	pool, _, _, _, _ := childReviewFixture(t)
	ctx := context.Background()
	var membershipID string
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_batch_memberships LIMIT 1`).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}
	secret := "TSW1-" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, 20))
	keyVersion, lookup, err := oauthdomain.LookupHMAC(cardIntegrationKeyRing{}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_cards(id,membership_id,hmac_key_version,lookup_hmac,display_suffix,redemption_deadline) VALUES($1,$2,$3,$4,'55AAAAAA',now()+interval '1 day')`, uuid.NewString(), membershipID, keyVersion, lookup[:]); err != nil {
		t.Fatal(err)
	}
	handler := NewPublicRedeemHandler(pool, cardIntegrationKeyRing{}, nil)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE tsw_target_credentials SET material_status='needs_totp',version=version+1 WHERE target_account_id=(SELECT target_account_id FROM tsw_batch_memberships WHERE id=$1)`, membershipID); err != nil {
		t.Fatal(err)
	}
	var started sync.WaitGroup
	started.Add(1)
	finished := make(chan *httpResponse, 1)
	go func() {
		started.Done()
		response := publicRedeemRequest(t, handler, http.MethodPost, "/api/public/v1/redeem/confirm", map[string]any{"cardSecret": secret}, nil)
		finished <- &httpResponse{code: response.Code, body: response.Body.String()}
	}()
	started.Wait()
	select {
	case response := <-finished:
		t.Fatalf("claim raced pending material update: %d %s", response.code, response.body)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-finished:
		if response.code != 404 {
			t.Fatalf("claim with missing 2FA=%d %s", response.code, response.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("claim did not release after material update")
	}
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_credentials SET material_status='complete',version=version+1 WHERE target_account_id=(SELECT target_account_id FROM tsw_batch_memberships WHERE id=$1)`, membershipID); err != nil {
		t.Fatal(err)
	}
	claimed := publicRedeemRequest(t, handler, http.MethodPost, "/api/public/v1/redeem/confirm", map[string]any{"cardSecret": secret}, nil)
	if claimed.Code != 200 {
		t.Fatalf("claim after repair=%d %s", claimed.Code, claimed.Body.String())
	}
	cookie := claimed.Result().Cookies()[0]
	original := publicRedeemRequest(t, handler, http.MethodPost, "/api/public/v1/redeem/download", nil, cookie)
	if original.Code != http.StatusOK || !bytes.Contains(accountCardZIPPayload(t, original), []byte("original-refresh")) {
		t.Fatalf("original account delivery=%d", original.Code)
	}
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_credentials SET material_status='needs_totp',version=version+1 WHERE target_account_id=(SELECT target_account_id FROM tsw_batch_memberships WHERE id=$1)`, membershipID); err != nil {
		t.Fatal(err)
	}
	restored := publicRedeemRequest(t, handler, http.MethodPost, "/api/public/v1/redeem/confirm", map[string]any{"cardSecret": secret}, nil)
	if restored.Code != 200 {
		t.Fatalf("existing customer's immutable order restore=%d %s", restored.Code, restored.Body.String())
	}
	for _, accessCookie := range []*http.Cookie{cookie, restored.Result().Cookies()[0]} {
		downloaded := publicRedeemRequest(t, handler, http.MethodPost, "/api/public/v1/redeem/download", nil, accessCookie)
		if downloaded.Code != http.StatusOK || !bytes.Equal(downloaded.Body.Bytes(), original.Body.Bytes()) {
			t.Fatalf("immutable download after 2FA changed=%d", downloaded.Code)
		}
	}
}

type httpResponse struct {
	code int
	body string
}

func TestPublicClaimDoesNotInvertActivationMembershipLocks(t *testing.T) {
	pool, owner, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	var membershipID string
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_batch_memberships LIMIT 1`).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}
	secret := "TSW1-" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x53}, 20))
	if response := activateCardIntegrationRequest(t, owner, membershipID, session, csrf, secret, "membership-lock"); response.Code != http.StatusCreated {
		t.Fatalf("activate card=%d %s", response.Code, response.Body.String())
	}
	public := NewPublicRedeemHandler(pool, cardIntegrationKeyRing{}, nil)
	cardTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cardTx.Rollback(ctx)
	var cardID string
	if err = cardTx.QueryRow(ctx, `SELECT id::text FROM tsw_cards WHERE membership_id=$1::uuid FOR UPDATE`, membershipID).Scan(&cardID); err != nil {
		t.Fatal(err)
	}
	claimDone := make(chan httpResponse, 1)
	go func() {
		response := publicRedeemRequest(t, public, http.MethodPost, "/api/public/v1/redeem/confirm", map[string]any{"cardSecret": secret}, nil)
		claimDone <- httpResponse{response.Code, response.Body.String()}
	}()
	waitForReviewLock(t, pool, `%FROM tsw_cards WHERE id=%FOR UPDATE%`)
	ownerDone := make(chan httpResponse, 1)
	go func() {
		response := activateCardIntegrationRequest(t, owner, membershipID, session, csrf, secret, "membership-lock-repeat")
		ownerDone <- httpResponse{response.Code, response.Body.String()}
	}()
	waitForReviewLock(t, pool, `%FOR UPDATE OF membership,batch,asset,credential%`)
	if err = cardTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, resultCh := range []<-chan httpResponse{claimDone, ownerDone} {
		select {
		case response := <-resultCh:
			if response.code != 200 {
				t.Fatalf("activation/claim lock inversion: %d %s", response.code, response.body)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("activation or claim deadlocked")
		}
	}
}

func waitForReviewLock(t *testing.T, pool *pgxpool.Pool, pattern string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1`, pattern).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no blocked lock query matching %s", pattern)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOwnerCardActivationAndPublicFirstClaimKeepCredentialBeforeCardLockOrder(t *testing.T) {
	pool, owner, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	var membershipID string
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_batch_memberships LIMIT 1`).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}
	secret := "TSW1-" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x52}, 20))
	if response := activateCardIntegrationRequest(t, owner, membershipID, session, csrf, secret, "material-lock-order"); response.Code != http.StatusCreated {
		t.Fatalf("activate card=%d %s", response.Code, response.Body.String())
	}
	public := NewPublicRedeemHandler(pool, cardIntegrationKeyRing{}, nil)
	// Hold precisely the membership/batch/asset/credential locks acquired by
	// Owner activation before its idempotent card FOR UPDATE. The public claim
	// must wait on the same lock group without holding the card.
	activationTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer activationTx.Rollback(ctx)
	var lockedID string
	err = activationTx.QueryRow(ctx, `SELECT membership.id::text FROM tsw_batch_memberships membership
		JOIN tsw_batches batch ON batch.id=membership.batch_id
		JOIN tsw_mother_workspace_bindings binding ON binding.id=batch.binding_id
		JOIN tsw_oauth_assets asset ON asset.membership_id=membership.id
		JOIN tsw_target_credentials credential ON credential.target_account_id=membership.target_account_id
		WHERE membership.id=$1::uuid AND membership.state='active'
		FOR UPDATE OF membership,batch,asset,credential`, membershipID).Scan(&lockedID)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan httpResponse, 1)
	go func() {
		response := publicRedeemRequest(t, public, http.MethodPost, "/api/public/v1/redeem/confirm", map[string]any{"cardSecret": secret}, nil)
		finished <- httpResponse{code: response.Code, body: response.Body.String()}
	}()
	// Wait for the actual claim to block on the activation lock group rather
	// than relying on scheduler timing. With the old card-first order it holds card.
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiters int
		err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname=current_database() AND wait_event_type='Lock'
			AND (query LIKE '%FOR SHARE OF credential%' OR query LIKE '%FOR SHARE OF membership,batch,asset,credential%')`).Scan(&waiters)
		if err != nil {
			t.Fatal(err)
		}
		if waiters > 0 {
			break
		}
		select {
		case response := <-finished:
			t.Fatalf("claim escaped activation credential lock: %d %s", response.code, response.body)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("claim never attempted the activation lock group")
		}
		time.Sleep(10 * time.Millisecond)
	}
	lockCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	err = activationTx.QueryRow(lockCtx, `SELECT id::text FROM tsw_cards WHERE membership_id=$1::uuid FOR UPDATE`, membershipID).Scan(&lockedID)
	if err != nil {
		_ = activationTx.Rollback(ctx)
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
		}
		t.Fatalf("Owner idempotent activation blocked on a card held by a claim waiting for credentials: %v", err)
	}
	if err = activationTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-finished:
		if result.code != http.StatusOK {
			t.Fatalf("public first claim=%d %s", result.code, result.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("public claim did not finish after activation commit")
	}
	// The real Owner idempotent endpoint still returns the existing card.
	if response := activateCardIntegrationRequest(t, owner, membershipID, session, csrf, secret, "material-lock-order-repeat"); response.Code != http.StatusOK {
		t.Fatalf("idempotent Owner activation=%d %s", response.Code, response.Body.String())
	}
	var orderCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM tsw_orders WHERE membership_id=$1::uuid`, membershipID).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if orderCount != 1 {
		t.Fatalf("orders=%d want one original order", orderCount)
	}
}
