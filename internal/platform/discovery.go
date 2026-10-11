package platform

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// PersonalSession is a confirmed Personal credential generation. It must never
// be returned through the Owner API or confused with a Workspace OAuth token.
type PersonalSession struct {
	AccessToken      string          `json:"accessToken"`
	Cookies          []SessionCookie `json:"cookies"`
	DeviceID         string          `json:"deviceId"`
	ExpiresAt        time.Time       `json:"expiresAt"`
	SessionExpiresAt time.Time       `json:"sessionExpiresAt,omitempty"`
}

type SessionCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Host  string `json:"host,omitempty"`
}

type PersonalRefreshResult struct {
	Status  string // ready, invalid_login, refresh_failed
	Session PersonalSession
	Failure *PersonalRefreshFailure `json:"failure,omitempty"`
}

// PersonalRefreshFailure contains classified facts, never request URLs or materials.
type PersonalRefreshFailure struct {
	Code       string `json:"code"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
}

// PersonalSessionRefresher performs full password+TOTP authentication.
// Explicit account and delivery actions share acquisition; saves and probes never log in.
type PersonalSessionRefresher interface {
	RefreshPersonal(context.Context, MotherMaterial) (PersonalRefreshResult, error)
}

type DiscoveryAdapter interface {
	Discover(context.Context, PersonalSession) (DiscoveryResult, error)
}

type MotherMaterial struct {
	LoginIdentifier string
	Password        string
	TOTPSecret      string
}

type DiscoveredWorkspace struct {
	PlatformID string
	Name       string
	// Readable is never a grant of management permission. Role is independent
	// first-party discovery evidence and may still be unknown.
	Access string // readable, permission_denied, unknown
	Role   string // owner, admin, member, unknown
}

type DiscoveryResult struct {
	Status     string // discovered, session_expired, discovery_failed, permission_denied
	Workspaces []DiscoveredWorkspace
	Failure    *WorkspaceExchangeFailure
}

var ErrDiscoveryUnavailable = errors.New("platform discovery adapter unavailable")
var ErrPersonalRefreshUnavailable = errors.New("personal session refresh adapter unavailable")

type UnavailableDiscovery struct{}

func (UnavailableDiscovery) Discover(context.Context, PersonalSession) (DiscoveryResult, error) {
	return DiscoveryResult{}, ErrDiscoveryUnavailable
}

type UnavailablePersonalRefresh struct{}

func (UnavailablePersonalRefresh) RefreshPersonal(context.Context, MotherMaterial) (PersonalRefreshResult, error) {
	return PersonalRefreshResult{}, ErrPersonalRefreshUnavailable
}

func ValidatePersonalRefresh(result PersonalRefreshResult, now time.Time) bool {
	if result.Failure != nil {
		if result.Status != "refresh_failed" || result.Failure.HTTPStatus != 0 && (result.Failure.HTTPStatus < 100 || result.Failure.HTTPStatus > 599) {
			return false
		}
		switch result.Failure.Code {
		case "platform_browser_challenge", "platform_access_denied", "platform_rate_limited", "platform_unavailable", "network_timeout", "network_error", "login_request_failed", "login_task_interrupted", "login_task_timeout", "login_result_not_saved":
		default:
			return false
		}
	}
	if result.Status != "ready" {
		return (result.Status == "invalid_login" || result.Status == "refresh_failed") && result.Session.AccessToken == "" && len(result.Session.Cookies) == 0 && result.Session.DeviceID == "" && result.Session.ExpiresAt.IsZero() && result.Session.SessionExpiresAt.IsZero()
	}
	s := result.Session
	return s.ExpiresAt.After(now.Add(time.Minute)) && validPersonalCookies(s) &&
		(s.SessionExpiresAt.IsZero() || !s.ExpiresAt.After(s.SessionExpiresAt)) &&
		strings.TrimSpace(s.AccessToken) != "" && len(s.AccessToken) <= 16384 && !strings.ContainsAny(s.AccessToken, "\r\n\t\x00")
}

func validPersonalCookies(s PersonalSession) bool {
	if strings.TrimSpace(s.DeviceID) == "" || len(s.DeviceID) > 128 || strings.ContainsAny(s.DeviceID, "\r\n\t\x00") || len(s.Cookies) < 1 || len(s.Cookies) > 50 {
		return false
	}
	for _, cookie := range s.Cookies {
		if strings.TrimSpace(cookie.Name) == "" || len(cookie.Name) > 128 || cookie.Value == "" || len(cookie.Value) > 16384 || strings.ContainsAny(cookie.Name+cookie.Value, "\r\n\t;\x00") ||
			(cookie.Host != "" && cookie.Host != "chatgpt.com" && cookie.Host != "auth.openai.com") {
			return false
		}
	}
	return hasBrowserSessionCookie(s.Cookies)
}

// ValidateDiscovery rejects contradictory or unbounded adapter responses before persistence.
func ValidateDiscovery(result DiscoveryResult) bool {
	if result.Status == "discovered" && result.Failure != nil {
		return false
	}
	if len(result.Workspaces) > 1000 {
		return false
	}
	if result.Status != "discovered" {
		return len(result.Workspaces) == 0 && (result.Status == "session_expired" || result.Status == "discovery_failed" || result.Status == "permission_denied")
	}
	seen := make(map[string]bool, len(result.Workspaces))
	for _, item := range result.Workspaces {
		if strings.TrimSpace(item.PlatformID) == "" || len(item.PlatformID) > 255 || strings.TrimSpace(item.Name) == "" || len(item.Name) > 120 ||
			(item.Access != "readable" && item.Access != "permission_denied" && item.Access != "unknown") ||
			(item.Role != "" && item.Role != "owner" && item.Role != "admin" && item.Role != "member" && item.Role != "unknown") || seen[item.PlatformID] {
			return false
		}
		seen[item.PlatformID] = true
	}
	return true
}

// NextAuth splits large session cookies into numbered chunks. All chunks must
// be present; a stray prefix or a missing segment is not a saved session.
func hasBrowserSessionCookie(cookies []SessionCookie) bool {
	const name = "__Secure-next-auth.session-token"
	whole := false
	chunks := map[int]bool{}
	for _, cookie := range cookies {
		if cookie.Host != "" && cookie.Host != "chatgpt.com" {
			continue
		}
		if cookie.Name == name {
			if whole {
				return false
			}
			whole = true
			continue
		}
		if !strings.HasPrefix(cookie.Name, name+".") {
			continue
		}
		suffix := strings.TrimPrefix(cookie.Name, name+".")
		index, err := strconv.Atoi(suffix)
		if err != nil || index < 0 || index >= 50 || strconv.Itoa(index) != suffix || chunks[index] {
			return false
		}
		chunks[index] = true
	}
	if whole {
		return len(chunks) == 0
	}
	if len(chunks) == 0 {
		return false
	}
	for index := 0; index < len(chunks); index++ {
		if !chunks[index] {
			return false
		}
	}
	return true
}
