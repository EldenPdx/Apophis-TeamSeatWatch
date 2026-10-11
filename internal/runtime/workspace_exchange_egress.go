package runtime

import (
	"context"
	"errors"
	"net/http"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

// The diagnostic describes the route held by this attempt, even if the Owner
// changes the routing policy while the platform request is in flight.
type leasedWorkspaceTokenExchanger struct{ egress *egress.Manager }

func (e leasedWorkspaceTokenExchanger) ExchangeWorkspace(ctx context.Context, personal platform.PersonalSession, workspaceID string) (platform.WorkspaceAccess, error) {
	lease, err := e.egress.Acquire(ctx)
	if err != nil {
		failure := platform.WorkspaceExchangeDiagnostic(err)
		failure.RouteMode = string(e.egress.Status().Policy)
		// No workspace request can be sent before a lease is acquired. A
		// shared concurrency limit also applies to direct connections.
		if errors.Is(err, egress.ErrLeaseExhausted) && failure.RouteMode == string(egress.ModeDirect) {
			failure.Code = "connection_capacity_exhausted"
		} else if errors.Is(err, context.DeadlineExceeded) {
			failure.Code = "connection_acquisition_timeout"
		}
		return platform.WorkspaceAccess{}, failure
	}
	defer lease.Release()
	adapter := platform.OfficialWorkspaceTokenExchanger{Client: func(context.Context) (*http.Client, func(), error) {
		return lease.Client(), nil, nil
	}}
	access, err := adapter.ExchangeWorkspace(ctx, personal, workspaceID)
	if err == nil {
		return access, nil
	}
	failure := platform.WorkspaceExchangeDiagnostic(err)
	failure.RouteMode, failure.ProxyScheme = string(lease.Mode()), lease.Scheme()
	return platform.WorkspaceAccess{}, failure
}
