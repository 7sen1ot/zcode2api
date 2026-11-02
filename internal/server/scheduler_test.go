package server

import (
	"testing"
	"time"
)

func (a *App) release(id string) {
	a.finishAccountAttempt(M{"id": id}, nil, false, nil)
}

func TestSchedulerQueueReusesOnlyReleasedSlot(t *testing.T) {
	a := testApp(t)
	k, _, accounts := testPool(t, a, 1)
	id := str(accounts[0]["id"])
	o := inferOptions{Key: k, Model: "m"}
	for _, model := range []string{"GLM-5.3", "GLM-5.3-Flash", "GLM-5.3"} {
		options := o
		options.Model = model
		account, _, _, _ := a.selectAccount(options, nil)
		if account == nil || str(account["id"]) != id {
			t.Fatal("account did not accept three requests")
		}
	}
	if account, busy, _, _ := a.selectAccount(o, nil); account != nil || !busy {
		t.Fatal("fourth request bypassed the account limit")
	}

	ctx := t.Context()
	acquired := make(chan M, 1)
	go func() {
		account, _, _, _, _ := a.acquire(ctx, o, nil, time.Second)
		acquired <- account
	}()
	select {
	case <-acquired:
		t.Fatal("queued request did not wait for a slot")
	case <-time.After(20 * time.Millisecond):
	}
	a.release(id)
	select {
	case account := <-acquired:
		if account == nil || str(account["id"]) != id {
			t.Fatal("queued request did not acquire the released slot")
		}
	case <-time.After(time.Second):
		t.Fatal("queued request was not notified")
	}
	if account, busy, _, _ := a.selectAccount(o, nil); account != nil || !busy {
		t.Fatal("releasing one request released other occupied slots")
	}
	for range 3 {
		a.release(id)
	}
	for range 3 {
		if account, _, _, _ := a.selectAccount(o, nil); account == nil {
			t.Fatal("slots were not restored after all requests completed")
		}
	}
}

func TestSchedulerStickyAccountUsesThreeSlots(t *testing.T) {
	a := testApp(t)
	k, _, accounts := testPool(t, a, 2)
	id := str(accounts[0]["id"])
	o := inferOptions{Key: k, Model: "m", Affinity: "conversation", Sticky: 10}
	a.mu.Lock()
	a.sticky[o.Affinity] = stickyEntry{id, nowMS()}
	a.state(id).LastUsed = nowMS()
	a.mu.Unlock()
	for range 3 {
		account, _, _, _ := a.selectAccount(o, nil)
		if account == nil || str(account["id"]) != id {
			t.Fatal("sticky account was skipped while it had available slots")
		}
	}
	account, _, _, _ := a.selectAccount(o, nil)
	if account == nil || account["id"] != accounts[1]["id"] {
		t.Fatal("full sticky account prevented switching to another account")
	}
}
