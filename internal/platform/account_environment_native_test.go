//go:build qualification

package platform

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
)

// This fixture exercises the production launcher and directory barrier. Native
// surface qualification remains the separately accepted #9 suite.
func TestPersistentAccountEnvironmentBrowser(t *testing.T) {
	root := os.Getenv("TSW_BROWSER_EVIDENCE")
	if root == "" {
		t.Skip("isolated native fixture root required")
	}
	ceiling, err := strconv.Atoi(os.Getenv("TSW_ENVIRONMENT_CONCURRENCY"))
	if err != nil || ceiling < 1 {
		t.Fatal("configured native fixture concurrency required")
	}
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cache-fixture.js" {
			w.Header().Set("Content-Type", "text/javascript")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			fmt.Fprint(w, "window.fixtureLoaded=true;/*"+strings.Repeat("cache-fixture", 10000)+"*/")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<!doctype html><title>Account environment fixture</title><script src=\"https://cdn.oaistatic.com/cache-fixture.js\"></script>")
	}))
	defer fixture.Close()
	sum := sha256.Sum256(fixture.Certificate().RawSubjectPublicKeyInfo)
	opts := []chromedp.ExecAllocatorOption{chromedp.Flag("ignore-certificate-errors-spki-list", base64.StdEncoding.EncodeToString(sum[:]))}
	// The ten fixture identities survive separate containers/hostnames in root.
	var environments []browserenv.Environment
	for n := 1; n <= 10; n++ {
		path := filepath.Join(root, fmt.Sprintf("environment-%02d.json", n))
		var e browserenv.Environment
		if raw, err := os.ReadFile(path); err == nil {
			if json.Unmarshal(raw, &e) != nil {
				t.Fatal("fixture identity invalid")
			}
		} else {
			e, err = browserenv.New()
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(e)
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		environments = append(environments, e)
	}
	for start := 0; start < len(environments); start += ceiling {
		var workers sync.WaitGroup
		for _, e := range environments[start:min(start+ceiling, len(environments))] {
			workers.Add(1)
			go func(e browserenv.Environment) {
				defer workers.Done()
				testAccountEnvironmentBrowser(t, fixture, e, root, opts)
			}(e)
		}
		workers.Wait()
	}
}

