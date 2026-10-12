package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
)

func fixturePersonalAuthorizationURL() string {
	query := url.Values{
		"client_id":    {personalWebClientID},
		"redirect_uri": {officialChatBase + "/api/auth/callback/openai"},
		"scope":        {personalWebScopes}, "response_type": {"code"},
		"state": {"fixture-state"}, "code_challenge": {strings.Repeat("A", 43)},
		"code_challenge_method": {"S256"}, "nonce": {"fixture-nonce"},
		"fixture_extension": {"opaque-value"},
	}
	return officialAuthBase + "/oauth/authorize?" + query.Encode()
}

func TestPersonalRefreshFollowsItsWebAuthorizationThroughBrowserGuard(t *testing.T) {
	fixture := &personalFlowFixture{signin: fixturePersonalAuthorizationURL(), token: personalFixtureToken(time.Now(), nil)}
	source := &http.Client{Transport: fixture, Timeout: time.Second}
	authorizations := 0
	adapter := PersonalWebRefresher{
		Client: func(context.Context) (*http.Client, func(), error) { return source, func() {}, nil },
		Browser: func(_ context.Context, _ *http.Client, _ http.CookieJar, _ browserenv.Environment) (http.RoundTripper, func(), error) {
			policy := &personalBrowserTransport{}
			return accountsRoundTrip(func(req *http.Request) (*http.Response, error) {
				policy.bindRequest(req.Context(), req)
				defer policy.authorization.Store(nil)
				if !policy.allowedRequest(&network.Request{URL: req.URL.String(), Method: req.Method}) {
					return nil, personalRequestDenial("fixture browser refused request")
				}
				if req.URL.Path == "/oauth/authorize" {
					authorizations++
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {officialAuthBase + "/login/start"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
				}
				return fixture.RoundTrip(req)
			}), func() {}, nil
		},
	}
	result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "account@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
	if err != nil || result.Status != "ready" || authorizations != 1 || fixture.blocked {
		t.Fatalf("Personal Web authorization did not finish password/TOTP/session: status=%s authorizations=%d error=%v", result.Status, authorizations, err)
	}
}

func TestPersonalAuthorizationPinsHTTPBrowserAndContinuationToOneLogin(t *testing.T) {
	target := fixturePersonalAuthorizationURL()
	authorization, err := personalAuthorizationFromLoginURL(target)
	if err != nil || authorization == nil {
		t.Fatal("fixture Personal authorization rejected")
	}
	ctx := context.WithValue(t.Context(), personalAuthorizationContextKey{}, authorization)
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	policy := &personalBrowserTransport{}
	continuation := policy.bindRequest(t.Context(), request)
	final, _ := http.NewRequestWithContext(continuation, http.MethodGet, target, nil)
	if validatePersonalRequest(request) != nil || validatePersonalRequest(final) != nil || !policy.allowedRequest(&network.Request{URL: target, Method: http.MethodGet}) {
		t.Fatal("one login's HTTP/browser/final continuation policies disagree")
	}
	for _, tc := range []struct {
		name, key, value string
	}{
		{"codex", "client_id", "app_EMoamEEZ73f0CkXaXp7hrann"},
		{"callback", "redirect_uri", "http://localhost:1455/auth/callback"},
		{"scope", "scope", personalWebScopes + " api.connectors.read"},
		{"state", "state", "other-state"},
		{"challenge", "code_challenge", strings.Repeat("B", 43)},
		{"method", "code_challenge_method", "plain"},
		{"response", "response_type", "token"},
		{"nonce", "nonce", "other-nonce"},
		{"extension", "fixture_extension", "other-value"},
		{"extra", "new_parameter", "extra-value"},
		{"workspace", "allowed_workspace_id", "workspace-1"},
		{"organization", "organization_id", "organization-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := url.Parse(target)
			query := u.Query()
			query.Set(tc.key, tc.value)
			u.RawQuery = query.Encode()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
			if validatePersonalRequest(req) == nil || policy.allowedRequest(&network.Request{URL: u.String(), Method: http.MethodGet}) {
				t.Fatal("modified authorization escaped the shared guard")
			}
		})
	}
	for _, changed := range []string{target + "&state=fixture-state", target + "#fragment", strings.Replace(target, "/oauth/authorize?", "/oauth/%61uthorize?", 1), strings.Replace(target, "auth.openai.com", "chatgpt.com", 1)} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, changed, nil)
		if validatePersonalRequest(req) == nil || policy.allowedRequest(&network.Request{URL: changed, Method: http.MethodGet}) {
			t.Fatal("ambiguous or different authorization URL escaped the shared guard")
		}
	}
	unbound, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	post, _ := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if validatePersonalRequest(unbound) == nil || validatePersonalRequest(post) == nil || allowedPersonalBrowserRequest(&network.Request{URL: target, Method: http.MethodGet}) {
		t.Fatal("bare authorization or POST gained permission")
	}
	policy.bindRequest(t.Context(), unbound)
	if policy.allowedRequest(&network.Request{URL: target, Method: http.MethodGet}) {
		t.Fatal("browser retained another request's authorization")
	}
}

