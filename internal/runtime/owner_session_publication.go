package runtime

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var errPersonalOwnerSession = errors.New("owner session changed before account publication")

// Business login does not change Owner permissions or rotate its cookie. Lock
// the authenticated session through publication so logout and password changes
// still prevent an in-flight login from publishing credentials.
func (h *OwnerAuthHandler) lockCurrentOwnerSession(ctx context.Context, tx pgx.Tx, owner ownerContext) error {
	var sessionID string
	err := tx.QueryRow(ctx, `SELECT session.id FROM tsw_owner_sessions session JOIN tsw_owners owner ON owner.id=session.owner_id WHERE session.id=$1 AND session.owner_id=$2 AND session.auth_version=owner.auth_version AND session.auth_version=$3 AND session.revoked_at IS NULL AND session.idle_expires_at>now() AND session.absolute_expires_at>now() FOR SHARE OF session,owner`, owner.SessionID, owner.OwnerID, owner.AuthVersion).Scan(&sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errPersonalOwnerSession
	}
	return err
}
