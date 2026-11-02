package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitForQueued(t *testing.T, a *App, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.scheduleMu.Lock()
		n := len(a.waiters)
		a.scheduleMu.Unlock()
		if n == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("requests did not enter the shared queue")
}

func TestBusyQueueSharesCooldownAndSerializesRecoveryProbe(t *testing.T) {
	a := testApp(t)
	k, _, accounts := testPool(t, a, 1)
	o := inferOptions{Key: k, Model: "m"}
	account, _, _, _ := a.selectAccount(o, nil)
	a.finishAccountAttempt(account, &Failure{Kind: "busy", RetryAfter: 100 * time.Millisecond}, true, a.retryParams())
	if next, busy, _, _ := a.selectAccount(o, nil); next != nil || !busy {
		t.Fatal("another request bypassed the shared cooldown")
	}
	first, second := make(chan M, 1), make(chan M, 1)
	go func() { r, _, _, _, _ := a.acquire(t.Context(), o, nil, time.Millisecond); first <- r }()
	waitForQueued(t, a, 1)
	go func() { r, _, _, _, _ := a.acquire(t.Context(), o, nil, time.Millisecond); second <- r }()
	waitForQueued(t, a, 2)
	var probe M
	select {
	case probe = <-first:
		if probe == nil {
			t.Fatal("shared busy wait was capped by the local queue timeout")
		}
	case <-time.After(time.Second):
		t.Fatal("cooldown did not expire")
	}
	select {
	case <-second:
		t.Fatal("more than one recovery probe was admitted")
	case <-time.After(20 * time.Millisecond):
	}
	a.admitAccount(probe)
	select {
	case next := <-second:
		if next == nil || next["id"] != accounts[0]["id"] {
			t.Fatal("successful probe did not wake the next waiter")
		}
		a.finishAccountAttempt(next, nil, true, nil)
	case <-time.After(time.Second):
		t.Fatal("queue did not resume on first output")
	}
	a.finishAccountAttempt(probe, nil, true, nil)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state(str(accounts[0]["id"])).Inflight != 0 {
		t.Fatal("account slot leaked")
	}
}

func TestBusyQueueCancellationAndNewerBusyState(t *testing.T) {
	a := testApp(t)
	k, _, accounts := testPool(t, a, 1)
	o := inferOptions{Key: k, Model: "m"}
	older, _, _, _ := a.selectAccount(o, nil)
	newer, _, _, _ := a.selectAccount(o, nil)
	a.finishAccountAttempt(newer, &Failure{Kind: "busy", RetryAfter: time.Minute}, true, nil)
	a.admitAccount(older)
	a.finishAccountAttempt(older, nil, true, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, _, _, _, err := a.acquire(ctx, o, nil, time.Second); done <- err }()
	waitForQueued(t, a, 1)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal("cancellation was lost", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter was not removed")
	}
	waitForQueued(t, a, 0)
	if next, busy, _, _ := a.selectAccount(o, nil); next != nil || !busy {
		t.Fatal("an older successful request erased a newer busy result")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state(str(accounts[0]["id"])).Inflight != 0 {
		t.Fatal("cancel leaked a slot")
	}
}

func TestBusyRetryOutlivesTenSeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("verifies recovery after the former ten-second window")
	}
	a := testApp(t)
	k, _, _ := testPool(t, a, 1)
	obj(a.cfg["retry"])["max_attempts"] = 1
	var calls atomic.Int32
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "11")
			writeJSON(w, 429, M{"code": 3008})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, wire(syntheticEvents(false)))
	})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	up, err := buildRequest(baseBody("chat", false), "chat", "m", "low")
	must(t, err)
	start := time.Now()
	result, failure := a.infer(ctx, inferOptions{Key: k, Model: "m", Upstream: up, ID: "busy-recovery"})
	if failure != nil || result == nil {
		t.Fatalf("busy recovery stopped early: %+v", failure)
	}
	if time.Since(start) < 11*time.Second || calls.Load() != 2 {
		t.Fatal("Retry-After was not respected", calls.Load())
	}
}

func TestBusyInferenceStopsOnClientCancellation(t *testing.T) {
	a := testApp(t)
	k, _, accounts := testPool(t, a, 1)
	var calls atomic.Int32
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "30")
		writeJSON(w, 429, M{"code": 3009})
	})
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	_, failure := a.infer(ctx, inferOptions{Key: k, Model: "m", Upstream: M{}, ID: "cancel-busy"})
	if failure == nil || failure.Kind != "canceled" || calls.Load() != 1 {
		t.Fatal("busy request ignored cancellation", failure)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state(str(accounts[0]["id"])).Inflight != 0 {
		t.Fatal("canceled inference leaked a slot")
	}
}

func TestBusyQueueDoesNotBlockUnrelatedAccountPool(t *testing.T) {
	a := testApp(t)
	k, _, _ := testPool(t, a, 1)
	k["group_order"] = js([]any{"g_main"})
	o := inferOptions{Key: k, Model: "m"}
	account, _, _, _ := a.selectAccount(o, nil)
	a.finishAccountAttempt(account, &Failure{Kind: "busy", RetryAfter: time.Minute}, true, nil)
	must(t, a.exec("INSERT INTO groups(id,name,role,created_at) VALUES('other','other','backup',?)", nowMS()))
	other, err := a.insertAccount(M{"family": "zai", "status": "active", "group_id": "other"}, M{})
	must(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _, _, _, _ = a.acquire(ctx, o, nil, time.Second) }()
	waitForQueued(t, a, 1)
	next, _, _, _, err := a.acquire(t.Context(), inferOptions{Key: M{"group_order": js([]any{"other"})}, Model: "m"}, nil, 100*time.Millisecond)
	if err != nil || next == nil || next["id"] != other["id"] {
		t.Fatal("busy queue head blocked an unrelated pool", err)
	}
	a.finishAccountAttempt(next, nil, true, nil)
	cancel()
	<-done
}

func TestQueueCancellationRacingWithAssignmentDoesNotLeakSlots(t *testing.T) {
	a := testApp(t)
	k, _, accounts := testPool(t, a, 1)
	var workers sync.WaitGroup
	for range 64 {
		workers.Go(func() {
			ctx, cancel := context.WithCancel(t.Context())
			timer := time.AfterFunc(time.Microsecond, cancel)
			defer timer.Stop()
			defer cancel()
			account, _, _, _, _ := a.acquire(ctx, inferOptions{Key: k, Model: "m"}, nil, time.Second)
			if account != nil {
				a.finishAccountAttempt(account, nil, false, nil)
			}
		})
	}
	workers.Wait()
	waitForQueued(t, a, 0)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state(str(accounts[0]["id"])).Inflight != 0 {
		t.Fatal("cancellation racing with dispatch leaked a reserved slot")
	}
}
