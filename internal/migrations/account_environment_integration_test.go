//go:build integration

package migrations

import (
	"database/sql"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestAccountEnvironmentUpgradePreservesSavedConcurrencyAndCreatesNoProfiles(t *testing.T) {
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated database required")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || !strings.HasPrefix(u.Path, "/tsw_") {
		t.Fatal("requires disposable loopback fixture database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	if _, err = db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
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
	if _, err = provider.UpTo(ctx, 59); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE tsw_proxy_pool_settings SET task_concurrency=3`); err != nil {
		t.Fatal(err)
	}
	if err = Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	var saved, count int
	if err = db.QueryRowContext(ctx, `SELECT task_concurrency,(SELECT count(*) FROM tsw_account_browser_environments) FROM tsw_proxy_pool_settings`).Scan(&saved, &count); err != nil || saved != 3 || count != 0 {
		t.Fatal("upgrade changed saved parameters or created profiles", saved, count, err)
	}
	if _, err = db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT task_concurrency FROM tsw_proxy_pool_settings`).Scan(&saved); err != nil || saved != 5 {
		t.Fatal("fresh install default is not five", saved, err)
	}
}
