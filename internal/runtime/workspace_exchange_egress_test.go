package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func TestWorkspaceExchangeAcquisitionFailureBeforePlatformRequest(t *testing.T) {
	for _, scenario := range []struct {
		name, code string
		mode       egress.Mode
		occupied   bool
		wait       bool
	}{
		{"proxy pool empty", "proxy_capacity_exhausted", egress.ModeRequired, false, false},
		{"direct capacity wait without maintainer", "connection_acquisition_timeout", egress.ModeDirect, true, false},
		{"direct capacity wait timeout", "connection_acquisition_timeout", egress.ModeDirect, true, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				t.Error("acquisition failure must not send a platform request")
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			router, err := egress.NewManaged(server.URL, server.URL, []byte("workspace-route-test-key"), "1")
			if err != nil {
				t.Fatal(err)
			}
			defer router.CloseIdleConnections()
			pool, err := egress.NewLeaseManager(router, egress.Admission{})
			if err != nil {
				t.Fatal(err)
			}
			manager := egress.NewManager(pool, router.Status(egress.Admission{}))
			manager.SetMode(scenario.mode)
			manager.SetConcurrency(1)
			if scenario.occupied {
				lease, err := manager.Acquire(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Release()
			}
			ctx := t.Context()
			if scenario.occupied {
				if scenario.wait {
					manager.SetRefillSignal(func() {})
				}
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			access, err := (leasedWorkspaceTokenExchanger{egress: manager}).ExchangeWorkspace(ctx, platform.PersonalSession{}, "chosen-team")
			failure := platform.WorkspaceExchangeDiagnostic(err)
			if failure == nil || failure.Code != scenario.code || failure.RouteMode != string(scenario.mode) || failure.ProxyScheme != "" {
				t.Fatalf("wrong pre-request diagnostic: %+v", failure)
			}
			if requests.Load() != 0 || access.AccessToken != "" {
				t.Fatal("failed acquisition issued a request or retained credentials")
			}
		})
	}
}
