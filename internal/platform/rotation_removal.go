package platform

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OfficialWorkspaceMemberRemover is the narrow sourced Team Workspace removal
// adapter. It uses an already exchanged Workspace bearer and deletes exactly the
// frozen remote member id supplied by the authorization; it never logs in, never
// falls back to BasicAuth/self-leave, and never treats a transport receipt as an
// absent-verified slot.
type OfficialWorkspaceMemberRemover struct {
	Client                   DiscoveryClient
	ResolveMemberIdentifiers func(context.Context, []string) (map[string]string, error)
}

// WorkspaceMemberRemoval never confirms business vacancy. The runtime records
// a separate current complete snapshot under the authorization and lease fence.
type WorkspaceMemberRemoval interface {
	SnapshotWorkspaceMembers(context.Context, WorkspaceAccess, string) (ExactMemberSnapshot, error)
	RemoveWorkspaceMember(context.Context, WorkspaceAccess, string, string, string) (RemoveMemberResult, error)
}

func (r OfficialWorkspaceMemberRemover) client(ctx context.Context) (*http.Client, func(), error) {
	if r.Client == nil {
		return nil, nil, errors.New("workspace removal client unavailable")
	}
	base, release, err := r.Client(ctx)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, err
	}
	if base == nil || base.Transport == nil || base.Timeout <= 0 {
		if release != nil {
			release()
		}
		return nil, nil, errors.New("workspace removal client invalid")
	}
	client := *base
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client, release, nil
}

func validRemovalWorkspaceAccess(access WorkspaceAccess, workspaceID string, now time.Time) bool {
	workspaceID = strings.TrimSpace(workspaceID)
	return workspaceID != "" && workspaceID == strings.TrimSpace(access.WorkspaceID) &&
		strings.TrimSpace(access.AccessToken) != "" && strings.TrimSpace(access.DeviceID) != "" &&
		!strings.ContainsAny(access.AccessToken+access.DeviceID+workspaceID, "\r\n\x00") &&
		len(workspaceID) <= 255 && len(access.DeviceID) <= 128 && access.ExpiresAt.After(now.Add(30*time.Second))
}

// SnapshotWorkspaceMembers returns only a strict, current, paginated member
// snapshot for post-delete verification. Callers must still check authorization,
// owner role, frozen identities and DB evidence in the same commit that records
// absence.
func (r OfficialWorkspaceMemberRemover) SnapshotWorkspaceMembers(ctx context.Context, access WorkspaceAccess, workspaceID string) (ExactMemberSnapshot, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if !validRemovalWorkspaceAccess(access, workspaceID, time.Now()) {
		return ExactMemberSnapshot{}, errors.New("workspace access is not valid for removal snapshot")
	}
	client, release, err := r.client(ctx)
	if release != nil {
		defer release()
	}
	if err != nil {
		return ExactMemberSnapshot{}, err
	}
	// Vacancy evidence and mutation must use the same verified bearer identity.
	// Saved browser cookies are not proof of that identity; do not mix them in.
	access.Cookies = nil
	facts := SelectedWorkspaceFacts{Result: Result{ObservedAt: time.Now().UTC(), Completeness: Unknown, Outcome: OutcomeIncomplete}}
	members, ok := (selectedHTTPRead{client: client, access: access, workspaceID: workspaceID, resolveMemberIdentifiers: r.ResolveMemberIdentifiers}).members(ctx, &facts)
	if !ok {
		return ExactMemberSnapshot{}, errors.New("membership snapshot is not exact")
	}
	count := len(members)
	fact := Result{Endpoint: EndpointMembers, Outcome: OutcomeOperational, HTTPStatus: http.StatusOK, ObservedAt: time.Now().UTC(), Completeness: Complete, MemberCount: &count, Members: members}
	return ExactMemberSnapshot{Fact: fact, DeclaredMemberCount: count}, nil
}

// RemoveWorkspaceMember sends the official owner-session DELETE for exactly the
// supplied frozen remote member id. The localRequestID is intentionally not sent:
// no sourced upstream idempotency/request-id header is known.
func (r OfficialWorkspaceMemberRemover) RemoveWorkspaceMember(ctx context.Context, access WorkspaceAccess, workspaceID, frozenMemberID, localRequestID string) (RemoveMemberResult, error) {
	_ = localRequestID
	workspaceID = strings.TrimSpace(workspaceID)
	frozenMemberID = strings.TrimSpace(frozenMemberID)
	if !validRemovalWorkspaceAccess(access, workspaceID, time.Now()) || frozenMemberID == "" || len(frozenMemberID) > 255 || strings.ContainsAny(frozenMemberID, "\r\n\x00") {
		return RemoveMemberResult{}, errors.New("remove member target is invalid")
	}
	client, release, err := r.client(ctx)
	if release != nil {
		defer release()
	}
	if err != nil {
		return RemoveMemberResult{}, err
	}
	result := RemoveMemberResult{RequestMayHaveEffect: true}
	raw := selectedWorkspaceOrigin + "/backend-api/accounts/" + url.PathEscape(workspaceID) + "/users/" + url.PathEscape(frozenMemberID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, raw, nil)
	if err != nil || req.URL.Scheme != "https" || req.URL.Host != "chatgpt.com" || req.URL.User != nil || req.URL.Fragment != "" {
		return result, errors.New("remove member request is invalid")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(access.AccessToken))
	req.Header.Set("Chatgpt-Account-Id", workspaceID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", selectedWorkspaceOrigin)
	req.Header.Set("Referer", selectedWorkspaceOrigin+"/admin/members")
	req.Header.Set("User-Agent", browserAuthUA)
	req.Header.Set("Oai-Device-Id", strings.TrimSpace(access.DeviceID))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	// No browser cookies and no ambient jar: the Workspace bearer is the only
	// authentication credential for this destructive request.
	response, err := client.Do(req)
	if err != nil {
		result.Retryable = true
		return result, err
	}
	if response.Body == nil {
		response.Body = io.NopCloser(strings.NewReader(""))
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if readErr != nil || len(body) > maxResponseBody {
		result.Retryable = true
		return result, errors.New("remove member response is incomplete")
	}
	result.ErrorCode = upstreamCode(body)
	result.Accepted = response.StatusCode >= 200 && response.StatusCode < 300
	result.Retryable = response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
	result.RequestMayHaveEffect = requestMayHaveSideEffect(response.StatusCode, result.Accepted, result.ErrorCode)
	return result, nil
}
