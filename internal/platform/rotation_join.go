package platform

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// The two sourced invite routes. They are the only two upstream mutations this
// adapter is allowed to perform, and they are always issued as separate calls so
// the caller can persist a durable marker and re-fence the egress lease between
// them.
const (
	rotationJoinRequestSuffix = "/invites/request"
	rotationJoinAcceptSuffix  = "/invites/accept"
)

// rotationJoinAcceptCookie is the browser account-selection hint the reference
// CLI sets between the request and accept POSTs. The adapter owns it: callers
// must not pre-seed it, and the request stage strips any supplied value.
const rotationJoinAcceptCookie = "_accept_account_id"

// ErrRotationJoinUnavailable covers every admission, routing and transport
// failure of this adapter. Errors never carry upstream bodies, tokens, cookies
// or transport text, because transport errors can embed credentials.
var ErrRotationJoinUnavailable = errors.New("candidate join attempt unavailable")

// RotationCandidateJoiner is the narrow seam for inviting one candidate seat
// into an already authorised Team workspace. It authenticates only with a
// Personal session; it never logs in, never exchanges tokens, never performs
// workspace selection, and never claims membership or a deliverable.
//
// Each method performs exactly one POST and returns one bounded receipt. A
// result is never proof of remote success: the caller must verify membership and
// credentials separately under its own fence, persisting durable stage markers
// for request and accept and reusing the ORIGINAL account and workspace.
type RotationCandidateJoiner interface {
	RequestCandidateJoin(ctx context.Context, session PersonalSession, workspaceID string) (JoinAttemptResult, error)
	AcceptCandidateJoin(ctx context.Context, session PersonalSession, workspaceID string) (JoinAttemptResult, error)
}

// OfficialRotationCandidateJoiner is the sourced implementation behind the
// candidate-join seam. Client must lease one already-admitted egress transport;
// this adapter clones it, replaces the jar with a per-attempt isolated jar, and
// refuses to follow redirects or retry.
type OfficialRotationCandidateJoiner struct{ Client DiscoveryClient }

// rotationJoinGuard pins scheme, host, port, path, query and method for every
// attempt, including continuations, so a shared helper can never turn the
// adapter into an arbitrary-host or arbitrary-route request.
type rotationJoinGuard struct {
	next   http.RoundTripper
	path   string
	method string
}

func (g rotationJoinGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	u := req.URL
	if u == nil || req.Method != g.method || u.Scheme != "https" || u.Host != "chatgpt.com" || u.Port() != "" ||
		u.User != nil || u.Fragment != "" || u.Path != g.path || u.RawQuery != "" ||
		(req.Host != "" && req.Host != u.Host) {
		return nil, ErrRotationJoinUnavailable
	}
	return g.next.RoundTrip(req)
}

// RequestCandidateJoin sends the sourced invite request for the frozen
// workspace using the Personal bearer only.
func (j OfficialRotationCandidateJoiner) RequestCandidateJoin(ctx context.Context, session PersonalSession, workspaceID string) (JoinAttemptResult, error) {
	return j.stage(ctx, session, workspaceID, rotationJoinRequestSuffix)
}

// AcceptCandidateJoin sends the sourced invite accept for the frozen workspace
// using the Personal bearer only, with the workspace account-selection cookie
// set explicitly for this stage.
func (j OfficialRotationCandidateJoiner) AcceptCandidateJoin(ctx context.Context, session PersonalSession, workspaceID string) (JoinAttemptResult, error) {
	return j.stage(ctx, session, workspaceID, rotationJoinAcceptSuffix)
}

func (j OfficialRotationCandidateJoiner) stage(ctx context.Context, session PersonalSession, workspaceID, suffix string) (JoinAttemptResult, error) {
	// Nothing has been sent yet, so every failure up to the transport call
	// reports a provably-unsent attempt.
	unsent := JoinAttemptResult{}
	// Validate the supplied workspace unchanged: a padded or otherwise inexact
	// segment is rejected rather than silently normalized into a different one.
	if !validRotationJoinSession(session, time.Now()) || !validRotationJoinWorkspace(workspaceID) {
		return unsent, ErrRotationJoinUnavailable
	}
	// Cancellation is decided here, before the client is acquired and before any
	// egress lease is consumed for a request that can no longer be sent.
	if ctx == nil || ctx.Err() != nil {
		return unsent, ErrRotationJoinUnavailable
	}
	client, release, result, err := j.client(ctx, session, workspaceID, suffix)
	if release != nil {
		defer release()
	}
	if err != nil {
		return result, err
	}
	return j.do(ctx, client, session, workspaceID, suffix, result)
}

