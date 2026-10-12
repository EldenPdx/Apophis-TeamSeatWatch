package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

func credentialJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".mock"
}
func candidateOAuth(t *testing.T, now time.Time) DeliveryCredentialSet {
	claims := map[string]any{"exp": now.Add(time.Hour).Unix(), "sub": "auth0|original", "scope": CodexScope, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace", "chatgpt_user_id": "canonical"}}
	token := credentialJWT(t, claims)
	return DeliveryCredentialSet{RefreshToken: "refresh", AccessToken: token, IDToken: token, ExpiresIn: 3600, Scope: CodexScope, WorkspaceID: "workspace", PlatformSubjectID: "canonical"}
}
func TestRotationCandidateOAuthValidation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, mode := range []string{"valid", "missing_refresh", "wrong_access_workspace", "wrong_access_user", "oidc_not_canonical", "scope", "expiry", "missing_id", "duplicate", "case_duplicate"} {
		t.Run(mode, func(t *testing.T) {
			c := candidateOAuth(t, now)
			switch mode {
			case "missing_refresh":
				c.RefreshToken = ""
			case "missing_id":
				c.IDToken = ""
			case "scope":
				c.Scope = "openid profile email"
			case "expiry":
				c.ExpiresIn = 0
			case "wrong_access_workspace", "wrong_access_user", "oidc_not_canonical":
				auth := map[string]any{"chatgpt_account_id": "workspace", "chatgpt_user_id": "canonical"}
				if mode == "wrong_access_workspace" {
					auth["chatgpt_account_id"] = "other"
				}
				if mode == "wrong_access_user" {
					auth["chatgpt_user_id"] = "other"
				}
				if mode == "oidc_not_canonical" {
					delete(auth, "chatgpt_user_id")
				}
				c.AccessToken = credentialJWT(t, map[string]any{"exp": now.Add(time.Hour).Unix(), "sub": "canonical", "scope": CodexScope, "https://api.openai.com/auth": auth})
			case "duplicate", "case_duplicate":
				key := "exp"
				if mode == "case_duplicate" {
					key = "EXP"
				}
				raw := `{"exp":` + jsonNumber(now.Add(time.Hour).Unix()) + `,"` + key + `":1}`
				c.IDToken = "e30." + base64.RawURLEncoding.EncodeToString([]byte(raw)) + ".mock"
			}
			_, err := ValidateRotationCandidateOAuth(c, "workspace", "canonical", now)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
		})
	}
}
func jsonNumber(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestRotationCandidateOAuthRejectsMalformedAndConflictingClaims(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, mode := range []string{"sub_array", "sub_nested_conflict", "issuer_conflict", "flat_workspace_conflict", "case_workspace", "expired_access", "missing_access_scope", "duplicate_scope", "missing_id_workspace"} {
		t.Run(mode, func(t *testing.T) {
			c := candidateOAuth(t, now)
			auth := map[string]any{"chatgpt_account_id": "workspace", "chatgpt_user_id": "canonical"}
			raw := map[string]any{"exp": now.Add(time.Hour).Unix(), "sub": "auth0|original", "scope": CodexScope, "https://api.openai.com/auth": auth}
			switch mode {
			case "sub_array":
				raw["sub"] = []string{"bad"}
			case "sub_nested_conflict":
				auth["sub"] = "conflict"
			case "issuer_conflict":
				raw["iss"] = "wrong"
			case "flat_workspace_conflict":
				raw["chatgpt_account_id"] = "other"
			case "case_workspace":
				auth["chatgpt_account_id"] = "Workspace"
			case "expired_access":
				raw["exp"] = now.Add(-time.Second).Unix()
			case "missing_access_scope":
				delete(raw, "scope")
			case "duplicate_scope":
				raw["scope"] = CodexScope + " openid"
			case "missing_id_workspace":
				delete(auth, "chatgpt_account_id")
			}
			if mode == "missing_id_workspace" {
				c.IDToken = credentialJWT(t, raw)
			} else {
				c.AccessToken = credentialJWT(t, raw)
			}
			if _, err := ValidateRotationCandidateOAuth(c, "workspace", "canonical", now); err == nil {
				t.Fatalf("accepted %s", mode)
			}
		})
	}
}

func TestOfficialRotationCandidateWorkspaceExactAuthenticatedIdentity(t *testing.T) {
	for _, mode := range []string{"valid", "wrong_user", "missing_user", "wrong_workspace", "duplicate_response", "duplicate_claim", "personal_substitution", "duplicate_scope", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			p := identitySession(t, nil)
			now := time.Now().UTC().Truncate(time.Second)
			auth := map[string]any{"chatgpt_account_id": "workspace", "chatgpt_user_id": "candidate-user", "chatgpt_plan_type": "team", "scope": "organization.read"}
			claims := map[string]any{"exp": now.Add(time.Hour).Unix(), "sub": "auth0|candidate", "https://api.openai.com/auth": auth}
			user := "auth0|candidate"
			account := "workspace"
			switch mode {
			case "wrong_user":
				user = "wrong"
			case "missing_user":
				user = ""
			case "wrong_workspace":
				account = "other"
			case "duplicate_scope":
				auth["scope"] = "organization.read organization.read"
			}
			token := credentialJWT(t, claims)
			if mode == "personal_substitution" {
				token = p.AccessToken
			}
			if mode == "duplicate_claim" {
				raw := `{"exp":` + jsonNumber(now.Add(time.Hour).Unix()) + `,"EXP":1}`
				token = "e30." + base64.RawURLEncoding.EncodeToString([]byte(raw)) + ".mock"
			}
			body, _ := json.Marshal(map[string]any{"accessToken": token, "account": map[string]string{"id": account}, "user": map[string]string{"id": user}, "expires": now.Add(time.Hour).Format(time.RFC3339)})
			if mode == "duplicate_response" {
				body = []byte(strings.TrimSuffix(string(body), "}") + `,"AccessToken":"other"}`)
			}
			calls := 0
			client := &http.Client{Timeout: time.Second, Transport: exchangeTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.RawQuery != "exchange_workspace_token=true&workspace_id=workspace&reason=setCurrentAccountWithoutRedirect" {
					t.Fatal("invented exchange")
				}
				if _, err := r.Cookie("__Secure-next-auth.session-token"); err != nil {
					t.Fatal("original session absent")
				}
				status := 200
				header := http.Header{}
				if mode == "redirect" {
					status = 302
					header.Set("Location", "https://evil.example/")
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}
			a := OfficialRotationCredentialAdapter{Browser: fixturePersonalBrowser, Client: func(context.Context) (*http.Client, func(), error) { return client, nil, nil }}
			web, err := a.ExchangeCandidateWorkspace(context.Background(), p, "workspace", "candidate-user")
			if (err == nil) != (mode == "valid") || calls != 1 {
				t.Fatalf("mode=%s err=%v calls=%d", mode, err, calls)
			}
			if err == nil && !ValidateRotationCandidateWorkspace(web, "workspace", "candidate-user", time.Now()) {
				t.Fatal("invalid saved web")
			}
		})
	}
}
func TestOfficialRotationCandidateOAuthUsesSourcedIsolatedPKCE(t *testing.T) {
	for _, mode := range []string{"valid", "access_conflict", "duplicate_response", "unicode_response", "external_redirect", "missing_refresh"} {
		t.Run(mode, func(t *testing.T) {
			flow := &deliveryFlowTransport{}
			now := time.Now().UTC().Truncate(time.Second)
			c := candidateOAuth(t, now)
			c.WorkspaceID = "workspace-1"
			c.PlatformSubjectID = "user-1"
			raw := map[string]any{"exp": now.Add(time.Hour).Unix(), "sub": "auth0|original", "scope": CodexScope, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace-1", "chatgpt_user_id": "user-1"}}
			c.IDToken = credentialJWT(t, raw)
			c.AccessToken = c.IDToken
			if mode == "access_conflict" {
				raw["chatgpt_account_id"] = "other"
				c.AccessToken = credentialJWT(t, raw)
			}
			if mode == "missing_refresh" {
				c.RefreshToken = ""
			}
			body, _ := json.Marshal(c)
			if mode == "duplicate_response" || mode == "unicode_response" {
				key := "Refresh_Token"
				if mode == "unicode_response" {
					key = "refreſh_token"
				}
				body = []byte(strings.TrimSuffix(string(body), "}") + `,"` + key + `":"other"}`)
			}
			calls := 0
			client := &http.Client{Timeout: time.Second, Transport: exchangeTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if _, err := r.Cookie("ambient"); err == nil {
					t.Fatal("ambient cookie leaked")
				}
				if r.URL.Path == "/oauth/token" {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
				}
				if mode == "external_redirect" && r.URL.Path == "/login/start" {
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.example/secret"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				return flow.RoundTrip(r)
			})}
			client.Jar, _ = cookiejar.New(nil)
			origin, _ := url.Parse("https://chatgpt.com/")
			client.Jar.SetCookies(origin, []*http.Cookie{{Name: "ambient", Value: "secret"}})
			a := OfficialRotationCredentialAdapter{Browser: fixturePersonalBrowser, Client: func(context.Context) (*http.Client, func(), error) { return client, nil, nil }}
			a.WorkspaceBrowser = fixtureWorkspaceOAuthBrowser
			got, err := a.CreateCandidateOAuth(context.Background(), DeliveryCredentialRequest{Environment: fixtureEnvironment, Identifier: "child@example.test", Password: "pw", TOTPSecret: "JBSWY3DPEHPK3PXP", Workspace: "workspace-1"}, "user-1")
			if (err == nil) != (mode == "valid") || calls == 0 {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
			if err == nil && got.RefreshToken != "refresh" {
				t.Fatal("lost grant")
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("upstream secret leaked")
			}
		})
	}
}
