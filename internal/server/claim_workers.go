package server

import "context"

const manualClaimConcurrency = 5

type manualClaimWorkerKey struct{}

// Lease a verification worker for the entire manual claim, so multiple tabs
// and batch requests share the same bound. Account locks still prevent two
// simultaneous submissions for the same account.
func (a *App) acquireManualClaimWorker(ctx context.Context) (context.Context, func(), error) {
	a.mu.Lock()
	if a.manualClaimSlots == nil {
		a.manualClaimSlots = make(chan int, manualClaimConcurrency)
		for worker := range manualClaimConcurrency {
			a.manualClaimSlots <- worker
		}
	}
	slots := a.manualClaimSlots
	a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	select {
	case worker := <-slots:
		return context.WithValue(ctx, manualClaimWorkerKey{}, worker), func() { slots <- worker }, nil
	case <-ctx.Done():
		return ctx, nil, ctx.Err()
	case <-a.ctx.Done():
		return ctx, nil, a.ctx.Err()
	}
}