// client leases the admitted transport, isolates the cookie jar and returns the
// per-attempt clone. On failure it returns the conservative receipt the caller
// should keep (still unsent unless the transport was actually entered).
func (j OfficialRotationCandidateJoiner) client(ctx context.Context, session PersonalSession, workspaceID, suffix string) (*http.Client, func(), JoinAttemptResult, error) {
	unsent := JoinAttemptResult{}
	if j.Client == nil {
		return nil, nil, unsent, ErrRotationJoinUnavailable
	}
	base, release, err := j.Client(ctx)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, unsent, ErrRotationJoinUnavailable
	}
	if base == nil || base.Transport == nil || base.Timeout <= 0 {
		if release != nil {
			release()
		}
		return nil, nil, unsent, ErrRotationJoinUnavailable
	}
	// A fresh jar seeded only from the supplied Personal cookies. Ambient
	// cookies from the leased client are never inherited.
	jar, err := cookiejar.New(nil)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, unsent, ErrRotationJoinUnavailable
	}
	origin, err := url.Parse(selectedWorkspaceOrigin + "/")
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, unsent, ErrRotationJoinUnavailable
	}
	jar.SetCookies(origin, rotationJoinCookies(session, workspaceID, suffix))
	client := *base
	client.Jar = jar
	client.Transport = rotationJoinGuard{next: base.Transport, path: rotationJoinPath(workspaceID, suffix), method: http.MethodPost}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client, release, unsent, nil
}

func (j OfficialRotationCandidateJoiner) do(ctx context.Context, client *http.Client, session PersonalSession, workspaceID, suffix string, result JoinAttemptResult) (JoinAttemptResult, error) {
	path := rotationJoinPath(workspaceID, suffix)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, selectedWorkspaceOrigin+path, strings.NewReader("{}"))
	if err != nil {
		return result, ErrRotationJoinUnavailable
	}
	// Intentionally use a header subset and the project's browserAuthUA, not
	// the reference browser fingerprint (Sec-Ch-Ua hints and Priority omitted).
	// The effective root Referer and target headers match the sourced stages;
	// this does not claim byte-for-byte or live protocol compatibility.
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(session.AccessToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("oai-device-id", strings.TrimSpace(session.DeviceID))
	req.Header.Set("oai-language", "en-US")
	req.Header.Set("Origin", selectedWorkspaceOrigin)
	req.Header.Set("Referer", selectedWorkspaceOrigin+"/")
	req.Header.Set("X-Openai-Target-Path", path)
	req.Header.Set("X-Openai-Target-Route", "/backend-api/accounts/{account_id}"+suffix)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("User-Agent", browserAuthUA)
	req.Header.Set("Accept-Language", browserenv.AcceptLanguage)
	// Once Do is entered the attempt is never provably unsent, even if it
	// fails locally. No receipt or error can discharge the sent obligation.
	result.RequestSent = true
	result.RequestMayHaveEffect = true
	response, err := client.Do(req)
	if err != nil {
		return result, ErrRotationJoinUnavailable
	}
	if response == nil {
		return result, ErrRotationJoinUnavailable
	}
	if response.Body == nil {
		response.Body = io.NopCloser(strings.NewReader(""))
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if readErr != nil || len(body) > maxResponseBody || (len(body) > 0 && !json.Valid(body)) {
		// Partial, oversized or nonempty malformed receipts cannot classify
		// the attempt. Keep the sent obligation without exposing raw content.
		return result, ErrRotationJoinUnavailable
	}
	result.ErrorCode = upstreamCode(body)
	result.Semantic = classifyMembershipResponseSemantic(body)
	result.Success = response.StatusCode >= 200 && response.StatusCode < 300
	// 2xx, 404, conflict and ambiguous semantics are all receipts only. None of
	// them verifies membership or a usable deliverable.
	return result, nil
}

func rotationJoinPath(workspaceID, suffix string) string {
	return "/backend-api/accounts/" + url.PathEscape(strings.TrimSpace(workspaceID)) + suffix
}

// validRotationJoinWorkspace admits a single, safe path segment. Separators,
// traversal, whitespace, control characters and the empty string are rejected
// before any request is built.
func validRotationJoinWorkspace(workspaceID string) bool {
	if workspaceID == "" || len(workspaceID) > 255 || strings.TrimSpace(workspaceID) != workspaceID {
		return false
	}
	if strings.ContainsAny(workspaceID, "/\\?#% \t\r\n\x00") || workspaceID == "." || workspaceID == ".." {
		return false
	}
	return url.PathEscape(workspaceID) == workspaceID
}

// rotationJoinCookies seeds the isolated jar with admission-validated Personal
// cookies; names and values are carried exactly, never normalized or deduplicated.
// It fails closed on contradictory or stale workspace-selection hints: a hint
// that disagrees with the frozen workspace is removed rather than allowed to
// redirect the POST, and the accept stage sets exactly one explicit
// account-selection value. A supplied hint that already matches is left for the
// jar to carry unchanged.
func rotationJoinCookies(session PersonalSession, workspaceID, suffix string) []*http.Cookie {
	cookies := make([]*http.Cookie, 0, len(session.Cookies)+1)
	for _, cookie := range session.Cookies {
		if cookie.Host != "" && cookie.Host != "chatgpt.com" {
			continue
		}
		name := cookie.Name
		// Never trust a caller-supplied accept hint; the adapter owns that cookie
		// and writes its own value below.
		if name == rotationJoinAcceptCookie {
			continue
		}
		// A workspace-selection hint is only carried when it already agrees with
		// the frozen workspace; a contradictory or stale value is dropped so it
		// cannot redirect the POST to another account.
		if rotationJoinWorkspaceHint(name) && cookie.Value != workspaceID {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: name, Value: cookie.Value, Path: "/", Secure: true})
	}
	if suffix == rotationJoinAcceptSuffix {
		cookies = append(cookies, &http.Cookie{Name: rotationJoinAcceptCookie, Value: workspaceID, Path: "/", Secure: true})
	}
	return cookies
}

