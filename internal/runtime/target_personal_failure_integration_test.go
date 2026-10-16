//go:build integration

package runtime

import (
	"context"
	"encoding/json"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

type targetChallengeTransport struct{ calls int }

func (transport *targetChallengeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls++
	return &http.Response{StatusCode: 403, Header: http.Header{"Cf-Mitigated": []string{"challenge"}}, Body: io.NopCloser(strings.NewReader("browser verification")), Request: request}, nil
}

func TestTargetPersonalRefreshReturnsClassifiedChallenge(t *testing.T) {
	pool, handler, id, call := targetReuseFixture(t)
	transport := &targetChallengeTransport{}
	handler.personalRefresh = platform.PersonalWebRefresher{Browser: func(_ context.Context, c *http.Client, _ http.CookieJar, _ browserenv.Environment) (http.RoundTripper, func(), error) {
		return c.Transport, func() {}, nil
	}, Client: func(context.Context) (*http.Client, func(), error) {
		return &http.Client{Transport: transport, Timeout: time.Second}, func() {}, nil
	}}
	response := call()
	var access ownerapi.TargetPersonalAccess
	if err := json.Unmarshal(response.Body.Bytes(), &access); err != nil || response.Code != 200 || access.Status != "refresh_failed" || access.Failure == nil || access.Failure.Code != "platform_browser_challenge" || access.Failure.HttpStatus == nil || *access.Failure.HttpStatus != 403 || transport.calls != 1 {
		t.Fatalf("challenge lost between adapter, store and API: code=%d access=%+v calls=%d err=%v", response.Code, access, transport.calls, err)
	}
	var saved bool
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM tsw_target_personal_sessions WHERE target_account_id=$1)`, id).Scan(&saved); err != nil || saved {
		t.Fatal("platform challenge published a session")
	}
	for _, secret := range []string{"target@example.com", "password", "JBSWY3DPEHPK3PXP", "browser verification"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatal("classified response leaked login material or remote body")
		}
	}
}
