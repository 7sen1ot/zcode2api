package server

import (
	"context"
	"maps"
	rand "math/rand/v2"
	"sort"
	"strings"
	"time"
	accountdata "zcode2api/internal/server/account"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
)

const accountConcurrencyLimit = 3

const maxQueuedRequests = 1024

type accountAssignment struct {
	account shared.M
	busy    bool
	failure *fault.Failure
}

type accountWaiter struct {
	ctx      context.Context
	o        inferOptions
	skip     map[string]bool
	deadline time.Time
	result   chan accountAssignment
}

type schedulingAccount struct {
	row           shared.M
	id            string
	jwtExpiresAt  int64
	cardExpiresAt int64
	// Decoded on first use during the dispatch pass.
	models  []string
	balance shared.M
}

type schedulingPool struct {
	group    shared.M
	accounts []schedulingAccount
}

type schedulingCandidate struct {
	*schedulingAccount
	remaining float64
}

type eligibleSchedulingPool struct {
	group    shared.M
	accounts []schedulingCandidate
}

type poolRecoveryLease struct {
	key        string
	generation uint64
}

// Keep one serial recovery round across requests. A canceled caller releases
// ownership, but the next caller continues with the accounts still unchecked.
type poolRecoveryRound struct {
	owner      *poolRecoveryLease
	checked    map[string]bool
	allBlocked bool
	refused    bool
	retryAt    time.Time
}

func (a *App) schedulingPools(groupOrder string) []schedulingPool {
	query := `SELECT a.*,g.id AS _gid,g.name AS _name,g.role AS _role,g.policy AS _policy,g.family_filter AS _family,g.urgent_window_min AS _urgent FROM groups g LEFT JOIN accounts a ON a.group_id=g.id WHERE g.enabled=1`
	chain, explicit := shared.Parse(groupOrder, nil).([]any)
	args := []any{}
	if explicit {
		if len(chain) == 0 {
			return nil
		}
		query += " AND g.id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(chain)), ",") + ")"
		args = append(args, chain...)
	}
	query += " ORDER BY g.created_at,g.id,a.created_at,a.id"
	pools := map[string]*schedulingPool{}
	order := []any{}
	for _, v := range a.rows(query, args...) {
		r := shared.Obj(v)
		gid := shared.Str(r["_gid"])
		pool := pools[gid]
		if pool == nil {
			pool = &schedulingPool{group: shared.M{"id": gid, "name": r["_name"], "role": r["_role"], "policy": r["_policy"], "family_filter": r["_family"], "urgent_window_min": r["_urgent"], "enabled": 1}}
			pools[gid] = pool
			order = append(order, pool.group)
		}
		if r["id"] != nil && shared.Str(r["status"]) == "active" {
			pool.accounts = append(pool.accounts, schedulingAccount{row: r, id: shared.Str(r["id"])})
		}
	}
	if !explicit {
		chain = defaultGroupChain(order)
	}
	out := make([]schedulingPool, 0, len(chain))
	seen := map[string]bool{}
	for _, id := range chain {
		gid := shared.Str(id)
		if p := pools[gid]; p != nil && !seen[gid] {
			for i := range p.accounts {
				account := &p.accounts[i]
				account.jwtExpiresAt = accountdata.JwtExpiresAt(a.accountTokens(account.row)["zcode_jwt"])
				account.cardExpiresAt = shared.Integer(account.row["card_expires_at"])
			}
			out = append(out, *p)
			seen[gid] = true
		}
	}
	return out
}

func (a *App) eligibleSchedulingPools(snapshot []schedulingPool, model string, now, minWindow int64) []eligibleSchedulingPool {
	pools := make([]eligibleSchedulingPool, 0, len(snapshot))
	for _, pool := range snapshot {
		eligible := eligibleSchedulingPool{group: pool.group, accounts: make([]schedulingCandidate, 0, len(pool.accounts))}
		familyFilter := pool.group["family_filter"]
		for i := range pool.accounts {
			account := &pool.accounts[i]
			if accountdata.LoginJWTExpiredAt(account.jwtExpiresAt, now) {
				a.scheduleNext(account.id, 0)
				continue
			}
			if shared.Truth(account.row["card_expires_at"]) && account.cardExpiresAt-now < minWindow {
				continue
			}
			if shared.Truth(familyFilter) && familyFilter != account.row["family"] {
				continue
			}
			if account.models == nil {
				models := shared.Arr(shared.Parse(account.row["capabilities"], nil))
				account.models = make([]string, len(models))
				for j, model := range models {
					account.models[j] = shared.Str(model)
				}
			}
			if len(account.models) > 0 {
				allowed := false
				for _, capability := range account.models {
					if strings.EqualFold(capability, model) {
						allowed = true
						break
					}
				}
				if !allowed {
					continue
				}
			}
			if account.balance == nil {
				account.balance = accountdata.BalanceData(account.row)
			}
			if _, known := account.balance["balances"].([]any); !known {
				continue
			}
			if remaining := accountdata.RemainingBalance(account.balance, now); remaining > 0 {
				eligible.accounts = append(eligible.accounts, schedulingCandidate{account, remaining})
			}
		}
		pools = append(pools, eligible)
	}
	return pools
}

