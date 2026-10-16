package platform

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type deliveryFlowTransport struct {
	state          string
	workspaceSteps int
	unexpected     string
	loginRequests  int
}

func (f *deliveryFlowTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	respond := func(status int, headers http.Header, body string) (*http.Response, error) {
		if headers == nil {
			headers = make(http.Header)
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	}
	path := req.URL.Host + req.URL.Path
	switch {
	case path == "chatgpt.com/api/auth/providers":
		f.loginRequests++
		return respond(http.StatusOK, nil, `{}`)
	case path == "chatgpt.com/api/auth/csrf":
		return respond(http.StatusOK, nil, `{"csrfToken":"csrf"}`)
	case path == "chatgpt.com/api/auth/signin/openai":
		query := req.URL.Query()
		if query.Get("ext-oai-did") == "" || query.Get("auth_session_logging_id") == "" || query.Get("ext-passkey-client-capabilities") != "1111" {
			return respond(http.StatusBadRequest, nil, `{"code":"missing_signin_context"}`)
		}
		return respond(http.StatusOK, nil, `{"url":"https://auth.openai.com/login/start"}`)
	case path == "auth.openai.com/login/start":
		return respond(http.StatusFound, http.Header{"Location": []string{"https://auth.openai.com/log-in/password"}}, "")
	case path == "auth.openai.com/log-in/password":
		return respond(http.StatusOK, nil, `<div>log-in/password</div>`)
	case path == "auth.openai.com/api/accounts/password/verify":
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"password":"pw"`) {
			return respond(http.StatusBadRequest, nil, `{"code":"bad_password_payload"}`)
		}
		return respond(http.StatusOK, nil, `{"continue_url":"https://auth.openai.com/after-password"}`)
	case path == "auth.openai.com/after-password":
		return respond(http.StatusOK, nil, `<div>/mfa-challenge/challenge-12345678</div>`)
	case path == "auth.openai.com/api/accounts/mfa/verify", path == "auth.openai.com/api/accounts/mfa/totp/verify":
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"type":"totp"`) && !strings.Contains(string(body), `"code":"`) {
			return respond(http.StatusBadRequest, nil, `{"code":"bad_totp_payload"}`)
		}
		if path == "auth.openai.com/api/accounts/mfa/verify" && !strings.Contains(string(body), `"id":"challenge-12345678"`) {
			return respond(http.StatusBadRequest, nil, `{"code":"missing_challenge_id"}`)
		}
		return respond(http.StatusOK, nil, `{"continue_url":"https://auth.openai.com/after-totp"}`)
	case path == "chatgpt.com/api/auth/session":
		return respond(http.StatusOK, http.Header{"Set-Cookie": []string{"__Secure-next-auth.session-token=fixture-session; Secure; Path=/"}}, `{"accessToken":"`+personalFixtureToken(time.Now(), nil)+`","expires":"`+time.Now().Add(30*24*time.Hour).UTC().Format(time.RFC3339)+`"}`)
	case path == "auth.openai.com/after-totp":
		return respond(http.StatusOK, nil, `authenticated`)
	case path == "auth.openai.com/sign-in-with-chatgpt/codex/consent":
		return respond(http.StatusOK, nil, `<div>consent</div>`)
	case path == "auth.openai.com/api/accounts/workspace/select":
		f.workspaceSteps++
		if f.state == "" {
			return respond(http.StatusConflict, nil, `{"error":{"code":"invalid_state"}}`)
		}
		return respond(http.StatusFound, http.Header{"Location": []string{"http://localhost:1455/auth/callback?code=AUTH-CODE&state=" + url.QueryEscape(f.state)}}, "")
	case path == "auth.openai.com/workspace-selected":
		return respond(http.StatusOK, nil, `selected`)
	case path == "auth.openai.com/oauth/authorize":
		f.state = req.URL.Query().Get("state")
		if req.URL.Query().Get("allowed_workspace_id") != "workspace-1" || req.URL.Query().Get("code_challenge_method") != "S256" {
			return respond(http.StatusBadRequest, nil, `{"code":"bad_authorize_query"}`)
		}
		return respond(http.StatusFound, http.Header{"Location": []string{"https://auth.openai.com/choose-an-account"}}, "")
	case path == "auth.openai.com/choose-an-account":
		return respond(http.StatusOK, nil, `<div>us_12345678901234567890</div>`)
	case path == "auth.openai.com/api/accounts/session/select":
		return respond(http.StatusOK, nil, `{"continue_url":"https://auth.openai.com/codex/consent"}`)
	case path == "auth.openai.com/codex/consent":
		return respond(http.StatusOK, nil, `<div>consent</div>`)
	case path == "auth.openai.com/oauth/token":
		body, _ := io.ReadAll(req.Body)
		form, _ := url.ParseQuery(string(body))
		if form.Get("code") != "AUTH-CODE" || form.Get("grant_type") != "authorization_code" || form.Get("code_verifier") == "" {
			return respond(http.StatusBadRequest, nil, `{"code":"bad_token_payload"}`)
		}
		idToken := testJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"workspace-1","chatgpt_user_id":"user-1"},"sub":"fallback-user"}`)
		return respond(http.StatusOK, nil, `{"access_token":"`+idToken+`","refresh_token":"refresh","id_token":"`+idToken+`","expires_in":3600,"scope":"openid"}`)
	default:
		f.unexpected = path
		return respond(http.StatusNotFound, nil, `{"code":"not_found"}`)
	}
}

func testJWT(payload string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return header + "." + encoded + ".signature"
}

func TestCreateDeliveryCredentialsRunsWorkspacePKCEFlow(t *testing.T) {
	transport := &deliveryFlowTransport{}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	config, err := NewHTTPConfig("https://fixture.invalid")
	config.personalBrowser = fixturePersonalBrowser
	config.workspaceBrowser = fixtureWorkspaceOAuthBrowser
	if err != nil {
		t.Fatal(err)
	}
	reader, err := config.Reader(client, Credentials{LoginIdentifier: "member@example.com", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.CreateDeliveryCredentials(context.Background(), DeliveryCredentialRequest{Environment: fixtureEnvironment,
		Identifier: "member@example.com", Password: "pw", TOTPSecret: "JBSWY3DPEHPK3PXP", Workspace: "workspace-1",
	})
	if err != nil {
		t.Fatalf("%v (unexpected=%s)", err, transport.unexpected)
	}
	if result.RefreshToken != "refresh" || result.WorkspaceID != "workspace-1" || result.PlatformSubjectID != "user-1" {
		t.Fatalf("unexpected delivery result: %+v", result)
	}
	if transport.workspaceSteps != 1 {
		t.Fatalf("workspace selection steps=%d want 1", transport.workspaceSteps)
	}
}

func TestCreateDeliveryCredentialsUsesFreshAttemptCookieJar(t *testing.T) {
	transport := &deliveryFlowTransport{}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	config, err := NewHTTPConfig("https://fixture.invalid")
	config.personalBrowser = fixturePersonalBrowser
	config.workspaceBrowser = fixtureWorkspaceOAuthBrowser
	if err != nil {
		t.Fatal(err)
	}
	reader, err := config.Reader(client, Credentials{LoginIdentifier: "member@example.com", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	input := DeliveryCredentialRequest{Environment: fixtureEnvironment, Identifier: "member@example.com", Password: "pw", TOTPSecret: "JBSWY3DPEHPK3PXP", Workspace: "workspace-1"}
	if _, err := reader.CreateDeliveryCredentials(context.Background(), input); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	firstJar := client.Jar
	if firstJar == nil {
		t.Fatal("first attempt did not create a cookie context")
	}

	// Model a fresh upstream authorization attempt while retaining the leased transport.
	transport.state = ""
	transport.workspaceSteps = 0
	client.Jar = nil
	if _, err := reader.CreateDeliveryCredentials(context.Background(), input); err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	if client.Transport != transport {
		t.Fatal("attempt replaced the lease-owned transport")
	}
	if client.Jar == nil || client.Jar == firstJar {
		t.Fatal("second attempt reused the previous cookie context")
	}
}

func TestCreateDeliveryCredentialsRequiresTOTP(t *testing.T) {
	config, err := NewHTTPConfig("https://fixture.invalid")
	config.personalBrowser = fixturePersonalBrowser
	config.workspaceBrowser = fixtureWorkspaceOAuthBrowser
	if err != nil {
		t.Fatal(err)
	}
	reader, err := config.Reader(&http.Client{Transport: &deliveryFlowTransport{}, Timeout: time.Second}, Credentials{LoginIdentifier: "member@example.com", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.CreateDeliveryCredentials(context.Background(), DeliveryCredentialRequest{Environment: fixtureEnvironment, Identifier: "member@example.com", Password: "pw", Workspace: "workspace-1"})
	if err == nil || !strings.Contains(err.Error(), "requires TOTP") {
		t.Fatalf("err=%v", err)
	}
}

type deliveryProbeTransport struct{}

func (deliveryProbeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != officialChatBase+"/backend-api/wham/usage?workspace_id=workspace-1" {
		return nil, &url.Error{Op: "GET", URL: req.URL.String(), Err: io.ErrUnexpectedEOF}
	}
	if req.Header.Get("Authorization") == "" || req.URL.Host != "chatgpt.com" {
		return nil, io.ErrUnexpectedEOF
	}
	return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"token_invalidated"}`)), Request: req}, nil
}

