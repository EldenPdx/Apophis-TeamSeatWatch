package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func personalFixtureToken(now time.Time, alter func(map[string]any)) string {
	claims := map[string]any{"exp": now.Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"client_id": personalWebClientID, "chatgpt_account_id": "personal-1", "chatgpt_plan_type": "free", "scp": strings.Fields(personalWebScopes), "amr": []string{"urn:openai:amr:otp_totp"}}, "https://api.openai.com/mfa": map[string]any{"required": "yes"}}
	if alter != nil {
		alter(claims)
	}
	body, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(body) + ".signature"
}

type personalFlowFixture struct {
	providerStatus    int
	providerChallenge bool
	token             string
	sessionExpires    time.Time
	sessionStatus     int
	sessionBody       string
	signin            string
	redirect          string
	mfaContinue       string
	mfaStatus         int
	passwordStatus    int
	passwordContinue  string
	paths             []string
	blocked           bool
}

func (f *personalFlowFixture) RoundTrip(req *http.Request) (*http.Response, error) {
	path := req.URL.Host + req.URL.Path
	f.paths = append(f.paths, req.Method+" "+path)
	respond := func(status int, body string, headers http.Header) (*http.Response, error) {
		if headers == nil {
			headers = make(http.Header)
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	}
	switch path {
	case "chatgpt.com/api/auth/providers":
		if f.providerStatus != 0 {
			headers := http.Header{}
			if f.providerChallenge {
				headers.Set("Cf-Mitigated", "challenge")
			}
			return respond(f.providerStatus, "edge verification", headers)
		}
		return respond(200, `{}`, nil)
	case "chatgpt.com/api/auth/csrf":
		return respond(200, `{"csrfToken":"csrf"}`, nil)
	case "chatgpt.com/api/auth/signin/openai":
		if f.signin != "" {
			return respond(200, `{"url":"`+f.signin+`"}`, nil)
		}
		return respond(200, `{"url":"https://auth.openai.com/login/start"}`, nil)
	case "auth.openai.com/login/start":
		if f.redirect != "" {
			return respond(302, "", http.Header{"Location": []string{f.redirect}})
		}
		return respond(302, "", http.Header{"Location": []string{"https://auth.openai.com/log-in/password"}})
	case "auth.openai.com/log-in/password":
		return respond(200, `<div>log-in/password</div>`, nil)
	case "auth.openai.com/api/accounts/password/verify":
		if f.passwordStatus != 0 {
			return respond(f.passwordStatus, `{}`, nil)
		}
		if f.passwordContinue != "" {
			return respond(200, `{"continue_url":"`+f.passwordContinue+`"}`, nil)
		}
		return respond(200, `{"continue_url":"https://auth.openai.com/after-password"}`, nil)
	case "auth.openai.com/after-password":
		return respond(200, `/mfa-challenge/challenge-12345678`, nil)
	case "auth.openai.com/api/accounts/mfa/verify":
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"id":"challenge-12345678"`) || !strings.Contains(string(body), `"type":"totp"`) {
			f.blocked = true
		}
		if f.mfaStatus != 0 {
			return respond(f.mfaStatus, `{}`, nil)
		}
		if f.mfaContinue != "" {
			return respond(200, `{"continue_url":"`+f.mfaContinue+`"}`, nil)
		}
		return respond(200, `{"continue_url":"https://auth.openai.com/after-totp"}`, nil)
	case "auth.openai.com/after-totp":
		return respond(200, `authenticated`, nil)
	case "chatgpt.com/api/auth/session":
		if f.sessionStatus != 0 {
			return respond(f.sessionStatus, f.sessionBody, nil)
		}
		if f.sessionBody != "" {
			return respond(200, f.sessionBody, nil)
		}
		expiry := f.sessionExpires
		if expiry.IsZero() {
			expiry = time.Now().Add(2 * time.Hour)
		}
		return respond(200, `{"accessToken":"`+f.token+`","expires":"`+expiry.UTC().Format(time.RFC3339)+`"}`, http.Header{"Set-Cookie": []string{"__Secure-next-auth.session-token=cookie-secret; Secure; Path=/"}})
	default:
		f.blocked = true
		return respond(404, `{}`, nil)
	}
}

func TestPersonalWebRefreshReportsBrowserChallengeWithoutCredentials(t *testing.T) {
	fixture := &personalFlowFixture{providerStatus: 403, providerChallenge: true}
	adapter := PersonalWebRefresher{Browser: fixturePersonalBrowser, Client: func(context.Context) (*http.Client, func(), error) {
		return &http.Client{Transport: fixture, Timeout: time.Second}, func() {}, nil
	}}
	result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "account@example.test", Password: "password-secret", TOTPSecret: "JBSWY3DPEHPK3PXP"})
	data, _ := json.Marshal(result)
	var facts struct {
		Failure *struct {
			Code       string
			HTTPStatus int
		} `json:"failure"`
	}
	if err != nil || json.Unmarshal(data, &facts) != nil || result.Status != "refresh_failed" || facts.Failure == nil || facts.Failure.Code != "platform_browser_challenge" || facts.Failure.HTTPStatus != 403 {
		t.Fatalf("browser challenge classification missing: status=%s failure=%+v error=%v", result.Status, facts.Failure, err)
	}
	if len(fixture.paths) != 1 || fixture.paths[0] != "GET chatgpt.com/api/auth/providers" || strings.Contains(string(data), "password-secret") || strings.Contains(string(data), "JBSWY3DPEHPK3PXP") || result.Session.AccessToken != "" {
		t.Fatal("challenge response must not submit or expose login materials")
	}
}

func TestPersonalWebRefreshUsesOnlyPasswordTOTPAndSessionGET(t *testing.T) {
	fixture := &personalFlowFixture{token: personalFixtureToken(time.Now(), nil)}
	client := &http.Client{Transport: fixture, Timeout: time.Second}
	leases, releases := 0, 0
	adapter := PersonalWebRefresher{Browser: fixturePersonalBrowser, Client: func(context.Context) (*http.Client, func(), error) {
		leases++
		return client, func() { releases++ }, nil
	}}
	result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "mother@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
	if err != nil || result.Status != "ready" || result.Session.AccessToken != fixture.token || result.Session.DeviceID == "" || len(result.Session.Cookies) != 1 || leases != 1 || releases != 1 || fixture.blocked {
		t.Fatalf("result=%+v err=%v requests=%v lease=%d/%d", result, err, fixture.paths, leases, releases)
	}
	for _, path := range fixture.paths {
		if strings.Contains(path, "workspace/select") || strings.Contains(path, "oauth/") || strings.Contains(path, "accounts/check") {
			t.Fatalf("forbidden operation: %s", path)
		}
	}
}

func TestPostTOTPPersonalExpiryRequiresPositivePersonalPlan(t *testing.T) {
	for _, plan := range []string{"free", "plus", "pro", "personal", "default"} {
		t.Run(plan, func(t *testing.T) {
			token := personalFixtureToken(time.Now(), func(c map[string]any) { c["https://api.openai.com/auth"].(map[string]any)["chatgpt_plan_type"] = plan })
			if _, err := postTOTPPersonalExpiry(token, time.Now()); err != nil {
				t.Fatalf("supported Personal plan %s rejected: %v", plan, err)
			}
		})
	}
}

func TestPersonalRefreshAndDiscoveryAcquireSeparateLeases(t *testing.T) {
	fixture := &personalFlowFixture{token: personalFixtureToken(time.Now(), nil)}
	client := &http.Client{Timeout: time.Second, Transport: accountsRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/backend-api/accounts/check/v4-2023-04-27" {
			if req.Method != "GET" || req.Header.Get("Authorization") != "Bearer "+fixture.token || !strings.Contains(req.Header.Get("Cookie"), "cookie-secret") {
				t.Fatalf("discovery escaped Personal session: %+v", req)
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"accounts":{"one":{"account":{"account_id":"team-1","name":"One","structure":"workspace","plan_type":"team"}}}}`)), Request: req}, nil
		}
		return fixture.RoundTrip(req)
	})}
	acquired, released := 0, 0
	factory := func(context.Context) (*http.Client, func(), error) {
		acquired++
		return client, func() { released++ }, nil
	}
	refreshed, err := (PersonalWebRefresher{Browser: fixturePersonalBrowser, Client: factory}).RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "mother@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
	if err != nil || refreshed.Status != "ready" {
		t.Fatalf("Personal refresh: %+v %v", refreshed, err)
	}
	discovered, err := (AccountsCheckDiscovery{Client: factory}).Discover(t.Context(), refreshed.Session)
	if err != nil || discovered.Status != "discovered" || len(discovered.Workspaces) != 1 || acquired != 2 || released != 2 {
		t.Fatalf("separate leases: %+v %v count %d/%d", discovered, err, acquired, released)
	}
}