func (a *App) selectAccount(o inferOptions, excluded map[string]bool, snapshot []schedulingPool, minWindow int64) (account shared.M, busy, upstreamBusy bool, failure *fault.Failure) {
	now := shared.NowMS()
	pools := a.eligibleSchedulingPools(snapshot, o.Model, now, minWindow)
	a.mu.Lock()
	defer a.mu.Unlock()
	poolKey, poolCooldown := a.accountPoolCooldownLocked(pools, o.Model)
	if o.Recovery != nil && o.Recovery.key != poolKey {
		a.releasePoolRecoveryLocked(o.Recovery)
	}
	var cooling *fault.Failure
	if poolCooldown != nil && (time.Now().Before(poolCooldown.until) || poolCooldown.probing && (poolCooldown.recovery == nil || poolCooldown.recovery.owner != o.Recovery)) {
		return nil, false, false, poolCooldown.refusal()
	}
	if poolCooldown != nil {
		if poolCooldown.recovery == nil {
			poolCooldown.recovery = &poolRecoveryRound{checked: map[string]bool{}, allBlocked: true}
		}
		round := poolCooldown.recovery
		eligible := map[string]bool{}
		for _, pool := range pools {
			for _, c := range pool.accounts {
				id := c.id
				if !a.state(id).QuotaExhausted {
					eligible[id] = true
					if _, exists := round.checked[id]; !exists {
						round.checked[id] = false
					}
				}
			}
		}
		for id := range round.checked {
			if !eligible[id] {
				delete(round.checked, id)
			}
		}
		if a.completePoolRecoveryLocked(poolKey, poolCooldown) {
			return nil, false, false, poolCooldown.refusal()
		}
		round.owner, poolCooldown.probing = o.Recovery, true
		if o.Recovery != nil {
			o.Recovery.key, o.Recovery.generation = poolKey, poolCooldown.generation
		}
		// Check every eligible account at most once, including backup groups.
		probePool := eligibleSchedulingPool{group: shared.M{"policy": "risk_probe"}}
		for _, pool := range pools {
			probePool.accounts = append(probePool.accounts, pool.accounts...)
		}
		pools = []eligibleSchedulingPool{probePool}
	}
	stickyAccount := a.sticky.account(o.Affinity, o.Sticky, now)
	for _, p := range pools {
		policy := shared.Str(p.group["policy"])
		urgentWindow := int64(shared.Num(shared.Fallback(p.group["urgent_window_min"], 180)) * 60000)
		preferred := func(x, y *schedulingCandidate) bool {
			xs, ys := a.state(x.id), a.state(y.id)
			if poolCooldown != nil {
				return xs.LastUsed < ys.LastUsed
			}
			if stickyAccount != "" {
				xp, yp := x.id == stickyAccount, y.id == stickyAccount
				if xp != yp {
					return xp
				}
			}
			switch policy {
			case "remaining_desc":
				return x.remaining > y.remaining
			case "expiring_first":
				xe, ye := x.cardExpiresAt, y.cardExpiresAt
				xu, yu := xe > 0 && xe-now < urgentWindow, ye > 0 && ye-now < urgentWindow
				if xu != yu {
					return xu
				}
				if xu {
					return xe < ye
				}
				return xs.LastUsed > ys.LastUsed
			default:
				if xs.Inflight != ys.Inflight {
					return xs.Inflight < ys.Inflight
				}
				return xs.LastUsed < ys.LastUsed
			}
		}
		var picked *schedulingCandidate
		for i := range p.accounts {
			c := &p.accounts[i]
			s := a.state(c.id)
			if s.QuotaExhausted || poolCooldown != nil && poolCooldown.recovery.checked[c.id] {
				continue
			}
			if entry := a.upstreamCooldowns[a.accountCooldownKey(c.id)]; entry != nil && (time.Now().Before(entry.until) || entry.probing) {
				failure := entry.refusal()
				if cooling == nil || failure.RetryAfter < cooling.RetryAfter {
					cooling = failure
				}
				continue
			}
			if excluded[c.id] {
				continue
			}
			if s.BusyUntil > now || s.BusyUntil != 0 && (s.BusyProbe || s.Inflight > 0) {
				busy, upstreamBusy = true, true
				continue
			}
			if s.Inflight >= accountConcurrencyLimit {
				busy = true
				continue
			}
			if picked == nil || preferred(c, picked) {
				picked = c
			}
		}
		if picked != nil {
			state := a.state(picked.id)
			state.Inflight++
			assigned := maps.Clone(picked.row)
			assigned["_busy_generation"] = state.BusyGeneration
			assigned["_busy_probe"] = state.BusyUntil != 0
			if poolCooldown != nil {
				assigned["_risk_pool"] = poolKey
				assigned["_risk_generation"] = poolCooldown.generation
			}
			if state.BusyUntil != 0 {
				state.BusyProbe = true
			}
			return assigned, busy, upstreamBusy, nil
		}
	}
	if busy {
		cooling = nil
	}
	return nil, busy, upstreamBusy, cooling
}

