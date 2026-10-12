package egress

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAccountAdmissionDefaultHigherLowerAndDynamicSettings(t *testing.T) {
	router, _ := NewManaged("http://platform.test", "http://exit.test", []byte("capacity-fixture"), "1")
	defer router.CloseIdleConnections()
	pool, _ := NewLeaseManager(router, Admission{})
	m := NewManager(pool, router.Status(Admission{}))
	m.SetMode(ModeDirect)
	if m.Concurrency() != 5 {
		t.Fatal("fresh default is not five")
	}
	m.SetConcurrency(7)
	var held []*Lease
	for range 7 {
		l, err := m.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, l)
	}
	defer func() {
		for _, l := range held {
			l.Release()
		}
	}()
	if m.ActiveCount() != 7 {
		t.Fatal("secondary five-browser cap")
	}
	m.SetConcurrency(2)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := m.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lower saved ceiling ignored", err)
	}
	if m.ActiveCount() != 7 {
		t.Fatal("lowering concurrency killed existing work")
	}
	admitted := make(chan *Lease, 1)
	go func() {
		l, err := m.Acquire(t.Context())
		if err == nil {
			admitted <- l
		}
	}()
	m.SetConcurrency(8)
	select {
	case l := <-admitted:
		l.Release()
	case <-time.After(time.Second):
		t.Fatal("dynamic increase did not wake queued work")
	}
}
