package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

var ErrPersonalIdentityUnavailable = errors.New("personal identity confirmation unavailable")

// PersonalIdentity is read-only authenticated evidence for the exact supplied
// bearer. It is neither a replacement credential nor Workspace membership.
type PersonalIdentity struct {
	Identifier, SubjectID string
	ObservedAt, ExpiresAt time.Time
}
type PersonalIdentityConfirmer interface {
	ConfirmPersonalIdentity(context.Context, PersonalSession) (PersonalIdentity, error)
}
type OfficialPersonalIdentityConfirmer struct{ Client DiscoveryClient }

func (c OfficialPersonalIdentityConfirmer) ConfirmPersonalIdentity(ctx context.Context, s PersonalSession) (PersonalIdentity, error) {
	fail := func() (PersonalIdentity, error) { return PersonalIdentity{}, ErrPersonalIdentityUnavailable }
	if ctx == nil || ctx.Err() != nil || c.Client == nil || !validRotationJoinSession(s, time.Now()) {
		return fail()
	}
	base, release, err := c.Client(ctx)
	if release != nil {
		defer release()
	}
	if err != nil || base == nil || base.Transport == nil || base.Timeout <= 0 {
		return fail()
	}
	jar, err := personalCookieJar(s)
	if err != nil {
		return fail()
	}
	client := *base
	client.Jar = jar
	client.Timeout = min(base.Timeout, 10*time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Transport = rotationJoinGuard{next: base.Transport, path: "/api/auth/session", method: http.MethodGet}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(bounded, http.MethodGet, officialChatBase+"/api/auth/session", nil)
	if err != nil {
		return fail()
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", browserAuthUA)
	req.Header.Set("Accept-Language", browserenv.AcceptLanguage)
	response, err := client.Do(req)
	if err != nil || response == nil {
		return fail()
	}
	if response.Body == nil {
		return fail()
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fail()
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 {
		return fail()
	}
	var snapshot struct {
		AccessToken string `json:"accessToken"`
		Expires     string `json:"expires"`
		User        struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
	}
	if json.Unmarshal(body, &snapshot) != nil || snapshot.AccessToken != s.AccessToken {
		return fail()
	}
	now := time.Now().UTC()
	expiry, err := postTOTPPersonalExpiry(s.AccessToken, now)
	if err != nil {
		return fail()
	}
	sessionExpiry, err := time.Parse(time.RFC3339Nano, snapshot.Expires)
	if err != nil || !sessionExpiry.After(now.Add(time.Minute)) {
		return fail()
	}
	if sessionExpiry.Before(expiry) {
		expiry = sessionExpiry
	}
	if s.ExpiresAt.Before(expiry) {
		expiry = s.ExpiresAt
	}
	identity, err := personalBearerIdentity(s, snapshot.User.Email, snapshot.User.ID, now)
	if err != nil {
		return fail()
	}
	identity.ExpiresAt = expiry
	return identity, nil
}

// PersonalSessionIdentity checks the claims of an already validated, saved session.
// OAuth grant and liveness validation must still confirm this subject remotely.
func PersonalSessionIdentity(s PersonalSession) (PersonalIdentity, error) {
	if !validRotationJoinSession(s, time.Now()) {
		return PersonalIdentity{}, ErrPersonalIdentityUnavailable
	}
	return personalBearerIdentity(s, "", "", time.Now().UTC())
}

// IdentityAfterPersonalProbe binds the identity in this bearer to a successful
// authenticated usage read of the exact same bearer. It performs no login or OAuth.
func IdentityAfterPersonalProbe(s PersonalSession, evidence PersonalProbeEvidence) (PersonalIdentity, error) {
	if evidence.HTTPStatus != http.StatusOK || ClassifyPersonalProbe(evidence) != PersonalAvailable || !validRotationJoinSession(s, time.Now()) {
		return PersonalIdentity{}, ErrPersonalIdentityUnavailable
	}
	return personalBearerIdentity(s, "", "", time.Now().UTC())
}

func personalBearerIdentity(s PersonalSession, snapshotEmail, snapshotID string, now time.Time) (PersonalIdentity, error) {
	fail := func() (PersonalIdentity, error) { return PersonalIdentity{}, ErrPersonalIdentityUnavailable }
	expiry, err := postTOTPPersonalExpiry(s.AccessToken, now)
	if err != nil {
		return fail()
	}
	if s.ExpiresAt.Before(expiry) {
		expiry = s.ExpiresAt
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.Split(s.AccessToken, ".")[1])
	if err != nil {
		return fail()
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return fail()
	}
	authClaims, _ := claims["https://api.openai.com/auth"].(map[string]any)
	profile, _ := claims["https://api.openai.com/profile"].(map[string]any)
	user, err := consistentPersonalClaim(claims, authClaims, "chatgpt_user_id", false)
	if err != nil || len(user) > 255 {
		return fail()
	}
	sub, err := consistentPersonalClaim(claims, authClaims, "sub", false)
	if err != nil || len(sub) > 255 {
		return fail()
	}
	subject := user
	if subject == "" {
		subject = sub
	}
	if subject == "" || strings.ContainsAny(subject, "\r\n\t\x00") || snapshotID != "" && snapshotID != user && snapshotID != sub {
		return fail()
	}
	// chatgpt_user_id and sub are distinct representations, not conflicting
	// claims. Personal chatgpt_account_id is never a user identity.
	identifier := ""
	for _, candidate := range []any{claims["email"], authClaims["email"], profile["email"], claims["https://api.openai.com/profile.email"], claims["https://api.openai.com/auth.email"], snapshotEmail} {
		if candidate == nil {
			continue
		}
		text, ok := candidate.(string)
		if !ok {
			return fail()
		}
		if text == "" {
			continue
		}
		text = strings.ToLower(strings.TrimSpace(text))
		addr, err := mail.ParseAddress(text)
		if err != nil || addr.Address != text || len(text) > 320 || strings.ContainsAny(text, "\r\n\t\x00") || identifier != "" && text != identifier {
			return fail()
		}
		identifier = text
	}
	if identifier == "" {
		return fail()
	}
	return PersonalIdentity{Identifier: identifier, SubjectID: subject, ObservedAt: now, ExpiresAt: expiry}, nil
}