func (a *App) accountCooldownKey(id string) string { return "account|" + a.origin + "|" + id }

// Pool identity is the authorized group set and model, independent of API key
// and ordering. Quota/card expiry must not reset a pool's backoff progression.
// Call with a.mu held, after the scheduler's ordinary eligibility filters.
func (a *App) accountPoolCooldownLocked(pools []eligibleSchedulingPool, model string) (string, *upstreamCooldown) {
	groups := make([]string, 0, len(pools))
	eligible := 0
	allBlocked := true
	recovered := false
	var until time.Time
	for _, pool := range pools {
		groups = append(groups, shared.Str(pool.group["id"]))
		for _, c := range pool.accounts {
			id := c.id
			if a.state(id).QuotaExhausted {
				continue
			}
			eligible++
			entry := a.upstreamCooldowns[a.accountCooldownKey(id)]
			if entry == nil {
				recovered = true
			}
			if entry == nil || entry.failure.Kind != "account_risk" {
				allBlocked = false
			} else {
				until = shared.MaxTime(until, entry.until)
			}
		}
	}
	if eligible == 0 {
		return "", nil
	}
	sort.Strings(groups)
	key := "inference-pool|" + a.origin + "|" + shared.Hash(shared.JSON([]any{strings.ToLower(model), groups}))
	entry := a.upstreamCooldowns[key]
	if entry != nil && recovered {
		// A recovered or newly added account is usable even when the pool's
		// other accounts are still cooling down.
		a.clearUpstreamCooldownLocked(key)
		entry = nil
	}
	if entry == nil && allBlocked {
		// Start at the last actual refusal, not the next client's arrival.
		entry = &upstreamCooldown{until: until, failures: 1, inference: true, generation: a.advanceCooldownEpochLocked(key), failure: fault.Failure{Kind: "risk", Code: "account_pool_cooling", Message: "账号池风控冷却中"}}
		a.upstreamCooldowns[key] = entry
		a.persistUpstreamCooldownLocked(key)
	}
	return key, entry
}

func (a *App) releasePoolRecovery(lease *poolRecoveryLease) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.releasePoolRecoveryLocked(lease)
}

func (a *App) releasePoolRecoveryLocked(lease *poolRecoveryLease) {
	entry := a.upstreamCooldowns[lease.key]
	if entry != nil && entry.generation == lease.generation && entry.recovery != nil && entry.recovery.owner == lease {
		entry.recovery.owner, entry.probing = nil, false
		a.notifyAvailableLocked()
	}
	lease.key, lease.generation = "", 0
}

// Advance only after the complete recovery round was blocked. Unrelated
// failures retain the current step; cached refusals cannot advance it alone.
func (a *App) completePoolRecoveryLocked(key string, entry *upstreamCooldown) bool {
	round := entry.recovery
	for _, checked := range round.checked {
		if !checked {
			return false
		}
	}
	entry.probing = false
	if len(round.checked) > 0 && round.allBlocked && round.refused {
		refusal := entry.failure
		refusal.RetryAfter = max(0, time.Until(round.retryAt))
		a.recordUpstreamCooldownLocked(key, entry.generation, &refusal, time.Minute, true)
	} else {
		entry.recovery = nil
		entry.until = shared.MaxTime(time.Now().Add(time.Second), round.retryAt)
		a.persistUpstreamCooldownLocked(key)
	}
	return true
}