func testAccountEnvironmentBrowser(t *testing.T, fixture *httptest.Server, e browserenv.Environment, root string, opts []chromedp.ExecAllocatorOption) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	source := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", fixture.Listener.Addr().String())
	}}}
	jar, _ := cookiejar.New(nil)
	started := time.Now()
	browser, closeBrowser, _, err := launchBrowser(ctx, source, allowedPersonalBrowserRequest, e, filepath.Join(root, "profiles"), opts)
	if err != nil {
		t.Errorf("production launch %s: %v", e.ProfileID, err)
		return
	}
	defer closeBrowser()
	tr := personalBrowserTransport{browser: browser, jar: jar, validateContinuation: validatePersonalRequest}
	response, _, err := tr.navigate(browser, officialChatBase+"/")
	if err != nil {
		t.Errorf("navigate: %v", err)
		return
	}
	response.Body.Close()
	type observedEnvironment struct{ Stored, Cookie, Canvas, UA, Timezone string }
	expression := `(()=>{const c=document.createElement('canvas');c.width=100;c.height=30;const x=c.getContext('2d');x.font='16px Arial';x.fillText('Persistent environment',1,20);return {Stored:localStorage.getItem('profile')||'',Cookie:document.cookie,Canvas:c.toDataURL(),UA:navigator.userAgent,Timezone:Intl.DateTimeFormat().resolvedOptions().timeZone}})()`
	observation, err := chromedp.Run(browser, chromedp.Evaluate[observedEnvironment](expression))
	if err != nil {
		t.Errorf("observe: %v", err)
		return
	}
	profile := e.ProfileID.String()
	if observation.Stored != "" && observation.Stored != profile {
		t.Errorf("profile inherited another account storage: %s", profile)
		return
	}
	if observation.UA != browserenv.UserAgent || observation.Timezone != "UTC" {
		t.Errorf("incoherent environment: %s/%s", observation.UA, observation.Timezone)
		return
	}
	script := fmt.Sprintf("localStorage.setItem('profile',%q);document.cookie='profile='+%q+'; Path=/; Secure; Max-Age=86400'", profile, profile)
	if _, err = chromedp.Run(browser, chromedp.Evaluate[chromedp.Void](script)); err != nil {
		t.Errorf("store: %v", err)
		return
	}
	databaseScript := fmt.Sprintf(`(async()=>{const db=await new Promise((resolve,reject)=>{const r=indexedDB.open('profile-state',1);r.onupgradeneeded=()=>r.result.createObjectStore('identity');r.onsuccess=()=>resolve(r.result);r.onerror=()=>reject(r.error)});const previous=await new Promise((resolve,reject)=>{const r=db.transaction('identity').objectStore('identity').get('profile');r.onsuccess=()=>resolve(r.result||'');r.onerror=()=>reject(r.error)});await new Promise((resolve,reject)=>{const tx=db.transaction('identity','readwrite');tx.objectStore('identity').put(%q,'profile');tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error)});db.close();return previous})()`, profile)
	previousDatabase, err := chromedp.Run(browser, chromedp.Evaluate[string](databaseScript, chromedp.EvalAwaitPromise))
	if err != nil || previousDatabase != "" && previousDatabase != profile {
		t.Errorf("IndexedDB inherited another identity or failed: %v", err)
		return
	}
	path := filepath.Join(root, profile+"-observation.json")
	stable := struct{ Canvas, UA, Timezone string }{observation.Canvas, observation.UA, observation.Timezone}
	raw, _ := json.Marshal(stable)
	if previous, err := os.ReadFile(path); err == nil {
		if string(previous) != string(raw) || observation.Stored != profile || !strings.Contains(observation.Cookie, "profile="+profile) || previousDatabase != profile {
			t.Errorf("return changed fingerprint or failed to restore own cookie/storage: %s", profile)
			return
		}
	} else if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Errorf("evidence: %v", err)
		return
	}
	// A simple text-only Canvas can collide. The accepted qualification script
	// measures actual page/worker Canvas, Audio, font metrics and geometry for
	// the independent-account composite; identifiers are not that evidence.
	native, err := chromedp.Run(browser, chromedp.Evaluate[map[string]any](qualificationObservation, chromedp.EvalAwaitPromise))
	if err != nil {
		t.Errorf("qualified native observations: %v", err)
		return
	}
	phase := os.Getenv("TSW_BROWSER_EVIDENCE_PHASE")
	if phase != "" {
		phase = "-" + phase
	}
	nativePath := filepath.Join(root, profile+"-native"+phase+".json")
	raw, err = json.Marshal(native)
	if err != nil {
		t.Errorf("native evidence encoding: %v", err)
		return
	}
	if previous, err := os.ReadFile(nativePath); err == nil {
		if err = os.WriteFile(filepath.Join(root, profile+"-native"+phase+"-return.json"), raw, 0600); err != nil {
			t.Errorf("native return evidence: %v", err)
			return
		}
		if string(previous) != string(raw) {
			t.Errorf("return changed native page/worker observations: %s", profile)
			return
		}
	} else if err = os.WriteFile(nativePath, raw, 0600); err != nil {
		t.Errorf("native evidence: %v", err)
		return
	}
	closeBrowser()
	for _, name := range []string{"Default/Cache", "Default/Code Cache", "GPUPersistentCache", "Default/GPUCache", "Default/DawnWebGPUCache", "Default/DawnGraphiteCache"} {
		if _, err = os.Lstat(filepath.Join(root, "profiles", profile, name)); !os.IsNotExist(err) {
			t.Errorf("idle profile retained rebuildable cache %s: %v", name, err)
		}
	}
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		if _, err = os.Lstat(filepath.Join(root, "profiles", profile, name)); !os.IsNotExist(err) {
			t.Errorf("browser left %s for %s", name, profile)
		}
	}
	t.Logf("production profile %s storage=%s launch+fixture=%s cleanup complete", profile, observation.Stored, time.Since(started))
}