func TestPersonalWebRefreshRejectsRedirectsClaimsAndBadTOTP(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*personalFlowFixture)
	}{
		{"external_signin", func(f *personalFlowFixture) { f.signin = "https://outside.test/steal" }},
		{"external_redirect", func(f *personalFlowFixture) { f.redirect = "https://outside.test/steal" }},
		{"external_mfa_continuation", func(f *personalFlowFixture) { f.mfaContinue = "https://outside.test/steal" }},
		{"workspace_selection_continuation", func(f *personalFlowFixture) { f.mfaContinue = "https://auth.openai.com/api/accounts/workspace/select" }},
		{"oauth_continuation", func(f *personalFlowFixture) { f.mfaContinue = "https://auth.openai.com/oauth/authorize" }},
		{"insecure_signin", func(f *personalFlowFixture) { f.signin = "http://auth.openai.com/login/start" }},
		{"invalid_password", func(f *personalFlowFixture) { f.passwordStatus = 401 }},
		{"invalid_totp", func(f *personalFlowFixture) { f.mfaStatus = 401 }},
		{"wrong_client_claim", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) {
				c["https://api.openai.com/auth"].(map[string]any)["client_id"] = "other-client"
			})
		}},
		{"missing_scope_claim", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) { c["https://api.openai.com/auth"].(map[string]any)["scp"] = []string{"openid"} })
		}},
		{"password_only_claim", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) { delete(c, "https://api.openai.com/mfa") })
		}},
		{"workspace_claim", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) {
				c["https://api.openai.com/auth"].(map[string]any)["chatgpt_plan_type"] = "team"
			})
		}},
		{"forged_flat_workspace_claim", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) {
				c["https://api.openai.com/auth.chatgpt_plan_type"] = "team"
				c["https://api.openai.com/auth.chatgpt_account_id"] = "workspace-1"
			})
		}},
		{"conflicting_account_id", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) { c["https://api.openai.com/auth.chatgpt_account_id"] = "other-personal" })
		}},
		{"conflicting_scopes", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) { c["https://api.openai.com/auth.scp"] = []string{"openid"} })
		}},
		{"conflicting_mfa", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) { c["https://api.openai.com/mfa.required"] = false })
		}},
		{"unknown_personal_plan", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) {
				c["https://api.openai.com/auth"].(map[string]any)["chatgpt_plan_type"] = "unclassified"
			})
		}},
		{"expired_claim", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() })
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := &personalFlowFixture{token: personalFixtureToken(time.Now(), nil)}
			tt.mutate(fixture)
			client := &http.Client{Transport: fixture, Timeout: time.Second}
			adapter := PersonalWebRefresher{Browser: fixturePersonalBrowser, Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
			result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "mother@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
			if err != nil || result.Status == "ready" || result.Session.AccessToken != "" || fixture.blocked {
				t.Fatalf("unexpected result=%+v err=%v paths=%v", result, err, fixture.paths)
			}
		})
	}
	called := false
	adapter := PersonalWebRefresher{Browser: fixturePersonalBrowser, Client: func(context.Context) (*http.Client, func(), error) { called = true; return nil, nil, nil }}
	result, _ := adapter.RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "mother@example.test", Password: "password", TOTPSecret: "bad"})
	if called || result.Status != "refresh_failed" {
		t.Fatal("invalid TOTP reached transport")
	}
}

