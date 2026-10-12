package platform

import (
	"context"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"time"
)

// PersonalSessionRenewer checks saved browser cookies without submitting login materials.
// invalid_login is returned only when the platform confirms that session is gone.
type PersonalSessionRenewer interface {
	RenewPersonal(context.Context, PersonalSession, browserenv.Environment) (PersonalRefreshResult, error)
}

// AcquirePersonal is the common login policy for explicit account actions.
// Transport failures never cause another password/MFA attempt.
func AcquirePersonal(ctx context.Context, adapter PersonalSessionRefresher, material MotherMaterial, saved PersonalSession) (PersonalRefreshResult, error) {
	if ValidatePersonalRefresh(PersonalRefreshResult{Status: "ready", Session: saved}, time.Now()) {
		return PersonalRefreshResult{Status: "ready", Session: saved}, nil
	}
	if validPersonalCookies(saved) && (saved.SessionExpiresAt.IsZero() || saved.SessionExpiresAt.After(time.Now())) {
		renewer, ok := adapter.(PersonalSessionRenewer)
		if !ok {
			return PersonalRefreshResult{Status: "refresh_failed"}, ErrPersonalRefreshUnavailable
		}
		result, err := renewer.RenewPersonal(ctx, saved, material.Environment)
		if err != nil || result.Status != "invalid_login" {
			return result, err
		}
		if !ValidatePersonalRefresh(result, time.Now()) {
			return PersonalRefreshResult{Status: "refresh_failed"}, nil
		}
	}
	return adapter.RefreshPersonal(ctx, material)
}
