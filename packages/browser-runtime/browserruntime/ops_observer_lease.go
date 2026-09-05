//go:build !windows

package browserruntime

import (
	"sync"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

// OpsObserverLeaseManager owns the single Runtime-wide observation lease.
//
// The lease has to live outside OpsObserverServer because more than one
// listener can front it: the operator Ops socket and, once the authenticated
// observation bridge exists, a second read-only socket. If each server kept its
// own lease pointer, both callers would be admitted at the same time and the
// single-observer guarantee would silently disappear. Every listener sharing one
// manager is what makes "first come, first served, no preemption" true rather
// than merely documented.
type OpsObserverLeaseManager struct {
	mu     sync.Mutex
	now    func() time.Time
	active *opsObserverLease
}

func NewOpsObserverLeaseManager(now func() time.Time) *OpsObserverLeaseManager {
	if now == nil {
		now = time.Now
	}
	return &OpsObserverLeaseManager{now: now}
}

// Admit claims the lease. A caller that arrives while another listener holds it
// is refused with the same already-active error the single-server path used, so
// contention is indistinguishable from the caller's point of view.
func (manager *OpsObserverLeaseManager) Admit(
	request browserprotocol.OpsObserverRequest,
) *browserprotocol.OpsObserverError {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.active != nil {
		return browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverAlreadyActive,
			"another Ops Observer is active",
		)
	}
	manager.active = &opsObserverLease{
		id:        request.ObserverLeaseID,
		runID:     request.RunID,
		expiresAt: request.LeaseExpiresAt.UTC(),
		seen:      make(map[string]struct{}, maxOpsObserverRequestsPerLease),
	}
	return nil
}

func (manager *OpsObserverLeaseManager) Validate(
	request browserprotocol.OpsObserverRequest,
) *browserprotocol.OpsObserverError {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	lease := manager.active
	if lease == nil || lease.id != request.ObserverLeaseID || lease.runID != request.RunID ||
		!lease.expiresAt.Equal(request.LeaseExpiresAt.UTC()) ||
		!manager.now().UTC().Before(lease.expiresAt) {
		return browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverProtocolError,
			"Ops Observer lease is stale",
		)
	}
	return nil
}

func (manager *OpsObserverLeaseManager) AdmitRequest(
	leaseID, requestID string,
) *browserprotocol.OpsObserverError {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	lease := manager.active
	if lease == nil || lease.id != leaseID {
		return browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverProtocolError,
			"Ops Observer lease is stale",
		)
	}
	if _, exists := lease.seen[requestID]; exists {
		return browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverProtocolError,
			"Ops Observer request was replayed",
		)
	}
	if len(lease.seen) >= maxOpsObserverRequestsPerLease {
		return browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverProtocolError,
			"Ops Observer request limit reached",
		)
	}
	lease.seen[requestID] = struct{}{}
	return nil
}

func (manager *OpsObserverLeaseManager) NextSequence(
	leaseID string,
) (uint64, *browserprotocol.OpsObserverError) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	lease := manager.active
	if lease == nil || lease.id != leaseID {
		return 0, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverProtocolError,
			"Ops Observer lease is stale",
		)
	}
	lease.sequence++
	return lease.sequence, nil
}

// Release drops the lease only when the caller still owns it, so a late release
// from a previous holder cannot free a lease another listener has since taken.
func (manager *OpsObserverLeaseManager) Release(leaseID string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.active != nil && manager.active.id == leaseID {
		manager.active = nil
	}
}

func (manager *OpsObserverLeaseManager) held() bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.active != nil
}