func TestAcquirePersonalUsesCookiesBeforePasswordAndKeepsPlatformExpiry(t *testing.T) {
	now := time.Now()
	sessionExpiry := now.Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	tokenExpiry := now.Add(48 * time.Hour).UTC().Truncate(time.Second)
	fixture := &personalFlowFixture{token: personalFixtureToken(now, func(c map[string]any) { c["exp"] = tokenExpiry.Unix() }), sessionExpires: sessionExpiry}
	old := PersonalSession{AccessToken: "old", DeviceID: "saved-device", ExpiresAt: now.Add(-time.Minute), SessionExpiresAt: sessionExpiry, Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "saved-cookie"}, {Name: "auth-session", Value: "auth-cookie", Host: "auth.openai.com"}}}
	client := &http.Client{Timeout: time.Second, Transport: accountsRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "chatgpt.com" && strings.Contains(req.Header.Get("Cookie"), "auth-cookie") {
			t.Fatal("auth-host cookie leaked to ChatGPT")
		}
		if req.URL.Path == "/api/auth/session" && !strings.Contains(req.Header.Get("Cookie"), "saved-cookie") {
			t.Fatal("saved cookie was not reused")
		}
		return fixture.RoundTrip(req)
	})}
	result, err := AcquirePersonal(t.Context(), PersonalWebRefresher{Browser: fixturePersonalBrowser, Client: func(context.Context) (*http.Client, func(), error) { return client, nil, nil }}, MotherMaterial{Environment: fixtureEnvironment}, old)
	if err != nil || result.Status != "ready" || !result.Session.ExpiresAt.Equal(tokenExpiry) || !result.Session.SessionExpiresAt.Equal(sessionExpiry) {
		t.Fatalf("renewal status=%s err=%v expiry=%v/%v", result.Status, err, result.Session.ExpiresAt, result.Session.SessionExpiresAt)
	}
	if len(fixture.paths) != 1 || fixture.paths[0] != "GET chatgpt.com/api/auth/session" {
		t.Fatalf("renewal logged in again: %v", fixture.paths)
	}
	if old.AccessToken != "old" || old.Cookies[0].Value != "saved-cookie" {
		t.Fatal("renewal changed saved input")
	}
	fixture.paths = nil
	if result, err = AcquirePersonal(t.Context(), PersonalWebRefresher{}, MotherMaterial{Environment: fixtureEnvironment}, result.Session); err != nil || result.Status != "ready" || len(fixture.paths) != 0 {
		t.Fatal("valid session was not reused without a lease")
	}
}

