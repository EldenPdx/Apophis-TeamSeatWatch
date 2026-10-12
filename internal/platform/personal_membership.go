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
	"unicode"
)

// PersonalMembershipReader reads the immutable target workspace roster with a
// saved Personal session. It never accepts Basic credentials or performs a
// workspace/account mutation.
type PersonalMembershipReader interface {
	ReadMembers(context.Context, PersonalSession, string) (Result, error)
}

// OfficialPersonalMembershipReader is the sourced, read-only membership route.
// The caller supplies an admitted transport; this reader isolates its cookies
// and pins the request to the exact members path.
type OfficialPersonalMembershipReader struct{ Client DiscoveryClient }

func (OfficialPersonalMembershipReader) Source() string { return "personal_session_members" }

func (r OfficialPersonalMembershipReader) ReadMembers(ctx context.Context, session PersonalSession, workspace string) (Result, error) {
	result := Result{Endpoint: EndpointMembers, ObservedAt: time.Now().UTC(), Completeness: Unknown, Outcome: OutcomeIncomplete}
	fail := func(err error) (Result, error) { return result, err }
	if ctx == nil || ctx.Err() != nil || r.Client == nil || !validRotationJoinSession(session, time.Now()) || !validRotationJoinWorkspace(workspace) {
		return fail(errors.New("personal membership reader unavailable"))
	}
	for _, cookie := range session.Cookies {
		if cookie.Host != "" && cookie.Host != "chatgpt.com" {
			continue
		}
		if (rotationJoinWorkspaceHint(cookie.Name) || cookie.Name == rotationJoinAcceptCookie) && cookie.Value != workspace {
			return fail(errors.New("personal membership reader unavailable"))
		}
	}
	base, release, err := r.Client(ctx)
	if release != nil {
		defer release()
	}
	if err != nil || base == nil || base.Transport == nil || base.Timeout <= 0 {
		return fail(errors.New("personal membership reader unavailable"))
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return fail(errors.New("personal membership reader unavailable"))
	}
	origin, err := url.Parse(officialChatBase + "/")
	if err != nil {
		return fail(errors.New("personal membership reader unavailable"))
	}
	cookies := make([]*http.Cookie, 0, len(session.Cookies))
	for _, cookie := range session.Cookies {
		if cookie.Host != "" && cookie.Host != "chatgpt.com" {
			continue
		}
		if cookie.Name == "" || cookie.Value == "" {
			return fail(errors.New("personal membership reader unavailable"))
		}
		cookies = append(cookies, &http.Cookie{Name: cookie.Name, Value: cookie.Value, Path: "/", Secure: true})
	}
	jar.SetCookies(origin, cookies)
	client := *base
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	path := "/workspaces/" + url.PathEscape(workspace) + "/members"
	client.Transport = rotationJoinGuard{next: base.Transport, path: path, method: http.MethodGet}
	bounded, cancel := context.WithTimeout(ctx, min(base.Timeout, 10*time.Second))
	defer cancel()
	req, err := http.NewRequestWithContext(bounded, http.MethodGet, officialChatBase+path, nil)
	if err != nil {
		return fail(errors.New("personal membership reader unavailable"))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(session.AccessToken))
	req.Header.Set("oai-device-id", strings.TrimSpace(session.DeviceID))
	req.Header.Set("User-Agent", browserAuthUA)
	req.Header.Set("Accept-Language", browserenv.AcceptLanguage)
	response, err := client.Do(req)
	if err != nil || response == nil {
		return fail(errors.New("personal membership reader unavailable"))
	}
	if response.Body == nil {
		return fail(errors.New("personal membership reader unavailable"))
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil || len(body) > maxResponseBody {
		return result, nil
	}
	var payload struct {
		wireResponse
		WorkspaceID string `json:"workspace_id"`
	}
	if len(body) == 0 || !unambiguousMembershipJSON(body) || json.Unmarshal(body, &payload) != nil {
		return result, nil
	}
	result.Outcome = Classify(response.StatusCode, payload.ErrorCode, nil)
	if result.Outcome != OutcomeOperational {
		return result, nil
	}
	if payload.WorkspaceID != "" && payload.WorkspaceID != workspace || payload.Members == nil || len(payload.Members) > selectedMaxEntries {
		result.Outcome = OutcomeIncomplete
		return result, nil
	}
	for _, member := range payload.Members {
		if len(member.PlatformMemberID) > 255 || len(member.SeatType) > 64 || strings.ContainsAny(member.PlatformMemberID+member.Identifier, "\r\n\t\x00") {
			result.Outcome = OutcomeIncomplete
			return result, nil
		}
	}
	result.Members = payload.Members
	if payload.Complete != nil {
		if *payload.Complete {
			result.Completeness = Complete
		} else {
			result.Completeness = Partial
		}
	}
	members, valid := validMembers(payload.Members)
	result.Members = members
	if !valid {
		result.Outcome = OutcomeIncomplete
		result.Completeness = Partial
	}
	if result.Outcome == OutcomeOperational && payload.Complete == nil {
		result.Outcome = OutcomeIncomplete
	}
	return result, nil
}

// Conflicting repeated JSON keys cannot be interpreted as a complete roster.
// The bound also prevents pathological nesting within the bounded body.
func unambiguousMembershipJSON(body []byte) bool {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 16 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return false
				}
				name, ok := key.(string)
				// Match encoding/json's case-insensitive struct field lookup,
				// including Unicode SimpleFold equivalents.
				name = strings.Map(func(r rune) rune {
					folded := r
					for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
						if next < folded {
							folded = next
						}
					}
					return folded
				}, name)
				if !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for decoder.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}
