package platform

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
)

type personalAuthorization struct{ query string }
type personalAuthorizationContextKey struct{}

// Only the URL returned by this login's first-party signin response can bind a
// Personal Web authorization. Every query value remains pinned to that response.
func personalAuthorizationFromLoginURL(target string) (*personalAuthorization, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, personalRequestDenial("personal login URL invalid")
	}
	if u.Path != "/oauth/authorize" {
		return nil, nil
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || !personalAuthorizationURL(u) {
		return nil, personalRequestDenial("personal authorization URL invalid")
	}
	for key, values := range query {
		if key == "" || len(values) != 1 || key == "allowed_workspace_id" || key == "organization_id" {
			return nil, personalRequestDenial("personal authorization query ambiguous or workspace-bound")
		}
	}
	challenge, err := base64.RawURLEncoding.Strict().DecodeString(query.Get("code_challenge"))
	if query.Get("client_id") != personalWebClientID ||
		query.Get("redirect_uri") != officialChatBase+"/api/auth/callback/openai" ||
		!personalAuthorizationScopes(query.Get("scope")) || query.Get("response_type") != "code" ||
		strings.TrimSpace(query.Get("state")) == "" || query.Get("code_challenge_method") != "S256" ||
		len(query.Get("code_challenge")) != 43 || err != nil || len(challenge) != 32 {
		return nil, personalRequestDenial("personal authorization contract invalid")
	}
	return &personalAuthorization{query: query.Encode()}, nil
}

func personalAuthorizationURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Host == "auth.openai.com" && u.User == nil &&
		u.Opaque == "" && u.EscapedPath() == "/oauth/authorize" && u.Fragment == "" && !u.ForceQuery
}

func personalAuthorizationScopes(raw string) bool {
	expected := strings.Fields(personalWebScopes)
	actual := strings.Fields(raw)
	if len(actual) != len(expected) {
		return false
	}
	remaining := make(map[string]bool, len(expected))
	for _, scope := range expected {
		remaining[scope] = true
	}
	for _, scope := range actual {
		if !remaining[scope] {
			return false
		}
		delete(remaining, scope)
	}
	return len(remaining) == 0
}

func (a *personalAuthorization) allows(method string, u *url.URL) bool {
	if a == nil || method != http.MethodGet || !personalAuthorizationURL(u) {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	return err == nil && query.Encode() == a.query
}

// A bound Web authorization can complete with the account's persisted browser
// session and return through its callback to ChatGPT's requested login landing.
func (a *personalAuthorization) completedAt(target string) bool {
	u, err := url.Parse(target)
	return a != nil && err == nil && u.Scheme == "https" && u.Host == "chatgpt.com" && u.User == nil &&
		u.EscapedPath() == "/login" && u.RawQuery == "" && u.Fragment == "" && !u.ForceQuery
}
