package agent

import "sync"

var remoteDeliveryMu sync.Mutex

// GuardRemoteDelivery and DrainRemoteDelivery share one mutex so turning the
// global remote-management switch off cannot race with agent.exec enqueue.
func GuardRemoteDelivery(allowed func() bool, fn func()) bool {
	remoteDeliveryMu.Lock()
	defer remoteDeliveryMu.Unlock()
	if allowed == nil || !allowed() {
		return false
	}
	fn()
	return true
}

func DrainRemoteDelivery(fn func() []RemovedV2Event) []RemovedV2Event {
	remoteDeliveryMu.Lock()
	defer remoteDeliveryMu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}