func TestPersonalRefreshRejectsOtherAuthorizationContractsBeforePassword(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*url.URL)
	}{
		{"client", func(u *url.URL) {
			q := u.Query()
			q.Set("client_id", "app_EMoamEEZ73f0CkXaXp7hrann")
			u.RawQuery = q.Encode()
		}},
		{"callback", func(u *url.URL) {
			q := u.Query()
			q.Set("redirect_uri", "http://localhost:1455/auth/callback")
			u.RawQuery = q.Encode()
		}},
		{"scope", func(u *url.URL) { q := u.Query(); q.Set("scope", "openid"); u.RawQuery = q.Encode() }},
		{"duplicate_scope", func(u *url.URL) { q := u.Query(); q.Set("scope", personalWebScopes+" openid"); u.RawQuery = q.Encode() }},
		{"missing_state", func(u *url.URL) { q := u.Query(); q.Del("state"); u.RawQuery = q.Encode() }},
		{"invalid_pkce", func(u *url.URL) { q := u.Query(); q.Set("code_challenge", "invalid"); u.RawQuery = q.Encode() }},
		{"plain_pkce", func(u *url.URL) { q := u.Query(); q.Set("code_challenge_method", "plain"); u.RawQuery = q.Encode() }},
		{"response", func(u *url.URL) { q := u.Query(); q.Set("response_type", "token"); u.RawQuery = q.Encode() }},
		{"workspace", func(u *url.URL) { q := u.Query(); q.Set("allowed_workspace_id", ""); u.RawQuery = q.Encode() }},
		{"organization", func(u *url.URL) { q := u.Query(); q.Set("organization_id", ""); u.RawQuery = q.Encode() }},
		{"duplicate", func(u *url.URL) { u.RawQuery += "&state=fixture-state" }},
		{"malformed_query", func(u *url.URL) { u.RawQuery += "&bad=%GG" }},
		{"fragment", func(u *url.URL) { u.Fragment = "fragment" }},
		{"external", func(u *url.URL) { u.Host = "outside.test" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := url.Parse(fixturePersonalAuthorizationURL())
			tc.alter(u)
			fixture := &personalFlowFixture{signin: u.String(), token: personalFixtureToken(time.Now(), nil)}
			source := &http.Client{Transport: fixture, Timeout: time.Second}
			adapter := PersonalWebRefresher{Browser: fixturePersonalBrowser, Client: func(context.Context) (*http.Client, func(), error) { return source, func() {}, nil }}
			result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "account@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
			if err != nil || result.Status != "refresh_failed" || result.Failure == nil || result.Failure.Code != "login_request_failed" || len(fixture.paths) != 3 || fixture.blocked {
				t.Fatalf("other authorization reached password: status=%s requests=%d error=%v", result.Status, len(fixture.paths), err)
			}
		})
	}
}

