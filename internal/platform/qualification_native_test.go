//go:build qualification

package platform

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
)

//go:embed qualification_observation.js
var qualificationObservation string

func TestNativeQualificationCDP(t *testing.T) {
	started := time.Now()
	var forbidden, posts atomic.Int64
	var headerMu sync.Mutex
	requestHeaders := make(map[string]http.Header)
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerMu.Lock()
		requestHeaders[r.Host+r.URL.Path] = r.Header.Clone()
		headerMu.Unlock()
		w.Header().Set("Accept-CH", "Sec-CH-UA-Full-Version-List, Sec-CH-UA-Platform-Version, Sec-CH-UA-Arch, Sec-CH-UA-Bitness")
		if strings.HasPrefix(r.URL.Path, "/backend-api/") {
			forbidden.Add(1)
		}
		if r.URL.Path == "/api/accounts/password/verify" {
			posts.Add(1)
			if r.URL.Query().Get("redirect") == "1" {
				w.Header().Set("Location", "/log-in?receipt=1")
				w.WriteHeader(302)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"ok":true}`)
			return
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/log-in", 302)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<!doctype html><html><title>Local qualification</title><body>fixture</body></html>`)
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	jar, _ := cookiejar.New(nil)
	source := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", fixture.Listener.Addr().String())
	}}}
	route, err := egress.StartBrowserRoute(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer route.Close()
	cert := fixture.Certificate()
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	profileID := os.Getenv("TSW_QUALIFICATION_PROFILE")
	if profileID == "" {
		profileID = "minimal"
	}
	profile := filepath.Join(os.Getenv("TSW_BROWSER_EVIDENCE"), "profiles", profileID)
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	recipe := os.Getenv("TSW_QUALIFICATION_RECIPE")
	if recipe == "" {
		t.Fatal("official ShardX fingerprint recipe is required")
	}
	opts = append(opts, chromedp.ExecPath(os.Getenv("TSW_CHROMIUM_PATH")), chromedp.UserDataDir(profile), chromedp.NoSandbox,
		chromedp.Flag("headless", false), chromedp.Flag("enable-automation", false), chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("fingerprint-profile", recipe), chromedp.Flag("shardx-webrtc-policy", "block"), chromedp.Flag("force-webrtc-ip-handling-policy", "disable_non_proxied_udp"),
		chromedp.Flag("disk-cache-size", "16777216"), chromedp.Flag("media-cache-size", "4194304"),
		chromedp.Flag("ignore-certificate-errors-spki-list", base64.StdEncoding.EncodeToString(sum[:])), chromedp.Flag("disable-quic", true), chromedp.Flag("proxy-bypass-list", "<-loopback>"), chromedp.ProxyServer(route.URL()))
	if os.Getenv("TSW_QUALIFICATION_SWIFTSHADER") == "1" {
		opts = append(opts, chromedp.Flag("use-angle", "swiftshader"), chromedp.Flag("enable-unsafe-swiftshader", true))
	}
	alloc, stopAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer stopAlloc()
	browser, stopBrowser := chromedp.NewContext(alloc)
	defer stopBrowser()
	defer func() {
		if err := chromedp.Cancel(browser); err != nil {
			t.Errorf("graceful browser close: %v", err)
		}
	}()
	if err := chromedp.Do(browser); err != nil {
		t.Fatalf("CDP initialization: %v", err)
	}
	startupMS := float64(time.Since(started).Microseconds()) / 1000
	tr := &personalBrowserTransport{browser: browser, jar: jar, validateContinuation: validatePersonalRequest}
	events := chromedp.Events(browser, fetch.RequestPaused)
	go func() {
		for event, err := range events {
			if err != nil {
				return
			}
			if tr.allowedRequest(event.Request) {
				_, err = chromedp.Call(browser, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: event.RequestID})
			} else {
				_, err = chromedp.Call(browser, fetch.FailRequest, fetch.FailRequestParams{RequestID: event.RequestID, ErrorReason: network.ErrorReasonBlockedByClient})
			}
			if err != nil {
				return
			}
		}
	}()
	if _, err := chromedp.Call(browser, fetch.Enable, fetch.EnableParams{}); err != nil {
		t.Fatal(err)
	}
	storageOnly := os.Getenv("TSW_QUALIFICATION_STORAGE_ONLY") == "1"
	target := "https://auth.openai.com/redirect"
	if storageOnly {
		target = "https://auth.openai.com/log-in"
	}
	response, final, err := tr.navigate(browser, target)
	if err != nil {
		t.Fatalf("current navigate: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || final != "https://auth.openai.com/log-in" {
		t.Fatalf("navigation status=%d final=%q", response.StatusCode, final)
	}
	if !storageOnly {
		value, err := chromedp.Run(browser, chromedp.Evaluate[int](`1+2`))
		if err != nil || value != 3 {
			t.Fatalf("evaluate: value=%d err=%v", value, err)
		}
		for _, suffix := range []string{"", "?redirect=1"} {
			req, _ := http.NewRequestWithContext(browser, http.MethodPost, "https://auth.openai.com/api/accounts/password/verify"+suffix, strings.NewReader(`{"fixture":true}`))
			req.Header.Set("Content-Type", "application/json")
			response, err := tr.fetch(browser, req)
			if err != nil {
				t.Fatalf("current fetch %s: %v", suffix, err)
			}
			response.Body.Close()
			if suffix == "" && response.StatusCode != 200 {
				t.Fatalf("fetch status %d", response.StatusCode)
			}
			if suffix != "" && (response.StatusCode != 302 || response.Header.Get("Location") != "/log-in?receipt=1") {
				t.Fatalf("redirect status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
			}
		}
		blocked, err := chromedp.Run(browser, chromedp.Evaluate[bool](`fetch('/backend-api/accounts/check/v4-2023-04-27').then(()=>false,()=>true)`, chromedp.EvalAwaitPromise))
		if err != nil || !blocked || forbidden.Load() != 0 || posts.Load() != 2 {
			t.Fatalf("guard blocked=%t forbidden=%d posts=%d err=%v", blocked, forbidden.Load(), posts.Load(), err)
		}
	}
	observation, err := chromedp.Run(browser, chromedp.Evaluate[map[string]any](`({userAgent:navigator.userAgent,platform:navigator.platform,language:navigator.language,languages:navigator.languages,timezone:Intl.DateTimeFormat().resolvedOptions().timeZone,hardwareConcurrency:navigator.hardwareConcurrency,deviceMemory:navigator.deviceMemory,webdriver:navigator.webdriver})`))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(observation, "", "  ")
	if err := os.WriteFile(filepath.Join(os.Getenv("TSW_BROWSER_EVIDENCE"), "basic-"+profileID+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if storageOnly {
		t.Logf("navigation/basic observation passed; observation=%s", data)
	} else {
		t.Logf("navigation/evaluate/fetch/opaque-redirect/guard passed; observation=%s", data)
	}
	if os.Getenv("TSW_QUALIFICATION_EXTENDED") == "1" {
		observationScript := qualificationObservation
		if storageOnly {
			observationScript = `(async()=>{const canvas=document.createElement('canvas');canvas.width=240;canvas.height=80;const ctx=canvas.getContext('2d');ctx.fillStyle='#173451';ctx.fillRect(0,0,240,80);ctx.font='19px Arial';ctx.fillStyle='#ab72ce';ctx.fillText('Account fixture 012345',7,27);const image=Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',ctx.getImageData(0,0,240,80).data))).map(x=>x.toString(16).padStart(2,'0')).join('');return {page:{canvas:{image,textMetrics:ctx.measureText('Account fixture').width},userAgent:navigator.userAgent,platform:navigator.platform}};})()`
		}
		native, err := chromedp.Run(browser, chromedp.Evaluate[map[string]any](observationScript, chromedp.EvalAwaitPromise))
		if err != nil {
			t.Fatalf("native observation: %v", err)
		}
		storage, err := chromedp.Run(browser, chromedp.Evaluate[map[string]any](`({cookie:document.cookie,localStorage:localStorage.getItem('fixture-account')})`))
		if err != nil {
			t.Fatal(err)
		}
		if value := storage["localStorage"]; value != nil && value != profileID {
			t.Fatalf("profile inherited another identity: %v", storage)
		}
		if cookie, ok := storage["cookie"].(string); ok && strings.Contains(cookie, "fixture-account=") && !strings.Contains(cookie, "fixture-account="+profileID) {
			t.Fatalf("profile inherited another cookie: %v", storage)
		}
		diagnostic, err := chromedp.Run(browser, chromedp.Evaluate[map[string]any](`(async()=>{await document.fonts.ready;const c=document.createElement('canvas').getContext('2d');let metrics={};for(const font of ['19px Arial','19px sans-serif','19px "Liberation Sans"','19px monospace']){c.font=font;metrics[font]={resolved:c.font,width:c.measureText('Account fixture').width};}return {metrics,fontStatus:document.fonts.status};})()`, chromedp.EvalAwaitPromise))
		if err != nil {
			t.Fatal(err)
		}
		native["fontDiagnostic"] = diagnostic
		native["storageBefore"] = storage
		native["profileID"] = profileID
		native["timings"] = map[string]float64{"startupMS": startupMS, "observedMS": float64(time.Since(started).Microseconds()) / 1000}
		recipeData, err := os.ReadFile(recipe)
		if err != nil {
			t.Fatal(err)
		}
		var recipeConfig map[string]any
		if err := json.Unmarshal(recipeData, &recipeConfig); err != nil {
			t.Fatal(err)
		}
		native["noiseRecipe"] = recipeConfig["noise"]
		headerMu.Lock()
		headerSnapshot := make(map[string]http.Header, len(requestHeaders))
		for target, headers := range requestHeaders {
			headerSnapshot[target] = headers.Clone()
		}
		native["requestHeaders"] = headerSnapshot
		headerMu.Unlock()
		encodedID, _ := json.Marshal(profileID)
		if _, err := chromedp.Run(browser, chromedp.Evaluate[bool](`(()=>{localStorage.setItem('fixture-account',`+string(encodedID)+`);document.cookie='fixture-account='+`+string(encodedID)+`+'; Max-Age=86400; Path=/; Secure; SameSite=Lax';return true;})()`)); err != nil {
			t.Fatal(err)
		}
		data, _ := json.MarshalIndent(native, "", "  ")
		phase := os.Getenv("TSW_QUALIFICATION_PHASE")
		if phase == "" {
			phase = "first"
		}
		if err := os.WriteFile(filepath.Join(os.Getenv("TSW_BROWSER_EVIDENCE"), phase+"-"+profileID+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("native observation and storage recorded for %s recipe %s", profileID, recipe)
		if storageOnly {
			return
		}
		page, ok := native["page"].(map[string]any)
		if !ok {
			t.Fatal("native page unavailable")
		}
		canvas, ok := page["canvas"].(map[string]any)
		if !ok {
			t.Fatal("native canvas unavailable")
		}
		width, ok := canvas["textMetrics"].(float64)
		if !ok || width <= 0 {
			t.Fatalf("native qualification failed: nonempty page canvas text width=%v; original observation retained", canvas["textMetrics"])
		}
		worker := native["worker"].(map[string]any)
		for _, field := range []string{"userAgent", "platform", "language", "languages", "hardwareConcurrency", "deviceMemory", "uaData", "timezone", "canvas", "webgl"} {
			left, right := page[field], worker[field]
			if field == "canvas" {
				lp, rp := left.(map[string]any), right.(map[string]any)
				left = map[string]any{"image": lp["image"], "textMetrics": lp["textMetrics"]}
				right = map[string]any{"image": rp["image"], "textMetrics": rp["textMetrics"]}
			}
			if field == "webgl" {
				metadata := func(value any) map[string]any {
					out := make(map[string]any)
					for key, v := range value.(map[string]any) {
						if key != "pixels" && key != "pixelsRepeated" && key != "repeatDifference" && key != "pixelSample" {
							out[key] = v
						}
					}
					return out
				}
				left, right = metadata(left), metadata(right)
			}
			l, _ := json.Marshal(left)
			r, _ := json.Marshal(right)
			if string(l) != string(r) {
				t.Fatalf("native qualification failed: page/worker %s differs: %s vs %s", field, l, r)
			}
		}
		if page["webgl"].(map[string]any)["supported"] != true {
			t.Fatal("native WebGL unavailable")
		}
		for scope, values := range map[string]map[string]any{"page": page, "worker": worker} {
			gl := values["webgl"].(map[string]any)
			if gl["pixels"] != gl["pixelsRepeated"] {
				t.Fatalf("native qualification failed: %s same-context consecutive readPixels changed: difference=%v", scope, gl["repeatDifference"])
			}
		}
	}
}