func TestAcquirePersonalFallsBackOnlyForConfirmedLoggedOutSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		login  bool
	}{
		{"401", 401, "{}", true}, {"empty session", 200, "{}", true}, {"forbidden", 403, "{}", false}, {"server error", 503, "{}", false}, {"malformed", 200, "not-json", false}, {"unconfirmed", 200, `{"error":"try again"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &personalFlowFixture{token: personalFixtureToken(time.Now(), nil)}
			first := true
			client := &http.Client{Timeout: time.Second, Transport: accountsRoundTrip(func(req *http.Request) (*http.Response, error) {
				if first && req.URL.Path == "/api/auth/session" {
					first = false
					return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
				}
				return fixture.RoundTrip(req)
			})}
			saved := PersonalSession{DeviceID: "device", ExpiresAt: time.Now().Add(-time.Hour), Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "saved"}}}
			result, err := AcquirePersonal(t.Context(), PersonalWebRefresher{Browser: fixturePersonalBrowser, Client: func(context.Context) (*http.Client, func(), error) { return client, nil, nil }}, MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "member@example.test", Password: "pw", TOTPSecret: "JBSWY3DPEHPK3PXP"}, saved)
			if err != nil {
				t.Fatal(err)
			}
			logged := false
			for _, path := range fixture.paths {
				if strings.Contains(path, "password/verify") {
					logged = true
				}
			}
			if logged != tc.login || (result.Status == "ready") != tc.login {
				t.Fatalf("status=%s login=%v paths=%v", result.Status, logged, fixture.paths)
			}
		})
	}
}

func TestPersonalRefreshSubmitsPasswordResponseChallengeWithoutLoadingAnotherPage(t *testing.T) {
	fixture := &personalFlowFixture{token: personalFixtureToken(time.Now(), nil), passwordContinue: "https://auth.openai.com/mfa-challenge/challenge-12345678"}
	adapter := PersonalWebRefresher{Browser: fixturePersonalBrowser, Client: func(context.Context) (*http.Client, func(), error) {
		return &http.Client{Transport: fixture, Timeout: time.Second}, func() {}, nil
	}}
	result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "account@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
	if err != nil || result.Status != "ready" || fixture.blocked {
		t.Fatalf("password challenge not submitted: status=%s err=%v", result.Status, err)
	}
	for _, path := range fixture.paths {
		if strings.Contains(path, "mfa-challenge/") {
			t.Fatal("loaded a challenge page despite the verified password response already identifying it")
		}
	}
}

func TestPersonalGuardRejectionDoesNotReportNetworkFailure(t *testing.T) {
	fixture := &personalFlowFixture{signin: officialAuthBase + "/oauth/authorize", token: personalFixtureToken(time.Now(), nil)}
	source := &http.Client{Transport: fixture, Timeout: time.Second}
	adapter := PersonalWebRefresher{Browser: fixturePersonalBrowser, Client: func(context.Context) (*http.Client, func(), error) { return source, func() {}, nil }}
	result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "member@example.test", Password: "pw", TOTPSecret: "JBSWY3DPEHPK3PXP"})
	if err != nil || result.Status != "refresh_failed" || result.Failure == nil || result.Failure.Code != "login_request_failed" {
		t.Fatalf("local scope rejection classified as network: result=%+v err=%v", result, err)
	}
}
