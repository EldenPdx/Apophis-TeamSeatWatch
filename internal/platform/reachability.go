package platform

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
)

// Pool maintenance and draft diagnostics share a bounded browser budget.
var reachabilityBrowsers = make(chan struct{}, 2)

// ProbeBrowserReachability opens an anonymous first-party document on the
// supplied transport, using the same browser route and navigation as login.
// It neither restores account cookies nor submits account or Workspace actions.
func ProbeBrowserReachability(ctx context.Context, source *http.Client, target string) (egress.ReachabilityResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil || validateReachabilityRequest(request) != nil {
		return egress.ReachabilityResult{Code: "proxy_transport_invalid"}, nil
	}
	select {
	case reachabilityBrowsers <- struct{}{}:
		defer func() { <-reachabilityBrowsers }()
	case <-ctx.Done():
		return egress.ReachabilityResult{}, ctx.Err()
	}
	probeCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	// Diagnostics have no account or credentials. Their explicit disposable
	// profile uses the same qualified engine and never enters a business factory.
	environment, err := browserenv.New()
	if err != nil {
		return egress.ReachabilityResult{}, err
	}
	directory, err := os.MkdirTemp("", "tsw-proxy-browser-")
	if err != nil {
		return egress.ReachabilityResult{}, err
	}
	defer os.RemoveAll(directory)
	browser, closeBrowser, route, err := launchBrowser(probeCtx, source, allowedReachabilityBrowserRequest, environment, directory, nil)
	if err != nil {
		if ctx.Err() != nil {
			return egress.ReachabilityResult{}, ctx.Err()
		}
		return egress.ReachabilityResult{Code: "proxy_browser_unavailable"}, nil
	}
	defer closeBrowser()
	document := personalBrowserTransport{browser: browser, validateContinuation: validateReachabilityRequest}
	response, _, navigateErr := document.navigate(browser, target)
	if response != nil {
		defer response.Body.Close()
	}
	if ctx.Err() != nil {
		return egress.ReachabilityResult{}, ctx.Err()
	}
	if response == nil {
		if failure, ok := route.Failure("chatgpt.com:443"); ok {
			return egress.ReachabilityResult{HTTPStatus: failure.HTTPStatus, Code: failure.Code}, nil
		}
	}
	return reachabilityOutcome(response, navigateErr), nil
}

func reachabilityOutcome(response *http.Response, navigateErr error) egress.ReachabilityResult {
	if response == nil {
		return egress.ReachabilityResult{Code: "proxy_browser_verification_incomplete"}
	}
	result := egress.ReachabilityResult{HTTPStatus: response.StatusCode}
	if response.Header.Get("Cf-Mitigated") == "challenge" {
		result.Code = "proxy_browser_challenge"
	} else if navigateErr != nil && response.StatusCode < http.StatusBadRequest {
		result.Code = "proxy_browser_verification_incomplete"
	}
	return result
}

func validateReachabilityRequest(request *http.Request) error {
	if request == nil || request.URL == nil || request.Method != http.MethodGet || request.URL.Scheme != "https" || request.URL.Host != "chatgpt.com" || request.URL.User != nil || request.URL.Fragment != "" || (request.URL.Path != "" && request.URL.Path != "/") {
		return errors.New("platform reachability continuation denied")
	}
	return nil
}

func allowedReachabilityBrowserRequest(request *network.Request) bool {
	if request == nil {
		return false
	}
	u, err := url.Parse(request.URL)
	if err != nil {
		return false
	}
	if u.Scheme == "data" || u.Scheme == "blob" {
		return true
	}
	if u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	switch u.Hostname() {
	case "chatgpt.com":
		if strings.HasPrefix(u.Path, "/cdn-cgi/") {
			return request.Method == http.MethodGet || request.Method == http.MethodPost
		}
		return request.Method == http.MethodGet && (u.Path == "/" || strings.HasPrefix(u.Path, "/_next/") || strings.HasPrefix(u.Path, "/assets/"))
	case "cdn.oaistatic.com", "auth-cdn.oaistatic.com":
		return request.Method == http.MethodGet
	case "challenges.cloudflare.com", "sentinel.openai.com":
		return request.Method == http.MethodGet || request.Method == http.MethodPost
	}
	return false
}
