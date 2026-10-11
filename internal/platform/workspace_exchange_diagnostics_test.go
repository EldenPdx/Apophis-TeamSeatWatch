package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A successful HTTP response can still contain Personal credentials after the
// explicit Team exchange. Preserve that distinction without accepting the token.
func TestWorkspaceExchangeReportsPersonalWorkspaceReturned(t *testing.T) {
	personal := PersonalSession{AccessToken: "personal-token", DeviceID: "saved-device", Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "saved-cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}
	token := workspaceFixtureTokenWithClaims(map[string]any{"scp": []string{"organization.read"}, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "personal-space", "chatgpt_plan_type": "free"}})
	exchanger, _ := exchangeFixture(t, 200, "personal-space", token)
	base, _, _ := exchanger.Client(t.Context())
	next := base.Transport
	base.Transport = exchangeTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == accountsCheckURL {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"accounts":{"team":{"account":{"account_id":"canonical-space","name":"Visible Team","plan_type":"team","structure":"workspace"}}}}`)), Request: req}, nil
		}
		return next.RoundTrip(req)
	})
	access, err := exchanger.ExchangeWorkspace(context.Background(), personal, "canonical-space")
	if err == nil || err.Error() != "workspace_personal_returned" || access.AccessToken != "" {
		t.Fatalf("Personal response must explain failed Team switching; error=%v retained_token=%v", err, access.AccessToken != "")
	}
}

func TestWorkspaceExchangeReportsRevokedLoginBehindPersonalResponse(t *testing.T) {
	personal := PersonalSession{AccessToken: "revoked-personal-token", DeviceID: "saved-device", Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "saved-cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}
	token := workspaceFixtureTokenWithClaims(map[string]any{"scp": []string{"organization.read"}, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "personal-space", "chatgpt_plan_type": "free"}})
	original, _ := exchangeFixture(t, 200, "personal-space", token)
	base, _, _ := original.Client(context.Background())
	next := base.Transport
	checks := 0
	base.Transport = exchangeTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/auth/session" {
			return next.RoundTrip(req)
		}
		if req.URL.String() != accountsCheckURL || req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer revoked-personal-token" {
			t.Fatal("diagnostic escaped the saved Personal read boundary")
		}
		checks++
		return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":"token_revoked","message":"do not expose this body"}}`)), Request: req}, nil
	})
	access, err := original.ExchangeWorkspace(context.Background(), personal, "canonical-space")
	if err == nil || err.Error() != "workspace_token_revoked" || checks != 1 || access.AccessToken != "" {
		t.Fatalf("revoked login must replace the unexplained Personal mismatch: error=%v checks=%d", err, checks)
	}
}

func TestWorkspaceFailuresKeepOnlyObservedPublicFacts(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		status     int
		body, code string
		challenge  bool
	}{
		{"revoked", 401, `{"error":{"code":"token_revoked","message":"secret upstream body"}}`, "workspace_token_revoked", false},
		{"generic401", 401, `{"error":{"code":"private-unknown-reason"}}`, "workspace_session_expired", false},
		{"challenge403", 403, `/cdn-cgi/challenge-platform/`, "platform_browser_challenge", true},
		{"denied403", 403, `{}`, "platform_access_denied", false},
		{"throttled", 429, `{}`, "platform_rate_limited", false},
		{"revokedCodeWithout401", 403, `{"error":{"code":"token_revoked"}}`, "platform_access_denied", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			failure := workspaceHTTPFailure(&http.Response{StatusCode: scenario.status, Header: make(http.Header)}, []byte(scenario.body))
			if failure.Code != scenario.code || failure.HTTPStatus != scenario.status {
				t.Fatalf("wrong diagnosis: %+v", failure)
			}
			encoded, _ := json.Marshal(WorkspaceExchangeDiagnostic(failure))
			if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "private-unknown") {
				t.Fatal("raw error leaked")
			}
			if (failure.UpstreamCode == "token_revoked") != (scenario.code == "workspace_token_revoked") {
				t.Fatal("invented revocation proof")
			}
		})
	}
	diagnostic := WorkspaceExchangeDiagnostic(&WorkspaceExchangeFailure{Code: "private", HTTPStatus: 900, UpstreamCode: "secret", RouteMode: "invalid", ProxyScheme: "socks5h"})
	if diagnostic.Code != "workspace_exchange_unavailable" || diagnostic.HTTPStatus != 0 || diagnostic.UpstreamCode != "" || diagnostic.RouteMode != "" || diagnostic.ProxyScheme != "" {
		t.Fatalf("untrusted adapter facts leaked: %+v", diagnostic)
	}
	timeout := WorkspaceExchangeDiagnostic(errors.Join(context.DeadlineExceeded, errors.New("https://user:password@proxy.example/token")))
	encoded, _ := json.Marshal(timeout)
	if timeout.Code != "network_timeout" || strings.Contains(string(encoded), "password") {
		t.Fatal("transport facts misclassified or leaked")
	}
}
