package platform

import (
	"context"
	"errors"
	"github.com/chromedp/cdproto/network"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type blockedOAuthHTTP struct{ token http.RoundTripper }

func (t blockedOAuthHTTP) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/oauth/token" {
		return t.token.RoundTrip(req)
	}
	return &http.Response{StatusCode: 403, Header: http.Header{"Cf-Mitigated": []string{"challenge"}}, Body: io.NopCloser(strings.NewReader("edge verification")), Request: req}, nil
}

func TestDeliveryWorkspaceRejectionIsNotExpiredSession(t *testing.T) {
	protocol := &deliveryFlowTransport{}
	client := &http.Client{Timeout: time.Second, Transport: accountsRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/accounts/workspace/select" {
			return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_workspace_selected","message":"private upstream details"}}`)), Request: req}, nil
		}
		return protocol.RoundTrip(req)
	})}
	config, _ := NewHTTPConfig("https://chatgpt.com")
	config.workspaceBrowser = fixtureWorkspaceOAuthBrowser
	reader, _ := config.Reader(client, Credentials{LoginIdentifier: "member@example.com", Password: "pw"})
	session := PersonalSession{DeviceID: "device", AccessToken: "saved-at", ExpiresAt: time.Now().Add(time.Hour), Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "saved-cookie"}}}
	_, err := reader.CreateDeliveryCredentials(t.Context(), DeliveryCredentialRequest{Environment: fixtureEnvironment, Identifier: "member@example.com", Password: "pw", TOTPSecret: "JBSWY3DPEHPK3PXP", Workspace: "workspace-1", PersonalSession: &session})
	if !errors.Is(err, ErrOAuthWorkspaceUnavailable) || errors.Is(err, ErrBrowserSessionExpired) || OAuthFailureCode(err) != "oauth_workspace_unavailable" || strings.Contains(err.Error(), "private") {
		t.Fatalf("workspace rejection misclassified or exposed: %v", err)
	}
	if NormalizeDiagnostic(OAuthFailureCode(err)) != "oauth_workspace_unavailable" || NormalizeDiagnostic("oauth_authorize_http_403 private-secret") != "diagnostic_other" {
		t.Fatal("persistence lost the bounded OAuth reason or accepted upstream text")
	}
}

func TestWorkspaceOAuthBrowserGuardsExactWorkspaceAndCallback(t *testing.T) {
	browser := &workspaceOAuthBrowser{workspace: "workspace-1"}
	initialState := "state"
	browser.state.Store(&initialState)
	for _, tc := range []struct {
		method, target, body string
		allowed              bool
	}{
		{"POST", officialAuthBase + "/api/accounts/workspace/select", `{"workspace_id":"workspace-1"}`, true},
		{"POST", officialAuthBase + "/api/accounts/workspace/select", `{"workspace_id":"other"}`, false},
		{"POST", officialAuthBase + "/api/accounts/workspace/select", `{"workspace_id":"workspace-1","extra":"value"}`, false},
		{"POST", officialChatBase + "/backend-api/accounts/workspace-1/invites/accept", `{}`, false},
		{"POST", officialChatBase + "/backend-api/accounts/workspace-1/requests", `{}`, false},
		{"GET", BuildWorkspaceAuthorizeURL("challenge", "state", "workspace-1"), "", true},
		{"GET", BuildWorkspaceAuthorizeURL("challenge", "state", "other"), "", false},
		{"GET", "https://other.example/", "", false},
	} {
		req, _ := http.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		if (browser.validate(req) == nil) != tc.allowed {
			t.Fatalf("wrong request guard for %s %s", tc.method, req.URL.Path)
		}
	}
	state := "expected"
	browser.state.Store(&state)
	if browser.allowed(&network.Request{Method: "GET", URL: CodexRedirectURI + "?code=private-code&state=wrong"}) || browser.terminal.Load() != nil {
		t.Fatal("accepted callback with wrong state")
	}
	if browser.allowed(&network.Request{Method: "GET", URL: CodexRedirectURI + "?code=private-code&state=expected"}) || browser.terminal.Load() == nil {
		t.Fatal("callback must be captured without a localhost request")
	}
}

func fixtureWorkspaceOAuthBrowser(_ context.Context, source *http.Client, _ http.CookieJar, _ string, _ browserenv.Environment) (http.RoundTripper, func(), error) {
	return source.Transport, func() {}, nil
}

func TestDeliveryOAuthKeepsBrowserContextAfterPersonalLogin(t *testing.T) {
	protocol := &deliveryFlowTransport{}
	source := &http.Client{Transport: blockedOAuthHTTP{token: protocol}, Timeout: time.Second}
	config, _ := NewHTTPConfig("https://chatgpt.com")
	opened, closed := false, false
	config.workspaceBrowser = func(_ context.Context, client *http.Client, jar http.CookieJar, workspace string, environment browserenv.Environment) (http.RoundTripper, func(), error) {
		if client.Transport != source.Transport || jar == nil || workspace != "workspace-1" {
			t.Fatal("browser lost lease, cookies or target")
		}
		if environment.DeviceID != fixtureEnvironment.DeviceID {
			t.Fatal("OAuth changed the explicit device identity")
		}
		origin, _ := url.Parse(officialChatBase)
		device := ""
		for _, cookie := range jar.Cookies(origin) {
			if cookie.Name == "oai-did" {
				device = cookie.Value
			}
		}
		if device != environment.DeviceID {
			t.Fatal("legacy saved session replaced the persisted browser device identity")
		}
		opened = true
		return protocol, func() { closed = true }, nil
	}
	reader, _ := config.Reader(source, Credentials{LoginIdentifier: "member@example.com", Password: "pw"})
	session := PersonalSession{DeviceID: "fixture-device", AccessToken: personalFixtureToken(time.Now(), nil), ExpiresAt: time.Now().Add(time.Hour), Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "fixture", Host: "chatgpt.com"}}}
	result, err := reader.CreateDeliveryCredentials(t.Context(), DeliveryCredentialRequest{Environment: fixtureEnvironment, Identifier: "member@example.com", Password: "pw", TOTPSecret: "JBSWY3DPEHPK3PXP", Workspace: "workspace-1", PersonalSession: &session})
	if err != nil {
		t.Fatalf("saved Personal login must reach OAuth despite HTTP consent challenge: %v", err)
	}
	if session.DeviceID != "fixture-device" {
		t.Fatal("OAuth mutated its caller-owned saved session")
	}
	if !opened || !closed || result.RefreshToken != "refresh" || result.WorkspaceID != "workspace-1" {
		t.Fatal("workspace OAuth not completed through isolated browser")
	}
}