func TestCheckDeliveryLivenessUsesOfficialReadEndpoint(t *testing.T) {
	config, err := NewHTTPConfig("https://fixture.invalid")
	config.personalBrowser = fixturePersonalBrowser
	config.workspaceBrowser = fixtureWorkspaceOAuthBrowser
	if err != nil {
		t.Fatal(err)
	}
	reader, err := config.OAuthReader(&http.Client{Transport: deliveryProbeTransport{}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	token := testJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"workspace-1","chatgpt_user_id":"user-1"}}`)
	result, err := reader.CheckDeliveryLiveness(context.Background(), token, "workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "auth_error" || result.HTTPStatus != http.StatusUnauthorized || result.PlatformSubjectID != "user-1" {
		t.Fatalf("unexpected probe result: %+v", result)
	}
}

func TestTokenIdentityPrefersPlatformUserClaim(t *testing.T) {
	workspace, subject := tokenIdentity(testJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"workspace-1","chatgpt_user_id":"user-1"},"sub":"jwt-sub"}`))
	if workspace != "workspace-1" || subject != "user-1" {
		t.Fatalf("workspace=%q subject=%q", workspace, subject)
	}
	fallback := testJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"workspace-1"},"sub":"jwt-sub"}`)
	_, subject = tokenIdentity(fallback)
	if subject != "jwt-sub" {
		t.Fatalf("fallback subject=%q", subject)
	}
}

func TestGeneratePKCEProducesS256Challenge(t *testing.T) {
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(digest[:])
	if challenge != want {
		t.Fatalf("challenge=%q want %q", challenge, want)
	}
}

func TestDeliveryAuthorizationReusesSavedAccountSession(t *testing.T) {
	transport := &deliveryFlowTransport{}
	reader, err := NewHTTPReader(&http.Client{Transport: transport, Timeout: time.Second}, "https://fixture.invalid", Credentials{LoginIdentifier: "member@example.com", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	reader.config.workspaceBrowser = fixtureWorkspaceOAuthBrowser
	saved := PersonalSession{AccessToken: "saved-at", DeviceID: "saved-device", ExpiresAt: time.Now().Add(7 * 24 * time.Hour), Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "saved-cookie"}, {Name: "auth-session", Value: "auth-cookie", Host: "auth.openai.com"}}}
	generated, err := reader.CreateDeliveryCredentials(t.Context(), DeliveryCredentialRequest{Environment: fixtureEnvironment, Identifier: "member@example.com", Password: "pw", TOTPSecret: "JBSWY3DPEHPK3PXP", Workspace: "workspace-1", PersonalSession: &saved})
	if err != nil || generated.WorkspaceID != "workspace-1" || transport.loginRequests != 0 {
		t.Fatalf("reused login=%d err=%v", transport.loginRequests, err)
	}
}

func TestDeliveryAuthorizationReportsOnlyConfirmedSessionRejection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		loginPage bool
		expired   bool
	}{
		{"401", 401, false, true}, {"403", 403, false, false}, {"login page", 302, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &deliveryFlowTransport{}
			client := &http.Client{Timeout: time.Second, Transport: accountsRoundTrip(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/oauth/authorize" {
					headers := make(http.Header)
					if tc.loginPage {
						headers.Set("Location", "https://auth.openai.com/log-in/password")
					}
					return &http.Response{StatusCode: tc.status, Header: headers, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
				}
				return fixture.RoundTrip(req)
			})}
			reader, err := NewHTTPReader(client, "https://fixture.invalid", Credentials{LoginIdentifier: "member@example.com", Password: "pw"})
			if err != nil {
				t.Fatal(err)
			}
			reader.config.workspaceBrowser = fixtureWorkspaceOAuthBrowser
			saved := PersonalSession{AccessToken: "saved-at", DeviceID: "device", ExpiresAt: time.Now().Add(time.Hour), Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie"}}}
			_, err = reader.CreateDeliveryCredentials(t.Context(), DeliveryCredentialRequest{Environment: fixtureEnvironment, Identifier: "member@example.com", Password: "pw", TOTPSecret: "JBSWY3DPEHPK3PXP", Workspace: "workspace-1", PersonalSession: &saved})
			if errors.Is(err, ErrBrowserSessionExpired) != tc.expired || fixture.loginRequests != 0 {
				t.Fatalf("session expired=%v want=%v login=%d err=%v", errors.Is(err, ErrBrowserSessionExpired), tc.expired, fixture.loginRequests, err)
			}
		})
	}
}
