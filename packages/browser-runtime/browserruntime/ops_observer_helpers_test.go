package browserruntime

// Test-only lease observation; production uses lease identity and Release.
func (manager *OpsObserverLeaseManager) held() bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.active != nil
}
