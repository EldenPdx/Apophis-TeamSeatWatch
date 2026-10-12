package platform

import (
	"context"
	"errors"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/network"
)

var fixtureEnvironment = func() browserenv.Environment {
	e, err := browserenv.New()
	if err != nil {
		panic(err)
	}
	return e
}()

func TestPersonalRefreshInitializesBothHostsOnItsReservedClient(t *testing.T) {
	fixture := &personalFlowFixture{token: personalFixtureToken(time.Now(), nil)}
	client := &http.Client{Transport: accountsRoundTrip(func(req *http.Request) (*http.Response, error) {
		if _, err := req.Cookie("__cf_bm"); err != nil {
			t.Fatal("first-party login request omitted initialized browser cookies")
		}
		return fixture.RoundTrip(req)
	}), Timeout: time.Second}
	releases := 0
	adapter := PersonalWebRefresher{
		Client: func(context.Context) (*http.Client, func(), error) { return client, func() { releases++ }, nil },
		Browser: func(ctx context.Context, source *http.Client, jar http.CookieJar, _ browserenv.Environment) (http.RoundTripper, func(), error) {
			if source != client || ctx.Err() != nil {
				t.Fatal("browser initializer did not use the already leased client and context")
			}
			for _, host := range []string{"chatgpt.com", "auth.openai.com"} {
				origin, _ := url.Parse("https://" + host + "/")
				jar.SetCookies(origin, []*http.Cookie{{Name: "__cf_bm", Value: "browser-cookie", Secure: true}})
			}
			return source.Transport, func() {}, nil
		},
	}
	result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "account@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
	if err != nil || result.Status != "ready" || releases != 1 {
		t.Fatalf("status=%s releases=%d error=%v", result.Status, releases, err)
	}
	for _, host := range []string{"chatgpt.com", "auth.openai.com"} {
		found := false
		for _, cookie := range result.Session.Cookies {
			found = found || cookie.Host == host && cookie.Name == "__cf_bm"
		}
		if !found {
			t.Fatalf("initialized cookies missing from saved %s session", host)
		}
	}
}

func TestPersonalRefreshInitializationFailureNeverSubmitsCredentials(t *testing.T) {
	for _, initialize := range []PersonalBrowserFactory{func(context.Context, *http.Client, http.CookieJar, browserenv.Environment) (http.RoundTripper, func(), error) {
		return nil, nil, errors.New("browser initialization failed")
	}} {
		fixture := &personalFlowFixture{}
		released := false
		adapter := PersonalWebRefresher{Client: func(context.Context) (*http.Client, func(), error) {
			return &http.Client{Transport: fixture, Timeout: time.Second}, func() { released = true }, nil
		}, Browser: initialize}
		result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "account@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
		if err != nil || result.Status != "refresh_failed" || len(fixture.paths) != 0 || !released {
			t.Fatalf("initialization failed open: status=%s requests=%d released=%t", result.Status, len(fixture.paths), released)
		}
	}
}

func TestBrowserSessionCookiesRejectUnrelatedHostsAndInvalidValues(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	seedBrowserCookies(jar, []*network.Cookie{{Name: "session", Value: "good", Domain: ".chatgpt.com"}, {Name: "evil", Value: "bad", Domain: ".example.com"}, {Name: "quoted", Value: `"tracking"`, Domain: ".chatgpt.com"}})
	origin, _ := url.Parse("https://chatgpt.com/")
	saved := jar.Cookies(origin)
	if len(saved) != 1 || saved[0].Name != "session" {
		t.Fatal("browser cookie scope or value corrupted")
	}
}

func TestBrowserInitializationPreservesHostCookieWhenParentHasSameName(t *testing.T) {
	parent := &network.Cookie{Name: "__cf_bm", Value: "parent-cookie", Domain: ".openai.com"}
	child := &network.Cookie{Name: "__cf_bm", Value: "auth-cookie", Domain: ".auth.openai.com"}
	for _, cookies := range [][]*network.Cookie{{child, parent}, {parent, child}} {
		jar, _ := cookiejar.New(nil)
		seedBrowserCookies(jar, cookies)
		origin, _ := url.Parse("https://auth.openai.com/")
		saved := jar.Cookies(origin)
		if len(saved) != 1 || saved[0].Name != "__cf_bm" || saved[0].Value != "auth-cookie" {
			t.Fatal("parent-domain edge cookie replaced the auth host cookie")
		}
	}
}

func fixturePersonalBrowser(_ context.Context, source *http.Client, _ http.CookieJar, _ browserenv.Environment) (http.RoundTripper, func(), error) {
	return source.Transport, func() {}, nil
}

func TestBrowserScriptsCannotPerformWorkspaceActions(t *testing.T) {
	for _, path := range []string{"/api/accounts/workspace/select", "/oauth/authorize", "/backend-api/accounts/check/v4-2023-04-27", "/api/invites/accept"} {
		if allowedPersonalBrowserRequest(&network.Request{URL: "https://auth.openai.com" + path, Method: "GET"}) {
			t.Fatalf("browser script escaped Personal scope: %s", path)
		}
	}
	if !allowedPersonalBrowserRequest(&network.Request{URL: "https://auth.openai.com/api/accounts/password/verify", Method: "POST"}) {
		t.Fatal("password operation rejected")
	}
}

func TestPersonalRefreshUsesBrowserForEveryLoginStageAndClosesBeforeLease(t *testing.T) {
	fixture := &personalFlowFixture{token: personalFixtureToken(time.Now(), nil)}
	source := &http.Client{Transport: accountsRoundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("login escaped its browser into the lease HTTP client")
		return nil, nil
	}), Timeout: time.Second}
	closed := false
	adapter := PersonalWebRefresher{Client: func(context.Context) (*http.Client, func(), error) {
		return source, func() {
			if !closed {
				t.Error("lease released before browser closed")
			}
		}, nil
	}, Browser: func(_ context.Context, c *http.Client, _ http.CookieJar, _ browserenv.Environment) (http.RoundTripper, func(), error) {
		if c != source {
			t.Fatal("browser changed lease")
		}
		return fixture, func() { closed = true }, nil
	}}
	result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{Environment: fixtureEnvironment, LoginIdentifier: "account@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
	if err != nil || result.Status != "ready" || !closed || len(fixture.paths) < 8 {
		t.Fatalf("browser login incomplete: status=%s closed=%t requests=%d err=%v", result.Status, closed, len(fixture.paths), err)
	}
}

func TestBrowserDocumentWaitSurvivesRedirectContextReplacement(t *testing.T) {
	calls := 0
	err := waitForBrowserDocument(t.Context(), func(context.Context) (bool, error) {
		calls++
		if calls == 1 {
			return false, &cdproto.Error{Code: -32000, Message: "Cannot find context with specified id"}
		}
		return calls >= 3, nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("redirect readiness calls=%d error=%v", calls, err)
	}
	permanent := errors.New("document evaluation failed")
	if err := waitForBrowserDocument(t.Context(), func(context.Context) (bool, error) { return false, permanent }); !errors.Is(err, permanent) {
		t.Fatalf("permanent error swallowed: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitForBrowserDocument(ctx, func(context.Context) (bool, error) { t.Fatal("evaluated canceled document"); return false, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait: %v", err)
	}
}
