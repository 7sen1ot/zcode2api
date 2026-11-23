package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"sync"
	"time"
	accountdata "zcode2api/internal/server/account"
	"zcode2api/internal/server/fault"
	wire "zcode2api/internal/server/protocol"
	"zcode2api/internal/server/shared"
	"zcode2api/internal/server/storage"
)

const balanceCacheTTL = 2 * time.Second

const balanceSnapshotBaseline = time.Minute

type balanceFlight struct {
	done      chan struct{}
	cancel    context.CancelFunc
	waiters   int
	completed bool
	result    shared.M
	err       error
}

type balanceCacheEntry struct {
	scope      string
	generation uint64
	until      time.Time
	result     shared.M
}

type balanceSnapshotState struct {
	fingerprint string
	at          int64
}

type balanceManager struct {
	slots       chan struct{}
	mu          sync.Mutex
	flights     map[string]*balanceFlight
	cache       map[string]balanceCacheEntry
	generations map[string]uint64
	sequence    uint64
	applyMu     sync.Mutex
	applied     map[string]uint64
	snapshots   map[string]balanceSnapshotState // Protected by applyMu.
}

func (a *App) balanceScope(r shared.M) string {
	return shared.Hash(shared.JSON([]any{r["id"], r["enc_tokens"], r["device_mid"], r["proxy_url"], a.origin, wire.ZCodeVersion(), a.configValue("outbound_proxy")}))
}

func (a *App) invalidateBalance(id string) {
	a.balances.applyMu.Lock()
	defer a.balances.applyMu.Unlock()
	a.invalidateBalanceLocked(id)
}

func (a *App) markQuotaExhausted(id string) {
	a.balances.applyMu.Lock()
	defer a.balances.applyMu.Unlock()
	// A balance query started before this refusal must not unblock the account.
	a.invalidateBalanceLocked(id)
	a.mu.Lock()
	a.state(id).QuotaExhausted = true
	a.mu.Unlock()
}

// The caller holds applyMu across any credential/status update as well, so an
// earlier response cannot reactivate an account between the update and eviction.
func (a *App) invalidateBalanceLocked(id string) {
	a.balances.mu.Lock()
	defer a.balances.mu.Unlock()
	if a.balances.generations == nil {
		a.balances.generations = map[string]uint64{}
	}
	a.balances.generations[id]++
	delete(a.balances.cache, id)
}

func (a *App) balanceResultCurrent(r, result shared.M) bool {
	scope, tracked := result["_balance_scope"].(string)
	if !tracked {
		return true
	}
	if scope != a.balanceScope(r) {
		return false
	}
	a.balances.mu.Lock()
	defer a.balances.mu.Unlock()
	return shared.Integer(result["_balance_generation"]) == int64(a.balances.generations[shared.Str(r["id"])])
}

func (a *App) fetchBalance(ctx context.Context, r shared.M) (shared.M, error) {
	return a.sharedBalance(ctx, r, false)
}

// Explicit refreshes bypass the short success cache, but can still join a
// current request. Claims and credential changes invalidate earlier flights.
func (a *App) fetchBalanceFresh(ctx context.Context, r shared.M) (shared.M, error) {
	return a.sharedBalance(ctx, r, true)
}

