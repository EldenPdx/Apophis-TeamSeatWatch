package egress

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedPoolPreservesActiveIdentityAcrossRefreshAndDrift(t *testing.T) {
	first := Candidate{ID: "original", Fingerprint: sha256.Sum256([]byte("original-exit"))}
	replacement := Candidate{ID: "replacement", Fingerprint: first.Fingerprint}
	m := &LeaseManager{router: &Router{config: Config{Mode: ModeRequired}}, candidates: []Candidate{first}, active: map[[32]byte]Candidate{first.Fingerprint: first}}
	m.ReplaceAdmission(Admission{Candidates: []Candidate{replacement}})
	m.exclude(first.Fingerprint)
	if got := m.ActiveCandidateIDs(); got[first.ID] != 1 || got[replacement.ID] != 0 {
		t.Fatalf("active lease was reassigned: %v", got)
	}
	called := false
	if err := m.RemoveIdleNode(first.ID, func() error { called = true; return nil }); !errors.Is(err, ErrNodeInUse) || called {
		t.Fatalf("active node removal err=%v callback=%v", err, called)
	}
	lease := &Lease{manager: m, client: &http.Client{}, mode: ModeRequired, fingerprint: first.Fingerprint}
	lease.Release()
	if len(m.candidates) != 0 || m.ActiveCount() != 0 {
		t.Fatal("released isolated route was restored")
	}
	if err := m.RemoveIdleNode(first.ID, func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("idle node removal err=%v", err)
	}
}

func TestManagedPoolConcurrentAcquisitionHonorsLimit(t *testing.T) {
	endpoints := make([]Endpoint, 6)
	for i := range endpoints {
		ip := "1.1.1." + strconv.Itoa(i+1)
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/echo" {
				_, _ = w.Write([]byte(ip))
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(proxy.Close)
		endpoints[i] = Endpoint{ID: strconv.Itoa(i), URL: proxy.URL}
	}
	router, err := NewManaged("http://platform.test/ready", "http://platform.test/echo", []byte("verification-key"), "1")
	if err != nil {
		t.Fatal(err)
	}
	defer router.CloseIdleConnections()
	admission, err := router.AdmitEndpoints(context.Background(), endpoints)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := NewLeaseManager(router, admission)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(pool, router.Status(admission))
	manager.SetConcurrency(3)
	start := make(chan struct{})
	leases := make(chan *Lease, 20)
	var active, peak atomic.Int32
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			lease, err := manager.Acquire(context.Background())
			if err == nil {
				n := active.Add(1)
				for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
				}
				time.Sleep(5 * time.Millisecond)
				active.Add(-1)
				lease.Release()
				leases <- lease
			} else if !errors.Is(err, ErrLeaseExhausted) {
				t.Errorf("capacity error=%v", err)
			}
		}()
	}
	close(start)
	wait.Wait()
	close(leases)
	completed := 0
	for lease := range leases {
		lease.Release()
		completed++
	}
	if peak.Load() != 3 || completed != 20 || manager.ActiveCount() != 0 {
		t.Fatalf("queued acquisitions peak=%d completed=%d remaining=%d", peak.Load(), completed, manager.ActiveCount())
	}
}