// rotationJoinWorkspaceHint reports whether a cookie name selects the account a
// backend-api invite POST is applied to. Values are still compared to the frozen
// workspace by the caller.
func rotationJoinWorkspaceHint(name string) bool {
	switch name {
	case "_account", "oai-workspace":
		return true
	default:
		return false
	}
}

// validRotationJoinCookies rejects ambiguous or invalid credentials before a
// client is acquired. Even identical duplicates are inadmissible; selecting or
// rewriting a cookie could substitute a different session generation.
func validRotationJoinCookies(cookies []SessionCookie) bool {
	seen := make(map[string]bool, len(cookies))
	for _, cookie := range cookies {
		host := cookie.Host
		if host == "" {
			host = "chatgpt.com"
		}
		if cookie.Name != strings.TrimSpace(cookie.Name) || seen[host+"/"+cookie.Name] {
			return false
		}
		if (&http.Cookie{Name: cookie.Name, Value: cookie.Value}).Valid() != nil {
			return false
		}
		seen[host+"/"+cookie.Name] = true
	}
	return true
}

// validRotationJoinSession requires the saved Personal generation to be complete
// and unexpired, and then requires the bearer itself to carry fresh post-TOTP
// Personal claims. Workspace-scoped JWT claims and flat/nested claim conflicts
// are rejected here, so a Workspace bearer can never authenticate this seam.
func validRotationJoinSession(session PersonalSession, now time.Time) bool {
	if !validRotationJoinCookies(session.Cookies) || !ValidatePersonalRefresh(PersonalRefreshResult{Status: "ready", Session: session}, now) {
		return false
	}
	expires, err := postTOTPPersonalExpiry(strings.TrimSpace(session.AccessToken), now)
	if err != nil {
		return false
	}
	if session.ExpiresAt.Before(now.Add(time.Minute)) {
		return false
	}
	return expires.After(now.Add(time.Minute))
}

// ValidateInvitationSession checks recipient readiness before the mother sends
// an invitation. It applies the same Personal claims as request and accept.
func ValidateInvitationSession(session PersonalSession, now time.Time) bool {
	return validRotationJoinSession(session, now)
}