func TestPersonalRefreshReadsItsCompletedWebAuthorizationBeforePassword(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(map[string]any)
		ready bool
	}{
		{"same_account", nil, true},
		{"known_subject_match", nil, true},
		{"known_subject_mismatch", nil, false},
		{"snapshot_identifier_mismatch", nil, false},
		{"other_identifier", func(c map[string]any) { c["email"] = "other@example.test" }, false},
		{"missing_identifier", func(c map[string]any) { delete(c, "email") }, false},
		{"missing_subject", func(c map[string]any) { delete(c, "sub") }, false},
		{"wrong_client", func(c map[string]any) {
			c["https://api.openai.com/auth"].(map[string]any)["client_id"] = "other-client"
		}, false},
		{"missing_scope", func(c map[string]any) { c["https://api.openai.com/auth"].(map[string]any)["scp"] = []string{"openid"} }, false},
		{"workspace", func(c map[string]any) {
			c["https://api.openai.com/auth"].(map[string]any)["chatgpt_plan_type"] = "team"
		}, false},
		{"missing_mfa", func(c map[string]any) { delete(c, "https://api.openai.com/mfa") }, false},
		{"password_only", func(c map[string]any) { c["https://api.openai.com/auth"].(map[string]any)["amr"] = []string{"pwd"} }, false},
		{"expired", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := personalFixtureToken(time.Now(), func(c map[string]any) {
				c["email"] = "account@example.test"
				c["sub"] = "personal-subject"
				if tc.alter != nil {
					tc.alter(c)
				}
			})
			fixture := &personalFlowFixture{signin: fixturePersonalAuthorizationURL(), token: token, passwordStatus: http.StatusConflict}
			source := &http.Client{Transport: fixture, Timeout: time.Second}
			adapter := PersonalWebRefresher{
				Client: func(context.Context) (*http.Client, func(), error) { return source, func() {}, nil },
				Browser: func(_ context.Context, _ *http.Client, _ http.CookieJar, _ browserenv.Environment) (http.RoundTripper, func(), error) {
					policy := &personalBrowserTransport{}
					return accountsRoundTrip(func(req *http.Request) (*http.Response, error) {
						policy.bindRequest(req.Context(), req)
						defer policy.authorization.Store(nil)
						if !policy.allowedRequest(&network.Request{URL: req.URL.String(), Method: req.Method}) {
							return nil, personalRequestDenial("fixture browser refused request")
						}
						if req.URL.Path == "/oauth/authorize" {
							// Production navigation follows the first-party callback in the
							// browser and returns its final document's Request to the reader.
							final, _ := http.NewRequestWithContext(req.Context(), http.MethodGet, officialChatBase+"/login", nil)
							return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: final, Body: io.NopCloser(strings.NewReader("completed Personal authorization"))}, nil
						}
						response, err := fixture.RoundTrip(req)
						if err == nil && req.URL.Path == "/api/auth/session" && (tc.name == "known_subject_match" || tc.name == "known_subject_mismatch" || tc.name == "snapshot_identifier_mismatch") {
							body, _ := io.ReadAll(response.Body)
							response.Body.Close()
							var snapshot map[string]any
							_ = json.Unmarshal(body, &snapshot)
							user := map[string]string{"id": "personal-subject", "email": "account@example.test"}
							if tc.name == "known_subject_mismatch" {
								user["id"] = "other-subject"
							}
							if tc.name == "snapshot_identifier_mismatch" {
								user["email"] = "other@example.test"
							}
							snapshot["user"] = user
							body, _ = json.Marshal(snapshot)
							response.Body = io.NopCloser(strings.NewReader(string(body)))
						}
						return response, err
					}), func() {}, nil
				},
			}
			result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "account@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
			if err != nil || (result.Status == "ready") != tc.ready || fixture.blocked {
				t.Fatalf("completed Personal authorization admission: status=%s want_ready=%t error=%v", result.Status, tc.ready, err)
			}
			sessions := 0
			for _, path := range fixture.paths {
				if path == "GET chatgpt.com/api/auth/session" {
					sessions++
				}
				if strings.Contains(path, "password") || strings.Contains(path, "mfa") {
					t.Fatal("completed Web authorization was forced back into password/MFA")
				}
			}
			if sessions != 1 {
				t.Fatalf("completed authorization session reads=%d, want one", sessions)
			}
		})
	}
}

func TestCompletedPersonalAuthorizationRequiresItsBoundExactLanding(t *testing.T) {
	authorization, err := personalAuthorizationFromLoginURL(fixturePersonalAuthorizationURL())
	if err != nil || !authorization.completedAt(officialChatBase+"/login") {
		t.Fatal("bound login landing unavailable")
	}
	for _, target := range []string{officialChatBase + "/login?error=OAuthCallback", officialChatBase + "/login#fragment", officialChatBase + "/", officialAuthBase + "/log-in", "http://chatgpt.com/login", "https://outside.test/login", officialChatBase + "/%6cogin"} {
		if authorization.completedAt(target) {
			t.Fatal("different authorization landing admitted")
		}
	}
	var unbound *personalAuthorization
	if unbound.completedAt(officialChatBase + "/login") {
		t.Fatal("unbound login landing admitted")
	}
}

func TestPersonalRefreshCannotReadCompletedSessionFromUnboundSignin(t *testing.T) {
	fixture := &personalFlowFixture{token: personalFixtureToken(time.Now(), func(c map[string]any) { c["email"] = "account@example.test"; c["sub"] = "personal-subject" }), passwordStatus: http.StatusConflict}
	source := &http.Client{Transport: fixture, Timeout: time.Second}
	adapter := PersonalWebRefresher{
		Client: func(context.Context) (*http.Client, func(), error) { return source, func() {}, nil },
		Browser: func(_ context.Context, _ *http.Client, _ http.CookieJar, _ browserenv.Environment) (http.RoundTripper, func(), error) {
			return accountsRoundTrip(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/login/start" {
					final, _ := http.NewRequestWithContext(req.Context(), http.MethodGet, officialChatBase+"/login", nil)
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: final, Body: io.NopCloser(strings.NewReader("unbound landing"))}, nil
				}
				return fixture.RoundTrip(req)
			}), func() {}, nil
		},
	}
	result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "account@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
	if err != nil || result.Status != "refresh_failed" || fixture.blocked {
		t.Fatalf("unbound signin bypassed full login: status=%s error=%v", result.Status, err)
	}
	for _, path := range fixture.paths {
		if path == "GET chatgpt.com/api/auth/session" {
			t.Fatal("unbound signin accepted a completed session")
		}
	}
}
