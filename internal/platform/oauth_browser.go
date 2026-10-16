package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/chromedp/cdproto/network"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
)

// WorkspaceOAuthBrowserFactory binds one authorization browser to the existing
// network lease and the operation's exact workspace. Personal login stays separate.
type WorkspaceOAuthBrowserFactory func(context.Context, *http.Client, http.CookieJar, string, browserenv.Environment) (http.RoundTripper, func(), error)

type workspaceOAuthBrowser struct {
	document  personalBrowserTransport
	workspace string
	state     atomic.Pointer[string]
	terminal  atomic.Pointer[string]
	selection atomic.Pointer[string]
}

func readAndRestoreBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, errors.New("OAuth request body absent")
	}
	data, err := io.ReadAll(io.LimitReader(req.Body, 1025))
	req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(data))
	if err != nil || len(data) > 1024 {
		return nil, errors.New("OAuth request body invalid")
	}
	return data, nil
}

func NewWorkspaceOAuthBrowser(ctx context.Context, source *http.Client, jar http.CookieJar, workspace string, environment browserenv.Environment) (http.RoundTripper, func(), error) {
	if jar == nil || strings.TrimSpace(workspace) == "" {
		return nil, nil, errors.New("OAuth browser input invalid")
	}
	t := &workspaceOAuthBrowser{workspace: workspace}
	t.document.jar = jar
	t.document.validateContinuation = t.validate
	t.document.terminalURL = func() string {
		if v := t.terminal.Load(); v != nil {
			return *v
		}
		return ""
	}
	browser, closeBrowser, _, err := personalBrowser(ctx, source, t.allowed, environment)
	if err != nil {
		return nil, nil, err
	}
	t.document.browser = browser
	response, _, err := t.document.navigate(browser, officialChatBase+"/")
	if err == nil {
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			err = &authHTTPError{status: response.StatusCode, browserChallenge: response.Header.Get("Cf-Mitigated") == "challenge"}
		}
	}
	if err == nil {
		for _, entry := range []string{officialAuthBase + "/log-in", officialChatBase + "/"} {
			response, _, err = t.document.navigate(browser, entry)
			if err != nil {
				break
			}
			response.Body.Close()
			if response.StatusCode != 200 {
				err = &authHTTPError{status: response.StatusCode}
				break
			}
		}
	}
	if err == nil {
		err = t.document.restoreCookies(browser)
	}
	if err != nil {
		closeBrowser()
		return nil, nil, err
	}
	return t, closeBrowser, nil
}

func (t *workspaceOAuthBrowser) validate(req *http.Request) error {
	deny := errors.New("OAuth browser request denied")
	if req == nil || req.URL == nil || req.URL.User != nil || req.URL.Scheme != "https" || req.URL.Port() != "" || req.Host != "" && req.Host != req.URL.Host {
		return deny
	}
	u := req.URL
	if req.Method == http.MethodGet {
		if u.Hostname() == "chatgpt.com" && (u.Path == "/" || u.Path == "/api/auth/callback/openai" || u.Path == "/auth/login") {
			return nil
		}
		if u.Hostname() != "auth.openai.com" {
			return deny
		}
		switch u.Path {
		case "/", "/error", "/log-in", "/log-in/password", "/login", "/choose-an-account", "/workspace", "/api/accounts/client_auth_session_dump", "/sign-in-with-chatgpt/codex/consent", "/codex/consent":
			return nil
		case "/oauth/authorize":
			q := u.Query()
			stateMatches := t.state.Load() == nil || *t.state.Load() == q.Get("state")
			if stateMatches && len(q["state"]) == 1 && q.Get("allowed_workspace_id") == t.workspace && q.Get("client_id") == CodexClientID && q.Get("redirect_uri") == CodexRedirectURI && q.Get("state") != "" && q.Get("code_challenge") != "" && q.Get("code_challenge_method") == "S256" {
				return nil
			}
		}
	}
	if req.Method == http.MethodPost && u.Hostname() == "auth.openai.com" && t.state.Load() != nil {
		if u.Path != "/api/accounts/workspace/select" && u.Path != "/api/accounts/session/select" {
			return deny
		}
		data, err := readAndRestoreBody(req)
		if err != nil {
			return deny
		}
		var values map[string]string
		if json.Unmarshal(data, &values) != nil || len(values) != 1 {
			return deny
		}
		if u.Path == "/api/accounts/workspace/select" && values["workspace_id"] == t.workspace {
			return nil
		}
		if u.Path == "/api/accounts/session/select" {
			if selected := t.selection.Load(); selected != nil && values["session_id"] == *selected {
				return nil
			}
		}
	}
	return deny
}

func (t *workspaceOAuthBrowser) allowed(req *network.Request) bool {
	if req == nil {
		return false
	}
	if state := t.state.Load(); state != nil && req.Method == http.MethodGet {
		if code, err := authorizationCallbackCode(req.URL, *state); err == nil && code != "" {
			terminal := req.URL
			t.terminal.Store(&terminal)
			return false
		}
	}
	parsed, err := url.Parse(req.URL)
	if err != nil {
		return false
	}
	if parsed.Hostname() != "chatgpt.com" && parsed.Hostname() != "auth.openai.com" || strings.HasPrefix(parsed.Path, "/cdn-cgi/") {
		return allowedPersonalBrowserRequest(req)
	}
	var body []byte
	for _, entry := range req.PostDataEntries {
		if entry == nil || len(body)+len(entry.Bytes) > 1024 {
			return false
		}
		body = append(body, entry.Bytes...)
	}
	request, err := http.NewRequest(req.Method, req.URL, bytes.NewReader(body))
	return err == nil && t.validate(request) == nil
}

func (t *workspaceOAuthBrowser) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL != nil && req.URL.Path == "/api/accounts/session/select" {
		data, err := readAndRestoreBody(req)
		var values map[string]string
		if err != nil || json.Unmarshal(data, &values) != nil || accountSessionPattern.FindString(values["session_id"]) != values["session_id"] || values["session_id"] == "" {
			return nil, errors.New("OAuth account selection invalid")
		}
		selected := values["session_id"]
		t.selection.Store(&selected)
		defer t.selection.Store(nil)
	}
	if err := t.validate(req); err != nil {
		return nil, err
	}
	t.document.mu.Lock()
	defer t.document.mu.Unlock()
	t.terminal.Store(nil)
	if req.URL.Path == "/oauth/authorize" {
		state := req.URL.Query().Get("state")
		t.state.Store(&state)
	}
	ctx, cancel := context.WithCancel(t.document.browser)
	defer cancel()
	stop := context.AfterFunc(req.Context(), cancel)
	defer stop()
	var response *http.Response
	var err error
	if req.Method == http.MethodGet && !strings.HasPrefix(req.URL.Path, "/api/") {
		response, _, err = t.document.navigate(ctx, req.URL.String())
	} else {
		response, err = t.document.fetch(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	if response.Request != nil && response.Request.URL != nil && req.URL.Path != "/log-in" && t.state.Load() != nil {
		path := response.Request.URL.Path
		if path == "/log-in" || path == "/log-in/password" || path == "/login" {
			response.Body.Close()
			return nil, ErrBrowserSessionExpired
		}
	}
	if err = t.document.copyCookies(ctx); err != nil {
		response.Body.Close()
		return nil, err
	}
	return response, nil
}
