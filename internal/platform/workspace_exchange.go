package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// OfficialWorkspaceTokenExchanger is an explicit Owner action only. Although
// the sourced operation is a GET, it changes the isolated browser session's
// current account. It must never run from discovery or a facts read.
type OfficialWorkspaceTokenExchanger struct{ Client DiscoveryClient }

var ErrWorkspaceExchangeUnavailable = errors.New("workspace token exchange unavailable")

const workspaceExchangeBodyLimit = 64 << 10

type workspaceExchangeGuard struct {
	next  http.RoundTripper
	query string
}

func (g workspaceExchangeGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL == nil || req.URL.Scheme != "https" || req.URL.Host != "chatgpt.com" ||
		req.URL.User != nil || req.URL.Fragment != "" || req.URL.Path != "/api/auth/session" ||
		req.URL.RawQuery != g.query || (req.Host != "" && req.Host != req.URL.Host) {
		return nil, ErrWorkspaceExchangeUnavailable
	}
	return g.next.RoundTrip(req)
}

func (e OfficialWorkspaceTokenExchanger) ExchangeWorkspace(ctx context.Context, personal PersonalSession, workspaceID string) (WorkspaceAccess, error) {
	return e.exchangeWorkspace(ctx, personal, workspaceID, "")
}

func (e OfficialWorkspaceTokenExchanger) exchangeWorkspace(ctx context.Context, personal PersonalSession, workspaceID, candidateUser string) (WorkspaceAccess, error) {
	failed := WorkspaceAccess{}
	if !personal.ExpiresAt.After(time.Now().Add(time.Minute)) {
		return failed, workspaceFailure("workspace_session_expired", 0, nil)
	}
	if !ValidatePersonalRefresh(PersonalRefreshResult{Status: "ready", Session: personal}, time.Now()) {
		return failed, workspaceFailure("workspace_session_invalid", 0, nil)
	}
	if e.Client == nil || workspaceID == "" || len(workspaceID) > 255 || strings.TrimSpace(workspaceID) != workspaceID || strings.ContainsAny(workspaceID, "\r\n\x00") {
		return failed, workspaceFailure("workspace_exchange_unavailable", 0, nil)
	}
	base, release, err := e.Client(ctx)
	if release != nil {
		defer release()
	}
	if err != nil {
		return failed, WorkspaceExchangeDiagnostic(err)
	}
	if base == nil || base.Transport == nil || base.Timeout <= 0 {
		return failed, workspaceFailure("workspace_exchange_unavailable", 0, nil)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return failed, ErrWorkspaceExchangeUnavailable
	}
	origin, _ := url.Parse(selectedWorkspaceOrigin + "/")
	cookies := make([]*http.Cookie, 0, len(personal.Cookies))
	for _, cookie := range personal.Cookies {
		if cookie.Host != "" && cookie.Host != "chatgpt.com" {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: cookie.Name, Value: cookie.Value, Path: "/", Secure: true})
	}
	jar.SetCookies(origin, cookies)
	// The reference exchange sets these account cookies before its session GET.
	// The jar is per-attempt; no saved Personal cookie is changed.
	jar.SetCookies(origin, []*http.Cookie{
		{Name: "_account", Value: workspaceID, Path: "/", Secure: true},
		{Name: "_account_residency_region", Path: "/", MaxAge: -1},
		{Name: "_account_routing_override", Path: "/", MaxAge: -1},
		{Name: "_account_is_fedramp", Value: "false", Path: "/", Secure: true},
		{Name: "oai-workspace", Value: workspaceID, Path: "/", Secure: true},
		{Name: "oai-workspace-residency-region", Path: "/", MaxAge: -1},
		{Name: "oai-workspace-routing-override", Path: "/", MaxAge: -1},
	})
	query := "exchange_workspace_token=true&workspace_id=" + url.QueryEscape(workspaceID) + "&reason=setCurrentAccountWithoutRedirect"
	client := *base
	client.Transport = workspaceExchangeGuard{next: base.Transport, query: query}
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, selectedWorkspaceOrigin+"/api/auth/session?"+query, nil)
	if err != nil {
		return failed, ErrWorkspaceExchangeUnavailable
	}
	req.Header.Set("User-Agent", browserAuthUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Oai-Device-Id", personal.DeviceID)
	req.Header.Set("Oai-Client-Version", "prod-75c89a339d12aa428439cc667726d9e2351c5334")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("X-Openai-Target-Path", "/api/auth/session")
	req.Header.Set("X-Openai-Target-Route", "/api/auth/session")
	req.Header.Set("Referer", selectedWorkspaceOrigin+"/")
	response, err := client.Do(req)
	if err != nil {
		return failed, WorkspaceExchangeDiagnostic(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, workspaceExchangeBodyLimit+1))
	if response.StatusCode != http.StatusOK || response.Header.Get("Cf-Mitigated") == "challenge" || strings.Contains(string(body), "/cdn-cgi/challenge-platform/") {
		return failed, workspaceHTTPFailure(response, body)
	}
	if err != nil {
		failure := WorkspaceExchangeDiagnostic(err)
		failure.HTTPStatus = response.StatusCode
		return failed, failure
	}
	if len(body) > workspaceExchangeBodyLimit {
		return failed, workspaceFailure("platform_response_invalid", response.StatusCode, nil)
	}
	var snapshot struct {
		AccessToken string `json:"accessToken"`
		Expires     string `json:"expires"`
		Account     struct {
			ID string `json:"id"`
		} `json:"account"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeErr := json.Unmarshal(body, &snapshot)
	if candidateUser != "" {
		decodeErr = credentialJSON(body, &snapshot)
	}
	if decodeErr != nil {
		return failed, workspaceFailure("platform_response_invalid", response.StatusCode, nil)
	}
	if returnedPersonalWorkspace(snapshot.AccessToken, workspaceID) {
		if candidateUser != "" {
			return failed, workspaceFailure("workspace_personal_returned", response.StatusCode, nil)
		}
		// A Personal response describes the failed switch, not its cause. Read
		// current account visibility through the same held route and original
		// Personal session before attributing it to login or workspace access.
		discovery, _ := (AccountsCheckDiscovery{Client: func(context.Context) (*http.Client, func(), error) { return base, nil, nil }}).Discover(ctx, personal)
		if discovery.Failure != nil {
			return failed, discovery.Failure
		}
		if discovery.Status == "discovered" {
			visible := false
			for _, workspace := range discovery.Workspaces {
				visible = visible || workspace.PlatformID == workspaceID
			}
			if !visible {
				return failed, workspaceFailure("workspace_no_longer_visible", http.StatusOK, nil)
			}
		}
		return failed, workspaceFailure("workspace_personal_returned", response.StatusCode, nil)
	}
	if snapshot.Account.ID != workspaceID {
		return failed, workspaceFailure("workspace_mismatch", response.StatusCode, nil)
	}
	sessionExpiry, err := time.Parse(time.RFC3339, snapshot.Expires)
	if err != nil {
		return failed, workspaceFailure("platform_response_invalid", response.StatusCode, nil)
	}
	now := time.Now().UTC()
	jwtExpiry, err := workspaceJWTExpiry(snapshot.AccessToken, workspaceID, now)
	if err != nil {
		failure := WorkspaceExchangeDiagnostic(err)
		failure.HTTPStatus = response.StatusCode
		return failed, failure
	}
	expires := now.Add(24 * time.Hour)
	if jwtExpiry.Before(expires) {
		expires = jwtExpiry
	}
	if sessionExpiry.Before(expires) {
		expires = sessionExpiry
	}
	if personal.ExpiresAt.Before(expires) {
		expires = personal.ExpiresAt
	}
	if !expires.After(now.Add(time.Minute)) {
		return failed, workspaceFailure("workspace_token_expired", response.StatusCode, nil)
	}
	access := WorkspaceAccess{AccessToken: snapshot.AccessToken, WorkspaceID: workspaceID, DeviceID: personal.DeviceID, SessionID: uuid.NewString(), ExpiresAt: expires}
	for _, cookie := range jar.Cookies(origin) {
		access.Cookies = append(access.Cookies, SessionCookie{Name: cookie.Name, Value: cookie.Value})
	}
	if candidateUser != "" {
		personalClaims, claimErr := rotationCredentialClaims(personal.AccessToken)
		if claimErr != nil {
			return failed, ErrWorkspaceExchangeUnavailable
		}
		nested, _ := personalClaims["https://api.openai.com/auth"].(map[string]any)
		if !workspaceClaimMatches(personalClaims, nested, "chatgpt_user_id", candidateUser) ||
			(snapshot.User.ID != candidateUser && snapshot.User.ID != personalClaims["sub"]) || snapshot.User.ID == "" ||
			!ValidateRotationCandidateWorkspace(access, workspaceID, candidateUser, now) {
			return failed, ErrWorkspaceExchangeUnavailable
		}
	}
	if !ValidateWorkspaceAccess(access, workspaceID, now) {
		return failed, workspaceFailure("workspace_token_invalid", response.StatusCode, nil)
	}
	return access, nil
}

func returnedPersonalWorkspace(token, workspaceID string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 16384 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err != nil || json.Unmarshal(payload, &claims) != nil {
		return false
	}
	nested, _ := claims["https://api.openai.com/auth"].(map[string]any)
	for _, plan := range []string{"free", "plus", "pro"} {
		if workspaceClaimMatches(claims, nested, "chatgpt_plan_type", plan) && !workspaceClaimMatches(claims, nested, "chatgpt_account_id", workspaceID) {
			return true
		}
	}
	return false
}

// JWT claims here are an admission hint, not independent signature proof: the
// token must also come from the fixed authenticated HTTPS session response.
func workspaceJWTExpiry(token, workspaceID string, now time.Time) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 16384 || len(parts[1]) == 0 {
		return time.Time{}, workspaceFailure("workspace_token_invalid", 0, nil)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > 16384 {
		return time.Time{}, workspaceFailure("workspace_token_invalid", 0, nil)
	}
	var expiryClaim struct {
		Expires json.Number `json:"exp"`
	}
	var claims map[string]any
	if json.Unmarshal(payload, &expiryClaim) != nil || json.Unmarshal(payload, &claims) != nil || claims == nil {
		return time.Time{}, workspaceFailure("workspace_token_invalid", 0, nil)
	}
	var authClaims map[string]any
	if candidate, present := claims["https://api.openai.com/auth"]; present {
		var ok bool
		authClaims, ok = candidate.(map[string]any)
		if !ok {
			return time.Time{}, workspaceFailure("workspace_token_invalid", 0, nil)
		}
	}
	// Admit the same Team/Business plans used by discovery. Conflicting flat
	// and nested identities, plans or scopes remain invalid.
	if !workspaceClaimMatches(claims, authClaims, "chatgpt_account_id", workspaceID) {
		return time.Time{}, workspaceFailure("workspace_mismatch", 0, nil)
	}
	if !workspaceClaimMatches(claims, authClaims, "chatgpt_plan_type", "team") && !workspaceClaimMatches(claims, authClaims, "chatgpt_plan_type", "business") {
		return time.Time{}, workspaceFailure("workspace_plan_unsupported", 0, nil)
	}
	if !workspaceReadScopes(claims, authClaims) {
		return time.Time{}, workspaceFailure("workspace_read_scope_missing", 0, nil)
	}
	seconds, err := expiryClaim.Expires.Int64()
	if err != nil {
		return time.Time{}, workspaceFailure("workspace_token_invalid", 0, nil)
	}
	expiry := time.Unix(seconds, 0).UTC()
	if !expiry.After(now.Add(time.Minute)) {
		return time.Time{}, workspaceFailure("workspace_token_expired", 0, nil)
	}
	return expiry, nil
}

func workspaceClaimMatches(raw, nested map[string]any, key, expected string) bool {
	found := false
	for _, candidate := range []struct {
		source map[string]any
		key    string
	}{
		{raw, key}, {nested, key}, {raw, "https://api.openai.com/auth." + key},
	} {
		if candidate.source == nil {
			continue
		}
		value, present := candidate.source[candidate.key]
		if !present {
			continue
		}
		text, ok := value.(string)
		if !ok || text != expected {
			return false
		}
		found = true
	}
	return found
}

// organization.read is a conservative Web read-scope gate. The exact
// exchanged Workspace JWT scope shape has not been captured, so absent or
// conflicting claims fail closed rather than falling back to Personal. Neither
// organization.write nor a successful GET authorizes membership mutation.
func workspaceReadScopes(raw, nested map[string]any) bool {
	var admitted map[string]bool
	for _, candidate := range []struct {
		source map[string]any
		key    string
	}{
		{raw, "scp"}, {nested, "scp"}, {raw, "scope"}, {nested, "scope"},
		{raw, "https://api.openai.com/auth.scp"}, {raw, "https://api.openai.com/auth.scope"},
	} {
		if candidate.source == nil {
			continue
		}
		value, present := candidate.source[candidate.key]
		if !present {
			continue
		}
		current := map[string]bool{}
		switch typed := value.(type) {
		case string:
			for _, scope := range strings.Fields(typed) {
				current[scope] = true
			}
		case []any:
			for _, item := range typed {
				scope, ok := item.(string)
				if !ok || strings.TrimSpace(scope) != scope || scope == "" {
					return false
				}
				current[scope] = true
			}
		default:
			return false
		}
		if len(current) == 0 || (admitted != nil && !samePersonalScopeSet(admitted, current)) {
			return false
		}
		admitted = current
	}
	return admitted != nil && admitted["organization.read"]
}

func ValidateWorkspaceAccess(access WorkspaceAccess, workspaceID string, now time.Time) bool {
	if access.WorkspaceID != workspaceID || access.DeviceID == "" || len(access.DeviceID) > 128 ||
		strings.ContainsAny(access.DeviceID, "\r\n\x00") || len(access.Cookies) == 0 || len(access.Cookies) > 50 ||
		access.ExpiresAt.Before(now.Add(30*time.Second)) || access.ExpiresAt.After(now.Add(24*time.Hour)) {
		return false
	}
	if _, err := uuid.Parse(access.SessionID); err != nil {
		return false
	}
	jwtExpiry, err := workspaceJWTExpiry(access.AccessToken, workspaceID, now)
	if err != nil || access.ExpiresAt.After(jwtExpiry) {
		return false
	}
	var accountCookie, workspaceCookie bool
	for _, cookie := range access.Cookies {
		if cookie.Name == "" || len(cookie.Name) > 128 || cookie.Value == "" || len(cookie.Value) > 16384 || strings.ContainsAny(cookie.Name+cookie.Value, "\r\n\t;\x00") {
			return false
		}
		switch cookie.Name {
		case "_account":
			accountCookie = cookie.Value == workspaceID
		case "oai-workspace":
			workspaceCookie = cookie.Value == workspaceID
		}
	}
	return hasBrowserSessionCookie(access.Cookies) && accountCookie && workspaceCookie
}
