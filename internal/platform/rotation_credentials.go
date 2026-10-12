package platform

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
	"unicode"
)

var ErrRotationCredentialsUnavailable = errors.New("candidate scoped credentials unavailable")

// This adapter only acquires credentials for the supplied original target. It
// neither accepts invitations nor refreshes the saved Personal generation.
type RotationCredentialAdapter interface {
	ExchangeCandidateWorkspace(context.Context, PersonalSession, string, string) (WorkspaceAccess, error)
	CreateCandidateOAuth(context.Context, DeliveryCredentialRequest, string) (DeliveryCredentialSet, error)
}
type OfficialRotationCredentialAdapter struct {
	Client           DiscoveryClient
	Browser          PersonalBrowserFactory
	WorkspaceBrowser WorkspaceOAuthBrowserFactory
}

func (a OfficialRotationCredentialAdapter) ExchangeCandidateWorkspace(ctx context.Context, p PersonalSession, w, u string) (WorkspaceAccess, error) {
	return (OfficialWorkspaceTokenExchanger{Client: a.Client}).exchangeWorkspace(ctx, p, w, u)
}
func (a OfficialRotationCredentialAdapter) CreateCandidateOAuth(ctx context.Context, r DeliveryCredentialRequest, u string) (DeliveryCredentialSet, error) {
	if a.Client == nil {
		return DeliveryCredentialSet{}, ErrRotationCredentialsUnavailable
	}
	base, release, err := a.Client(ctx)
	if release != nil {
		defer release()
	}
	if err != nil || base == nil || base.Transport == nil || base.Timeout <= 0 {
		return DeliveryCredentialSet{}, ErrRotationCredentialsUnavailable
	}
	client := *base
	client.Jar, _ = cookiejar.New(nil)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Transport = credentialOriginGuard{base.Transport}
	factory := a.Browser
	if factory == nil {
		factory = NewPersonalBrowser
	}
	browser := func(ctx context.Context, c *http.Client, jar http.CookieJar, environment browserenv.Environment) (http.RoundTripper, func(), error) {
		source := *c
		source.Transport = base.Transport
		return factory(ctx, &source, jar, environment)
	}
	config, err := NewHTTPConfig(officialChatBase)
	if err != nil {
		return DeliveryCredentialSet{}, ErrRotationCredentialsUnavailable
	}
	config.personalBrowser = browser
	if a.WorkspaceBrowser != nil {
		config.workspaceBrowser = a.WorkspaceBrowser
	}
	credentials, err := (&HTTPReader{client: &client, config: config}).CreateDeliveryCredentials(ctx, r)
	if err != nil {
		return DeliveryCredentialSet{}, ErrRotationCredentialsUnavailable
	}
	_, err = ValidateRotationCandidateOAuth(credentials, r.Workspace, u, time.Now())
	if err != nil {
		return DeliveryCredentialSet{}, err
	}
	return credentials, nil
}

type credentialOriginGuard struct{ next http.RoundTripper }

func (g credentialOriginGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL == nil || r.URL.Scheme != "https" || (r.URL.Host != "chatgpt.com" && r.URL.Host != "auth.openai.com") || r.URL.User != nil || r.URL.Fragment != "" || r.Host != "" && r.Host != r.URL.Host {
		return nil, ErrRotationCredentialsUnavailable
	}
	return g.next.RoundTrip(r)
}

// Duplicate names (including case-folded names) at every object depth are
// rejected before decoding security claims or authenticated token responses.
func credentialJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func() error
	walk = func() error {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := k.(string)
				if !ok {
					return ErrRotationCredentialsUnavailable
				}
				// encoding/json folds Unicode too (for example, s and ſ).
				// Lowercasing alone misses aliases of typed response fields.
				folded := strings.Map(func(r rune) rune {
					lowest := r
					for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
						lowest = min(lowest, next)
					}
					return lowest
				}, name)
				if seen[folded] {
					return ErrRotationCredentialsUnavailable
				}
				seen[folded] = true
				if err = walk(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		default:
			return ErrRotationCredentialsUnavailable
		}
		_, err = d.Token()
		return err
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrRotationCredentialsUnavailable
	}
	return json.Unmarshal(raw, out)
}
func rotationCredentialClaims(token string) (map[string]any, error) {
	p := strings.Split(token, ".")
	if len(p) != 3 || len(token) > 16384 || p[0] == "" || p[2] == "" {
		return nil, ErrRotationCredentialsUnavailable
	}
	raw, err := base64.RawURLEncoding.DecodeString(p[1])
	if err != nil {
		return nil, ErrRotationCredentialsUnavailable
	}
	var claims map[string]any
	if credentialJSON(raw, &claims) != nil || claims == nil {
		return nil, ErrRotationCredentialsUnavailable
	}
	return claims, nil
}
func rotationCredentialIdentity(token, w, u string, now time.Time) (map[string]any, map[string]any, time.Time, error) {
	raw, err := rotationCredentialClaims(token)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	nested := map[string]any{}
	if v, ok := raw["https://api.openai.com/auth"]; ok {
		nested, ok = v.(map[string]any)
		if !ok {
			return nil, nil, time.Time{}, ErrRotationCredentialsUnavailable
		}
	}
	if w == "" || u == "" || !workspaceClaimMatches(raw, nested, "chatgpt_account_id", w) || !workspaceClaimMatches(raw, nested, "chatgpt_user_id", u) {
		return nil, nil, time.Time{}, ErrRotationCredentialsUnavailable
	}
	exp, ok := raw["exp"].(float64)
	if !ok || exp != float64(int64(exp)) {
		return nil, nil, time.Time{}, ErrRotationCredentialsUnavailable
	}
	expiry := time.Unix(int64(exp), 0).UTC()
	if !expiry.After(now.Add(time.Minute)) || expiry.After(now.Add(24*time.Hour)) {
		return nil, nil, time.Time{}, ErrRotationCredentialsUnavailable
	}
	return raw, nested, expiry, nil
}
func strictScopeSet(v any) (map[string]bool, bool) {
	scopes := []string{}
	switch x := v.(type) {
	case string:
		scopes = strings.Fields(x)
	case []any:
		for _, s := range x {
			text, ok := s.(string)
			if !ok {
				return nil, false
			}
			scopes = append(scopes, text)
		}
	default:
		return nil, false
	}
	out := map[string]bool{}
	for _, s := range scopes {
		if s == "" || strings.TrimSpace(s) != s || out[s] {
			return nil, false
		}
		out[s] = true
	}
	return out, len(out) > 0
}
func rotationOAuthScopes(raw, nested map[string]any, expected map[string]bool, required bool) bool {
	found := false
	for _, c := range []struct {
		m map[string]any
		k string
	}{{raw, "scope"}, {raw, "scp"}, {nested, "scope"}, {nested, "scp"}, {raw, "https://api.openai.com/auth.scope"}, {raw, "https://api.openai.com/auth.scp"}} {
		if v, ok := c.m[c.k]; ok {
			found = true
			scopes, ok := strictScopeSet(v)
			if !ok || !samePersonalScopeSet(expected, scopes) {
				return false
			}
		}
	}
	return found || !required
}

