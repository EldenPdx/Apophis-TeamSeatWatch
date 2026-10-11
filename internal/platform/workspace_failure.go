package platform

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
)

// WorkspaceExchangeFailure carries only classified facts. Upstream bodies,
// URLs, cookies, tokens and raw transport errors never reach its public fields.
type WorkspaceExchangeFailure struct {
	Code         string `json:"code"`
	HTTPStatus   int    `json:"httpStatus,omitempty"`
	UpstreamCode string `json:"upstreamCode,omitempty"`
	RouteMode    string `json:"routeMode,omitempty"`
	ProxyScheme  string `json:"proxyScheme,omitempty"`
	cause        error
}

func (f *WorkspaceExchangeFailure) Error() string { return f.Code }
func (f *WorkspaceExchangeFailure) Unwrap() []error {
	return []error{ErrWorkspaceExchangeUnavailable, f.cause}
}

func workspaceFailure(code string, status int, cause error) *WorkspaceExchangeFailure {
	return &WorkspaceExchangeFailure{Code: code, HTTPStatus: status, cause: cause}
}

// WorkspaceExchangeDiagnostic normalizes even injected adapters before their
// diagnostic is persisted or returned. Unknown errors never become user text.
func WorkspaceExchangeDiagnostic(err error) *WorkspaceExchangeFailure {
	if err == nil {
		return nil
	}
	var failure *WorkspaceExchangeFailure
	if errors.As(err, &failure) {
		result := *failure
		result.cause = nil
		switch result.Code {
		case "workspace_token_revoked", "workspace_no_longer_visible", "workspace_session_expired", "workspace_session_invalid", "proxy_capacity_exhausted", "proxy_egress_drift", "proxy_session_expired", "proxy_auth_rejected",
			"connection_capacity_exhausted", "connection_acquisition_timeout",
			"network_timeout", "network_dns_failed", "network_tls_failed", "network_error", "request_cancelled", "platform_browser_challenge", "platform_access_denied",
			"platform_rate_limited", "platform_unavailable", "platform_http_rejected", "platform_redirected", "platform_response_invalid", "workspace_personal_returned",
			"workspace_mismatch", "workspace_plan_unsupported", "workspace_read_scope_missing", "workspace_token_invalid", "workspace_token_expired", "workspace_exchange_unavailable":
		default:
			result.Code = "workspace_exchange_unavailable"
		}
		if result.HTTPStatus < 100 || result.HTTPStatus > 599 {
			result.HTTPStatus = 0
		}
		if result.Code != "workspace_token_revoked" || result.UpstreamCode != "token_revoked" || result.HTTPStatus != http.StatusUnauthorized {
			result.UpstreamCode = ""
		}
		if result.RouteMode != "required" && result.RouteMode != "direct" {
			result.RouteMode = ""
		}
		switch result.ProxyScheme {
		case "http", "https", "socks5", "socks5h":
		default:
			result.ProxyScheme = ""
		}
		if result.RouteMode != "required" {
			result.ProxyScheme = ""
		}
		return &result
	}
	var dns *net.DNSError
	var network net.Error
	var certificate x509.UnknownAuthorityError
	var hostname x509.HostnameError
	code := "network_error"
	switch {
	case errors.Is(err, egress.ErrLeaseExhausted):
		code = "proxy_capacity_exhausted"
	case errors.Is(err, egress.ErrEgressDrift):
		code = "proxy_egress_drift"
	case errors.Is(err, egress.ErrSessionExpired):
		code = "proxy_session_expired"
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout():
		code = "network_timeout"
	case errors.Is(err, context.Canceled):
		code = "request_cancelled"
	case errors.As(err, &dns):
		code = "network_dns_failed"
	case errors.As(err, &certificate) || errors.As(err, &hostname):
		code = "network_tls_failed"
	case errors.Is(err, ErrWorkspaceExchangeUnavailable):
		code = "workspace_exchange_unavailable"
	default:
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "proxy authentication required") || strings.Contains(message, "socks") && strings.Contains(message, "authentication failed") {
			code = "proxy_auth_rejected"
		}
	}
	return workspaceFailure(code, 0, nil)
}

func workspaceHTTPFailure(response *http.Response, body []byte) *WorkspaceExchangeFailure {
	status := response.StatusCode
	code := "platform_http_rejected"
	var upstream struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &upstream)
	switch {
	case response.Header.Get("Cf-Mitigated") == "challenge" || strings.Contains(string(body), "/cdn-cgi/challenge-platform/"):
		code = "platform_browser_challenge"
	case status == http.StatusUnauthorized && upstream.Error.Code == "token_revoked":
		return &WorkspaceExchangeFailure{Code: "workspace_token_revoked", HTTPStatus: status, UpstreamCode: "token_revoked"}
	case status == http.StatusUnauthorized:
		code = "workspace_session_expired"
	case status == http.StatusForbidden:
		code = "platform_access_denied"
	case status == http.StatusProxyAuthRequired:
		code = "proxy_auth_rejected"
	case status == http.StatusTooManyRequests:
		code = "platform_rate_limited"
	case status >= 500:
		code = "platform_unavailable"
	case status >= 300 && status < 400:
		code = "platform_redirected"
	}
	return workspaceFailure(code, status, nil)
}