func (a *App) sharedBalance(ctx context.Context, r shared.M, fresh bool) (shared.M, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := shared.Str(r["id"])
	r = a.account(id)
	if r == nil {
		return nil, fmt.Errorf("账号不存在")
	}
	scope := a.balanceScope(r)
	manager := &a.balances
	manager.mu.Lock()
	generation := manager.generations[id]
	if cached, ok := manager.cache[id]; !fresh && ok && cached.scope == scope && cached.generation == generation && time.Now().Before(cached.until) {
		result := cached.result
		manager.mu.Unlock()
		return shared.Clone(result), nil
	}
	key := fmt.Sprintf("%s|%s|%d", id, scope, generation)
	flight := manager.flights[key]
	if flight == nil {
		if manager.flights == nil {
			manager.flights = map[string]*balanceFlight{}
		}
		// One canceled caller must not terminate the request for other waiters.
		requestCtx, cancel := context.WithCancel(a.ctx)
		flight = &balanceFlight{done: make(chan struct{}), cancel: cancel}
		manager.flights[key] = flight
		a.background(func() {
			defer cancel()
			result, err := a.fetchBalanceUpstream(requestCtx, r)
			current := a.account(id)
			currentScope := a.balanceScope(current)
			manager.mu.Lock()
			if current == nil || currentScope != scope || manager.generations[id] != generation {
				err = &fault.Failure{Kind: "canceled", Code: "balance_context_changed", Message: "余额查询期间账号或额度已更新，请重新刷新", Retryable: true}
			}
			if err == nil {
				result["_balance_scope"], result["_balance_generation"] = scope, generation
				manager.sequence++
				result["_balance_sequence"] = manager.sequence
			}
			flight.result, flight.err, flight.completed = result, err, true
			if manager.flights[key] == flight {
				delete(manager.flights, key)
				if err == nil && requestCtx.Err() == nil && shared.Truth(result["ok"]) {
					if manager.cache == nil {
						manager.cache = map[string]balanceCacheEntry{}
					}
					if len(manager.cache) >= 128 {
						for accountID := range manager.cache {
							delete(manager.cache, accountID)
							break
						}
					}
					manager.cache[id] = balanceCacheEntry{scope: scope, generation: generation, until: time.Now().Add(balanceCacheTTL), result: result}
				}
			}
			close(flight.done)
			manager.mu.Unlock()
		})
	}
	flight.waiters++
	manager.mu.Unlock()
	select {
	case <-ctx.Done():
		manager.mu.Lock()
		flight.waiters--
		if flight.waiters == 0 && !flight.completed {
			if manager.flights[key] == flight {
				delete(manager.flights, key)
			}
			flight.cancel()
		}
		manager.mu.Unlock()
		return nil, ctx.Err()
	case <-flight.done:
		manager.mu.Lock()
		flight.waiters--
		result, err := flight.result, flight.err
		manager.mu.Unlock()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			var failure *fault.Failure
			if errors.As(err, &failure) {
				copy := *failure
				return nil, &copy
			}
			return nil, err
		}
		if !a.balanceResultCurrent(a.account(id), result) {
			return nil, &fault.Failure{Kind: "canceled", Code: "balance_context_changed", Message: "余额查询结果已失效，请重新刷新", Retryable: true}
		}
		return shared.Clone(result), nil
	}
}

func balancePayloadFingerprint(value any) string {
	payload := maps.Clone(accountdata.BalancePayload(value))
	delete(payload, "server_time")
	delete(payload, "logid")
	return shared.Hash(shared.JSON(payload))
}

func balanceSnapshotFingerprint(rows []any) string {
	values := make([]string, 0, len(rows))
	for _, value := range rows {
		r := shared.Obj(value)
		values = append(values, shared.JSON([]any{shared.Str(r["bucket_id"]), shared.Str(r["entitlement_id"]), shared.Str(r["meter"]), shared.Num(r["total_units"]), shared.Num(r["used_units"]), shared.Num(r["remaining_units"]), shared.Integer(r["expires_at"])}))
	}
	sort.Strings(values)
	return shared.Hash(shared.JSON(values))
}

// Called under applyMu. Changes are recorded immediately, with a periodic
// baseline for the existing rolling usage calculations. Commit each set as one
// transaction so a restart cannot leave a partially recorded snapshot.
func (a *App) saveBalanceSnapshot(id string, balances []any) error {
	if a.balances.snapshots == nil {
		a.balances.snapshots = map[string]balanceSnapshotState{}
	}
	previous, loaded := a.balances.snapshots[id]
	if !loaded {
		rows, err := storage.Query(a.db, "SELECT * FROM quota_snapshots WHERE account_id=? AND ts=(SELECT MAX(ts) FROM quota_snapshots WHERE account_id=?)", id, id)
		if err != nil {
			return err
		}
		previous.fingerprint = balanceSnapshotFingerprint(rows)
		if len(rows) > 0 {
			previous.at = shared.Integer(shared.Obj(rows[0])["ts"])
		}
	}
	rows := make([]any, 0, len(balances))
	for _, value := range balances {
		r := shared.Clone(shared.Obj(value))
		r["expires_at"] = accountdata.SecondsMS(r["expires_at"])
		rows = append(rows, r)
	}
	fingerprint := balanceSnapshotFingerprint(rows)
	now := shared.NowMS()
	if fingerprint == previous.fingerprint && now-previous.at < balanceSnapshotBaseline.Milliseconds() {
		a.balances.snapshots[id] = previous
		return nil
	}
	if len(rows) > 0 {
		tx, err := a.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, value := range rows {
			r := shared.Obj(value)
			if _, err := tx.Exec("INSERT INTO quota_snapshots(account_id,ts,bucket_id,entitlement_id,meter,total_units,used_units,remaining_units,expires_at) VALUES(?,?,?,?,?,?,?,?,?)", id, now, r["bucket_id"], r["entitlement_id"], r["meter"], r["total_units"], r["used_units"], r["remaining_units"], r["expires_at"]); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	a.balances.snapshots[id] = balanceSnapshotState{fingerprint: fingerprint, at: now}
	return nil
}

func (a *App) startBalanceSnapshotCleanup() {
	a.background(func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if a.ctx.Err() != nil {
				return
			}
			_ = a.exec("DELETE FROM quota_snapshots WHERE ts<?", shared.NowMS()-48*time.Hour.Milliseconds())
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}
