package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// WorkspaceInvitation uses the selected mother's Workspace bearer. Seat class
// is explicit, and HTTP success alone never proves the invitation exists.
type WorkspaceInvitation struct {
	Client           *http.Client
	Access           WorkspaceAccess
	Workspace        string
	RecipientSubject string
}

var errWorkspaceInvitation = errors.New("workspace invitation unavailable")

func (a WorkspaceInvitation) roster(ctx context.Context) ([]Member, error) {
	if a.Client == nil || a.Client.Transport == nil || !ValidateWorkspaceAccess(a.Access, a.Workspace, time.Now()) {
		return nil, errWorkspaceInvitation
	}
	client := *a.Client
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	reader := selectedHTTPRead{client: &client, access: a.Access, workspaceID: a.Workspace}
	facts := SelectedWorkspaceFacts{}
	members, memberOutcome := reader.memberEntries(ctx)
	invites, inviteOK := reader.invites(ctx, &facts)
	if memberOutcome != OutcomeOperational || !inviteOK {
		return nil, errWorkspaceInvitation
	}
	return append(members, invites...), nil
}

func (a WorkspaceInvitation) Membership(ctx context.Context, identifier string) (MembershipResult, error) {
	result := MembershipResult{ObservedAt: time.Now().UTC()}
	if len(a.RecipientSubject) > 255 || strings.ContainsAny(a.RecipientSubject, "\r\n\t\x00") {
		return result, errWorkspaceInvitation
	}
	entries, err := a.roster(ctx)
	if err != nil {
		return result, err
	}
	result.Complete = true
	result.InvitationListComplete = true
	for _, entry := range entries {
		memberMatches := entry.Kind == "member" && (a.RecipientSubject != "" && entry.PlatformMemberID == a.RecipientSubject || a.RecipientSubject == "" && strings.EqualFold(entry.Identifier, identifier))
		inviteMatches := entry.Kind == "pending_invite" && strings.EqualFold(entry.Identifier, identifier)
		if memberMatches || inviteMatches {
			if result.Present || result.InvitationPresent {
				result.Complete = false
				return result, errWorkspaceInvitation
			}
			if memberMatches {
				if entry.Status != "listed" {
					result.Complete = false
					result.ErrorCode = "member_not_confirmed"
					return result, nil
				}
				result.Present, result.PlatformMemberID, result.SeatType = true, entry.PlatformMemberID, entry.SeatType
			} else if entry.Kind == "pending_invite" {
				result.InvitationPresent, result.InvitationSeatType = true, entry.SeatType
			}
		}
	}
	return result, nil
}

func premiumInvitation(entries []Member, identifier, subject string) (bool, bool) {
	found := false
	for _, entry := range entries {
		memberMatches := entry.Kind == "member" && (subject != "" && entry.PlatformMemberID == subject || subject == "" && strings.EqualFold(entry.Identifier, identifier))
		inviteMatches := entry.Kind == "pending_invite" && strings.EqualFold(entry.Identifier, identifier)
		if memberMatches || inviteMatches {
			if entry.SeatType != "prolite" || memberMatches && entry.Status != "listed" {
				return false, false
			}
			found = true
		}
	}
	return found, true
}

func (a WorkspaceInvitation) Send(ctx context.Context, identifier string) (JoinAttemptResult, error) {
	result := JoinAttemptResult{}
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	if identifier == "" || len(identifier) > 254 || strings.ContainsAny(identifier, "\r\n\x00") {
		return result, errWorkspaceInvitation
	}
	entries, err := a.roster(ctx)
	if err != nil {
		return result, err
	}
	found, valid := premiumInvitation(entries, identifier, a.RecipientSubject)
	if !valid {
		result.ErrorCode = "seat_type_mismatch"
		return result, nil
	}
	if found {
		result.Success = true
		return result, nil
	}
	reader := selectedHTTPRead{client: a.Client, access: a.Access, workspaceID: a.Workspace}
	facts := SelectedWorkspaceFacts{}
	_, _, entitlements, subscriptionOK := reader.subscription(ctx, &facts)
	occupants, countsOK := reader.counts(ctx, &facts)
	reserved := 0
	for _, entry := range entries {
		if entry.Kind == "pending_invite" && entry.SeatType == "prolite" {
			reserved++
		}
	}
	capacity := PremiumInvitationCapacity(entitlements, occupants, reserved)
	if !subscriptionOK || !countsOK || !capacity.Known {
		result.ErrorCode = "premium_capacity_unknown"
		return result, nil
	}
	if capacity.Remaining == 0 {
		result.ErrorCode = "premium_capacity_exceeded"
		return result, nil
	}
	payload, _ := json.Marshal(map[string]any{"email_addresses": []string{identifier}, "role": "standard-user", "seat_type": "prolite", "flow_id": uuid.NewString(), "submission_id": uuid.NewString(), "resend_emails": false})
	path := "/backend-api/accounts/" + url.PathEscape(a.Workspace) + "/invites"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, selectedWorkspaceOrigin+path, bytes.NewReader(payload))
	if err != nil {
		return result, errWorkspaceInvitation
	}
	req.Header.Set("Authorization", "Bearer "+a.Access.AccessToken)
	req.Header.Set("chatgpt-account-id", a.Workspace)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", browserAuthUA)
	req.Header.Set("Accept-Language", browserenv.AcceptLanguage)
	req.Header.Set("Origin", selectedWorkspaceOrigin)
	req.Header.Set("Referer", selectedWorkspaceOrigin+"/admin/members?tab=members")
	req.Header.Set("Oai-Device-Id", a.Access.DeviceID)
	req.Header.Set("Oai-Session-Id", uuid.NewString())
	req.Header.Set("Oai-Client-Build-Number", "9432397")
	req.Header.Set("Oai-Client-Version", "prod-1b777b39db92d7476d69bf3cd407f6b9a41f7410")
	req.Header.Set("Oai-Language", "zh-CN")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("X-Oai-Is-Client-Observation", "null")
	req.Header.Set("X-Oai-Is-Pending-Updates", `{"v":3,"updates":[]}`)
	req.Header.Set("X-OpenAI-Target-Path", path)
	req.Header.Set("X-OpenAI-Target-Route", "/backend-api/accounts/{account_id}/invites")
	for _, cookie := range a.Access.Cookies {
		req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	client := *a.Client
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	result.RequestSent, result.RequestMayHaveEffect = true, true
	response, err := client.Do(req)
	if err != nil {
		return result, errWorkspaceInvitation
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if readErr != nil || len(body) > maxResponseBody {
		return result, errWorkspaceInvitation
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.ErrorCode = "invitation_rejected"
		if upstreamCode(body) == "workspace_subscription_delinquent" {
			result.ErrorCode = "workspace_subscription_delinquent"
		}
		result.RequestMayHaveEffect = response.StatusCode == 403 || response.StatusCode == 409 || response.StatusCode == 429 || response.StatusCode >= 500
		return result, nil
	}
	entries, err = a.roster(ctx)
	if err != nil {
		return result, errWorkspaceInvitation
	}
	found, valid = premiumInvitation(entries, identifier, a.RecipientSubject)
	if !valid {
		result.ErrorCode = "seat_type_mismatch"
		return result, nil
	}
	result.Success = found
	if !found {
		result.ErrorCode = "invitation_unconfirmed"
	}
	return result, nil
}