// Keep the round lease between attempts. Only successful inference establishes
// recovery; one blocked account must not postpone checking the rest of the pool.
func (a *App) finishPoolProbeLocked(account shared.M, failure *fault.Failure, completed bool) {
	key := shared.Str(account["_risk_pool"])
	entry := a.upstreamCooldowns[key]
	if entry == nil || entry.generation != uint64(shared.Integer(account["_risk_generation"])) || entry.recovery == nil {
		return
	}
	if completed && failure == nil {
		a.clearUpstreamCooldownLocked(key)
		return
	}
	if !completed || failure == nil || failure.Kind == "canceled" {
		return
	}
	round := entry.recovery
	round.checked[shared.Str(account["id"])] = true
	blocked := failure.Kind == "account_risk" || failure.Code == "captcha_risk_blocked" || failure.Code == "F001" || failure.Code == "upstream_waf_blocked"
	round.allBlocked = round.allBlocked && blocked
	round.refused = round.refused || blocked && !failure.LocalOnly
	round.retryAt = shared.MaxTime(round.retryAt, time.Now().Add(failure.RetryAfter))
	if a.completePoolRecoveryLocked(key, entry) {
		failure.RetryAfter = max(failure.RetryAfter, time.Until(entry.until))
	}
}

func (a *App) availability() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.available == nil {
		a.available = make(chan struct{})
	}
	return a.available
}

func (a *App) notifyAvailableLocked() {
	if a.available != nil {
		close(a.available)
	}
	a.available = make(chan struct{})
}

func (a *App) notifyAvailable() { a.mu.Lock(); a.notifyAvailableLocked(); a.mu.Unlock() }

func (a *App) acquire(ctx context.Context, o inferOptions, skip map[string]bool, limit time.Duration) (shared.M, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	w := &accountWaiter{ctx: ctx, o: o, skip: skip, result: make(chan accountAssignment, 1)}
	if limit >= 0 {
		w.deadline = time.Now().Add(limit)
	}
	a.scheduleMu.Lock()
	if len(a.waiters) >= maxQueuedRequests {
		a.scheduleMu.Unlock()
		return nil, false, &fault.Failure{Kind: "busy", Code: "queue_full", Message: "请求等待队列已满", RetryAfter: time.Second}
	}
	a.waiters = append(a.waiters, w)
	a.scheduleMu.Unlock()
	a.notifyAvailable()
	defer func() {
		a.scheduleMu.Lock()
		for i, pending := range a.waiters {
			if pending == w {
				copy(a.waiters[i:], a.waiters[i+1:])
				a.waiters[len(a.waiters)-1] = nil
				a.waiters = a.waiters[:len(a.waiters)-1]
				break
			}
		}
		// Cancellation can race with a dispatch that already reserved a slot.
		select {
		case assigned := <-w.result:
			if assigned.account != nil {
				a.finishAccountAttempt(assigned.account, nil, false, nil)
			}
		default:
		}
		a.scheduleMu.Unlock()
		a.notifyAvailable()
	}()
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-a.ctx.Done():
		return nil, false, a.ctx.Err()
	case assigned := <-w.result:
		if assigned.failure != nil {
			return nil, assigned.busy, assigned.failure
		}
		return assigned.account, assigned.busy, nil
	}
}

