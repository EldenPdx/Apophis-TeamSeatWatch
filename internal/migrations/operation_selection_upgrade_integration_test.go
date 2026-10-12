//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Rehearse upgrading from 24 while preserving the ready selection and preview.
func TestOperationSelectionUpgradeFrom24Integration(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	files, err := fs.Sub(sqlFiles, "sql")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 24); err != nil {
		t.Fatal(err)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil || version != 24 {
		t.Fatalf("before upgrade: version=%d err=%v", version, err)
	}
	owner, mother, workspace, batch, destination := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, fixture := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO tsw_owners(id,username,password_hash,totp_ciphertext,totp_nonce,totp_key_version) VALUES($1,'upgrade-owner','hash',decode(repeat('11',16),'hex'),decode(repeat('22',12),'hex'),1)`, []any{owner}},
		{`INSERT INTO tsw_mother_accounts(id,display_name) VALUES($1,'upgrade-mother')`, []any{mother}},
		{`INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES($1,$2,'upgrade-workspace')`, []any{workspace, workspace.String()}},
		{`INSERT INTO tsw_standby_child_batches(id,name) VALUES($1,'upgrade-batch')`, []any{batch}},
		{`INSERT INTO tsw_delivery_destinations(id,name,endpoint,target_group,secret_key_version,secret_nonce,secret_ciphertext) VALUES($1,'upgrade-destination','https://hub.fixture.test/api/v1','42',1,decode(repeat('33',12),'hex'),decode(repeat('44',32),'hex'))`, []any{destination}},
	} {
		if _, err := db.ExecContext(ctx, fixture.query, fixture.args...); err != nil {
			t.Fatal(err)
		}
	}
	run, generation := uuid.New(), uuid.New()
	var verification int64
	if err := db.QueryRowContext(ctx, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','verifying','unknown','unknown',now(),now()+interval '1 day') RETURNING id`, workspace, mother, run, generation, uuid.New()).Scan(&verification); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 25); err != nil {
		t.Fatal(err)
	}
	version, err = provider.GetDBVersion(ctx)
	if err != nil || version != 25 {
		t.Fatalf("before 25 -> 26 upgrade: version=%d err=%v", version, err)
	}
	var id uuid.UUID
	if err := db.QueryRowContext(ctx, `INSERT INTO tsw_operation_selection_drafts(owner_id,step,mother_account_id,mother_revision,workspace_id,visibility_run_id,session_generation,verification_id,batch_id,batch_version,destination_id,destination_revision) VALUES($1,'complete',$2,1,$3,$4,$5,$6,$7,1,$8,1) RETURNING id`, owner, mother, workspace, run, generation, verification, batch, destination).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 26); err != nil {
		t.Fatal(err)
	}
	var preview uuid.UUID
	if err := db.QueryRowContext(ctx, `INSERT INTO tsw_expiry_rotation_previews(owner_id,draft_id,draft_version,workspace_id,verification_id,facts,digest,status,expires_at) VALUES($1,$2,1,$3,$4,'{"source":"upgrade"}',repeat('a',64),'facts_incomplete',now()+interval '1 hour') RETURNING id`, owner, id, workspace, verification).Scan(&preview); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 28); err != nil {
		t.Fatal(err)
	}
	version, err = provider.GetDBVersion(ctx)
	if err != nil || version != 28 {
		t.Fatalf("after upgrade: version=%d err=%v", version, err)
	}
	if _, err := provider.UpTo(ctx, 28); err != nil {
		t.Fatalf("second upgrade: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='tsw_expiry_rotation_previews'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration 26 preview table missing: count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tsw_operation_selection_drafts WHERE id=$1 AND owner_id=$2 AND mother_account_id=$3 AND workspace_id=$4 AND batch_id=$5 AND destination_id=$6 AND step='complete'`, id, owner, mother, workspace, batch, destination).Scan(&count); err != nil || count != 1 {
		t.Fatalf("idempotent upgrade lost selection dependencies: count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tsw_expiry_rotation_previews WHERE id=$1 AND draft_id=$2 AND facts->>'source'='upgrade' AND epoch_versions IS NULL`, preview, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("26 -> 28 upgrade lost preview or treated legacy authorization as fenced: count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('tsw_rotation_usage_ledger','tsw_rotation_global_protections')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("migration 28 evidence ledgers missing: count=%d err=%v", count, err)
	}
	for _, key := range []struct {
		kind string
		id   uuid.UUID
	}{{"workspace", workspace}, {"mother", mother}, {"standby_batch", batch}, {"destination", destination},
		{"target_identity", uuid.MustParse("00000000-0000-0000-0000-000000000001")},
		{"standby_membership", uuid.MustParse("00000000-0000-0000-0000-000000000002")}} {
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tsw_rotation_epochs WHERE kind=$1 AND id=$2`, key.kind, key.id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing backfilled epoch %s/%s: count=%d err=%v", key.kind, key.id, count, err)
		}
	}
	if _, err := provider.DownTo(ctx, 27); err != nil {
		t.Fatalf("28 -> 27 downgrade: %v", err)
	}
	if _, err := provider.UpTo(ctx, 28); err != nil {
		t.Fatalf("27 -> 28 reapply: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tsw_expiry_rotation_previews WHERE id=$1 AND draft_id=$2`, preview, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("28 downgrade/reapply lost frozen preview: count=%d err=%v", count, err)
	}
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, db); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	version, err = provider.GetDBVersion(ctx)
	if err != nil || version != RequiredVersion {
		t.Fatalf("latest upgrade: version=%d err=%v", version, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tsw_operation_selection_drafts WHERE id=$1 AND owner_id=$2 AND mother_account_id=$3 AND workspace_id=$4 AND batch_id=$5 AND destination_id=$6 AND step='ready'`, id, owner, mother, workspace, batch, destination).Scan(&count); err != nil || count != 1 {
		t.Fatalf("latest upgrade lost ready selection dependencies: count=%d err=%v", count, err)
	}
}
