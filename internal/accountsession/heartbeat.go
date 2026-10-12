package accountsession

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// KeepVerifying keeps the existing reservation live while capacity is queued or
// its bounded browser executes. A crash stops renewal and permits stale recovery.
// The revision/attempt predicate never revives a superseded attempt.
func KeepVerifying(ctx context.Context, pool *pgxpool.Pool, kind string, id uuid.UUID, revision, attempt int64) func() {
	query := `UPDATE tsw_target_personal_access SET checked_at=now() WHERE target_account_id=$1 AND secret_revision=$2 AND attempt=$3 AND status='verifying'`
	if kind == "mother" {
		query = `UPDATE tsw_mother_personal_access SET checked_at=now() WHERE mother_account_id=$1 AND secret_revision=$2 AND attempt=$3 AND status='verifying'`
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				write, stop := context.WithTimeout(ctx, 5*time.Second)
				result, err := pool.Exec(write, query, id, revision, attempt)
				stop()
				if err == nil && result.RowsAffected() == 0 {
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}