// One dispatcher owns wakeups and cooldown timers for the entire queue.
func (a *App) runAccountQueue() {
	for a.ctx.Err() == nil {
		changed := a.availability()
		a.dispatchAccounts()
		a.scheduleMu.Lock()
		queued := len(a.waiters) > 0
		delay := a.nextBusyDelay()
		for _, w := range a.waiters {
			if !w.deadline.IsZero() {
				delay = min(delay, max(0, time.Until(w.deadline)))
			}
			if !w.o.RecheckAt.IsZero() {
				delay = min(delay, max(0, time.Until(w.o.RecheckAt)))
			}
		}
		a.scheduleMu.Unlock()
		var timer *time.Timer
		var tick <-chan time.Time
		if queued {
			timer = time.NewTimer(delay)
			tick = timer.C
		}
		select {
		case <-a.ctx.Done():
		case <-changed:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// Serve older compatible waiters first; a blocked key must not block other pools.
func (a *App) dispatchAccounts() {
	a.scheduleMu.Lock()
	defer a.scheduleMu.Unlock()
	// Share database state within this pass only. Each assignment checks live
	// occupancy and cooldowns when reserving a slot.
	snapshots := make(map[string][]schedulingPool)
	lastByGroupOrder := make(map[string]*accountWaiter)
	for _, w := range a.waiters {
		lastByGroupOrder[shared.Str(w.o.Key["group_order"])] = w
	}
	var minWindow int64
	retryLoaded := false
	pending := a.waiters[:0]
	for _, w := range a.waiters {
		groupOrder := shared.Str(w.o.Key["group_order"])
		last := lastByGroupOrder[groupOrder] == w
		pools, loaded := snapshots[groupOrder]
		if last {
			delete(snapshots, groupOrder)
		}
		if w.ctx.Err() != nil {
			continue
		}
		if !retryLoaded {
			params := a.retryParams()
			minWindow = int64(shared.Num(shared.Fallback(params["min_request_window_sec"], 60)) * 1000)
			retryLoaded = true
		}
		if !loaded {
			pools = a.schedulingPools(groupOrder)
			if !last {
				snapshots[groupOrder] = pools
			}
		}
		account, busy, upstreamBusy, failure := a.selectAccount(w.o, w.skip, pools, minWindow)
		if account == nil && upstreamBusy {
			w.deadline = time.Time{}
		}
		// Unlike the ordinary busy wait, a throttle recheck cannot be waived
		// by upstream concurrency. Return to infer so it can rebuild the skips.
		now := time.Now()
		recheck := !w.o.RecheckAt.IsZero() && !now.Before(w.o.RecheckAt)
		if account != nil || !busy || recheck || !w.deadline.IsZero() && !now.Before(w.deadline) {
			w.result <- accountAssignment{account: account, busy: busy, failure: failure}
		} else {
			pending = append(pending, w)
		}
	}
	clear(a.waiters[len(pending):])
	a.waiters = pending
}

func (a *App) nextBusyDelay() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	delay := time.Second
	for _, s := range a.states {
		if s.BusyUntil > shared.NowMS() {
			delay = min(delay, time.Duration(s.BusyUntil-shared.NowMS())*time.Millisecond)
		}
	}
	return max(time.Millisecond, delay)
}

func (a *App) admitAccount(account shared.M) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.state(shared.Str(account["id"]))
	if s.BusyGeneration == shared.Integer(account["_busy_generation"]) {
		s.BusyUntil, s.BusyProbe, s.BusyBackoffStep = 0, false, 0
		a.notifyAvailableLocked()
	}
}

// Publish upstream busy state before waking other requests on slot release.
func (a *App) finishAccountAttempt(account shared.M, failure *fault.Failure, completed bool, params shared.M) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if failure != nil {
		fault.Classify(failure)
	}
	a.finishPoolProbeLocked(account, failure, completed)
	s := a.state(shared.Str(account["id"]))
	s.Inflight = max(0, s.Inflight-1)
	s.LastUsed = shared.NowMS()
	if failure != nil && fault.Classify(failure).Kind == "busy" {
		// Only the single current recovery probe advances the backoff. Replies
		// from the same earlier batch may extend a deadline, never compound it.
		if shared.Truth(account["_busy_probe"]) && s.BusyGeneration == shared.Integer(account["_busy_generation"]) {
			s.BusyBackoffStep = min(7, s.BusyBackoffStep+1)
		}
		delay := accountBusyDelay(params, s.BusyBackoffStep, failure.RetryAfter)
		s.BusyGeneration++
		s.BusyUntil = max(s.BusyUntil, shared.NowMS()+max(50*time.Millisecond, delay).Milliseconds())
		s.BusyProbe = false
	} else if s.BusyGeneration == shared.Integer(account["_busy_generation"]) {
		s.BusyProbe = false
		if completed && failure == nil {
			s.BusyUntil, s.BusyBackoffStep = 0, 0
		}
	}
	a.notifyAvailableLocked()
}

func accountBusyDelay(params shared.M, step int, retryAfter time.Duration) time.Duration {
	delay := max(50*time.Millisecond, shared.Ms(shared.Fallback(params["rate_limit_retry_interval_ms"], 1000)))
	limit := max(5*time.Second, delay)
	for i := 0; i < step && delay < limit; i++ {
		delay = min(limit, delay*2)
	}
	var jitter time.Duration
	if shared.Truth(params["jitter"]) {
		jitter = time.Duration(rand.Float64() * float64(min(250*time.Millisecond, delay/5)))
	}
	return max(delay, retryAfter) + jitter
}