// Claims are consistency checks, NOT signature authority. Only the admitted
// authenticated Workspace/PKCE response may supply these tokens. Canonical
// chatgpt_user_id is required; an OIDC sub or Personal account id cannot stand in.
func ValidateRotationCandidateOAuth(c DeliveryCredentialSet, w, u string, now time.Time) (time.Time, error) {
	fail := func() (time.Time, error) { return time.Time{}, ErrRotationCredentialsUnavailable }
	for _, token := range []string{c.RefreshToken, c.AccessToken, c.IDToken} {
		if token == "" || len(token) > 16384 || strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n\t\x00") {
			return fail()
		}
	}
	if c.ExpiresIn <= 60 || c.ExpiresIn > 86400 || c.WorkspaceID != w || c.PlatformSubjectID != u {
		return fail()
	}
	scope, ok := strictScopeSet(c.Scope)
	supported, _ := strictScopeSet(CodexScope)
	if !ok || !samePersonalScopeSet(scope, supported) {
		return fail()
	}
	access, an, ae, err := rotationCredentialIdentity(c.AccessToken, w, u, now)
	if err != nil {
		return fail()
	}
	id, in, ie, err := rotationCredentialIdentity(c.IDToken, w, u, now)
	if err != nil {
		return fail()
	}
	if !rotationOAuthScopes(access, an, scope, true) || !rotationOAuthScopes(id, in, scope, false) {
		return fail()
	}
	for _, key := range []string{"sub", "iss"} {
		av, aerr := consistentPersonalClaim(access, an, key, false)
		iv, ierr := consistentPersonalClaim(id, in, key, false)
		if aerr != nil || ierr != nil || av != iv {
			return fail()
		}
	}
	expiry := now.Add(time.Duration(c.ExpiresIn) * time.Second)
	if ae.Before(expiry) {
		expiry = ae
	}
	if ie.Before(expiry) {
		expiry = ie
	}
	return expiry, nil
}
func ValidateRotationCandidatePersonal(p PersonalSession, subject string, now time.Time) bool {
	if !ValidatePersonalRefresh(PersonalRefreshResult{Status: "ready", Session: p}, now) {
		return false
	}
	raw, err := rotationCredentialClaims(p.AccessToken)
	if err != nil {
		return false
	}
	nested, _ := raw["https://api.openai.com/auth"].(map[string]any)
	return workspaceClaimMatches(raw, nested, "chatgpt_user_id", subject)
}

func ValidateRotationCandidateWorkspace(a WorkspaceAccess, w, u string, now time.Time) bool {
	if !ValidateWorkspaceAccess(a, w, now) {
		return false
	}
	raw, nested, expiry, err := rotationCredentialIdentity(a.AccessToken, w, u, now)
	if err != nil || a.ExpiresAt.After(expiry) {
		return false
	}
	// Reject duplicate scope/cookie representations, even if the legacy Web
	// hint validator would otherwise collapse them into one value.
	var admitted map[string]bool
	for _, source := range []struct {
		m   map[string]any
		key string
	}{
		{raw, "scope"}, {raw, "scp"}, {nested, "scope"}, {nested, "scp"},
		{raw, "https://api.openai.com/auth.scope"}, {raw, "https://api.openai.com/auth.scp"},
	} {
		if value, present := source.m[source.key]; present {
			current, ok := strictScopeSet(value)
			if !ok || admitted != nil && !samePersonalScopeSet(admitted, current) {
				return false
			}
			admitted = current
		}
	}
	seen := map[string]bool{}
	for _, cookie := range a.Cookies {
		if seen[strings.ToLower(cookie.Name)] {
			return false
		}
		seen[strings.ToLower(cookie.Name)] = true
	}
	return admitted != nil && admitted["organization.read"]
}
