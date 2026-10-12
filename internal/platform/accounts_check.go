package platform

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const accountsCheckURL = "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27?timezone_offset_min=0"

// DiscoveryClient leases one already-admitted transport for this read attempt.
// A production caller must supply an egress lease, not http.DefaultClient.
type DiscoveryClient func(context.Context) (*http.Client, func(), error)

type AccountsCheckDiscovery struct{ Client DiscoveryClient }

func (a AccountsCheckDiscovery) Discover(ctx context.Context, session PersonalSession) (DiscoveryResult, error) {
	if a.Client == nil || !ValidatePersonalRefresh(PersonalRefreshResult{Status: "ready", Session: session}, time.Now()) {
		return DiscoveryResult{Status: "discovery_failed"}, nil
	}
	source, release, err := a.Client(ctx)
	if err != nil {
		return DiscoveryResult{Status: "discovery_failed"}, nil
	}
	if release != nil {
		defer release()
	}
	if source == nil || source.Transport == nil || source.Timeout <= 0 {
		return DiscoveryResult{Status: "discovery_failed"}, nil
	}
	jar, err := personalCookieJar(session)
	if err != nil {
		return DiscoveryResult{}, err
	}
	client := *source
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, accountsCheckURL, nil)
	if err != nil {
		return DiscoveryResult{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)
	req.Header.Set("oai-device-id", session.DeviceID)
	req.Header.Set("User-Agent", browserAuthUA)
	req.Header.Set("Accept-Language", browserenv.AcceptLanguage)
	resp, err := client.Do(req)
	if err != nil {
		return DiscoveryResult{Status: "discovery_failed", Failure: WorkspaceExchangeDiagnostic(err)}, nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cf-Mitigated") == "challenge" || strings.Contains(string(body), "/cdn-cgi/challenge-platform/") {
		failure := workspaceHTTPFailure(resp, body)
		status := "discovery_failed"
		if failure.Code == "workspace_session_expired" || failure.Code == "workspace_token_revoked" {
			status = "session_expired"
		} else if failure.Code == "platform_access_denied" {
			status = "permission_denied"
		}
		return DiscoveryResult{Status: status, Failure: failure}, nil
	}
	if err != nil || len(body) > maxResponseBody {
		return DiscoveryResult{Status: "discovery_failed", Failure: workspaceFailure("platform_response_invalid", resp.StatusCode, nil)}, nil
	}
	workspaces, err := ParseAccountsCheckWorkspaces(body)
	if err != nil {
		return DiscoveryResult{Status: "discovery_failed", Failure: workspaceFailure("platform_response_invalid", resp.StatusCode, nil)}, nil
	}
	return DiscoveryResult{Status: "discovered", Workspaces: workspaces}, nil
}

type accountsRoleFields struct {
	WorkspaceRole      string `json:"workspace_role"`
	WorkspaceRoleCamel string `json:"workspaceRole"`
	AccountRole        string `json:"account_role"`
	AccountRoleCamel   string `json:"accountRole"`
	Role               string `json:"role"`
}

type accountsAccountFields struct {
	ID        string `json:"account_id"`
	Name      string `json:"name"`
	Plan      string `json:"plan_type"`
	Structure string `json:"structure"`
	Kind      string `json:"kind"`
	accountsRoleFields
	Membership          *accountsRoleFields `json:"membership"`
	WorkspaceMembership *accountsRoleFields `json:"workspace_membership"`
	User                *accountsRoleFields `json:"user"`
}

// ParseAccountsCheckWorkspaces accepts the sourced accounts map, not HTML,
// invitation candidates or a client-provided workspace ID. Missing facts fail closed.
func ParseAccountsCheckWorkspaces(data []byte) ([]DiscoveredWorkspace, error) {
	var payload struct {
		Accounts map[string]struct {
			Account *accountsAccountFields `json:"account"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.Accounts == nil {
		return nil, errors.New("accounts check response missing accounts map")
	}
	if len(payload.Accounts) > 1000 {
		return nil, errors.New("accounts check response too large")
	}
	keys := make([]string, 0, len(payload.Accounts))
	for key := range payload.Accounts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	workspaces := make([]DiscoveredWorkspace, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		item := payload.Accounts[key].Account
		if item == nil {
			return nil, errors.New("accounts check account missing")
		}
		id, name := strings.TrimSpace(item.ID), strings.TrimSpace(item.Name)
		plan, kind := strings.ToLower(strings.TrimSpace(item.Plan)), strings.ToLower(strings.TrimSpace(item.Kind))
		structure := strings.ToLower(strings.TrimSpace(item.Structure))
		isExcluded := func(value string) bool { return value == "personal" || value == "free" || value == "default" }
		if isExcluded(strings.ToLower(strings.TrimSpace(key))) || isExcluded(strings.ToLower(id)) ||
			isExcluded(plan) || isExcluded(kind) || isExcluded(structure) {
			continue
		}
		// Team/Business are the only evidenced target plans. Unknown plans or
		// kinds cannot become selectable merely because structure says workspace.
		if structure != "workspace" || (plan != "team" && plan != "business") ||
			(kind != "" && kind != "workspace" && kind != "team") {
			return nil, errors.New("accounts check Team plan or kind unknown")
		}
		if id == "" || name == "" || len(id) > 255 || len(name) > 120 {
			return nil, errors.New("accounts check workspace identity incomplete")
		}
		if !seen[id] {
			role := roleFromFields(&item.accountsRoleFields)
			for _, nested := range []*accountsRoleFields{item.Membership, item.WorkspaceMembership, item.User} {
				if role == "" {
					role = roleFromFields(nested)
				}
			}
			workspaces = append(workspaces, DiscoveredWorkspace{PlatformID: id, Name: name, Access: "readable", Role: NormalizeWorkspaceRole(role)})
			seen[id] = true
		}
	}
	return workspaces, nil
}

func roleFromFields(fields *accountsRoleFields) string {
	if fields == nil {
		return ""
	}
	return firstNonEmpty(fields.WorkspaceRole, fields.WorkspaceRoleCamel, fields.AccountRole, fields.AccountRoleCamel, fields.Role)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// NormalizeWorkspaceRole maps first-party member and discovery roles to one contract.
func NormalizeWorkspaceRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "owner", "workspace_owner", "workspace owner", "primary_owner", "primary owner", "account-owner":
		return "owner"
	case "admin", "administrator", "account-admin":
		return "admin"
	case "member", "user", "standard", "regular", "standard-user":
		return "member"
	default:
		return "unknown"
	}
}
