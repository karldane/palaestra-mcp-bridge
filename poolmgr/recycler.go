package poolmgr

import (
	"sync"
)

type SafetyRecycler struct {
	mu            sync.RWMutex
	poolManager   *PoolManager
	dedicated     map[string]map[string]*ManagedProcess
	recyclePolicy RecyclePolicy
}

type RecyclePolicy string

const (
	RecyclePolicyAlways    RecyclePolicy = "always"
	RecyclePolicyDedicated RecyclePolicy = "dedicated"
	RecyclePolicyOnError   RecyclePolicy = "on-error"
)

func NewSafetyRecycler(pm *PoolManager) *SafetyRecycler {
	return &SafetyRecycler{
		poolManager:   pm,
		dedicated:     make(map[string]map[string]*ManagedProcess),
		recyclePolicy: RecyclePolicyDedicated,
	}
}

func (sr *SafetyRecycler) AcquireProcess(backendID, userID string) *ManagedProcess {
	pm := sr.poolManager
	pool := pm.GetPool(backendID)

	select {
	case proc := <-pool.Warm:
		proc.UserID = userID
		pool.IncrementActive()

		if sr.recyclePolicy == RecyclePolicyDedicated {
			sr.mu.Lock()
			if sr.dedicated[backendID] == nil {
				sr.dedicated[backendID] = make(map[string]*ManagedProcess)
			}
			sr.dedicated[backendID][userID] = proc
			sr.mu.Unlock()
		}

		return proc
	default:
		return nil
	}
}

func (sr *SafetyRecycler) ReleaseProcess(backendID, userID string, proc *ManagedProcess, err error) {
	pm := sr.poolManager
	pool := pm.GetPool(backendID)

	shouldRecycle := sr.shouldRecycle(err)

	if shouldRecycle {
		proc.Kill()
		proc.mu.Lock()
		close(proc.LineChan)
		proc.mu.Unlock()

		pool.wg.Add(1)
		go pool.spawnAndHandshake()
	} else {
		if sr.recyclePolicy == RecyclePolicyDedicated {
			sr.mu.Lock()
			if sr.dedicated[backendID] != nil {
				delete(sr.dedicated[backendID], userID)
			}
			sr.mu.Unlock()
		}

		pool.Warm <- proc
	}

	pool.DecrementActive()
}

func (sr *SafetyRecycler) shouldRecycle(err error) bool {
	switch sr.recyclePolicy {
	case RecyclePolicyAlways:
		return true
	case RecyclePolicyOnError:
		return err != nil
	case RecyclePolicyDedicated:
		return false
	default:
		return false
	}
}

func (sr *SafetyRecycler) SetRecyclePolicy(policy RecyclePolicy) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.recyclePolicy = policy
}

func (sr *SafetyRecycler) GetDedicatedProcess(backendID, userID string) *ManagedProcess {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	if sr.dedicated[backendID] != nil {
		return sr.dedicated[backendID][userID]
	}
	return nil
}

func (sr *SafetyRecycler) ReleaseDedicated(backendID, userID string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if proc, ok := sr.dedicated[backendID][userID]; ok {
		proc.Kill()
		delete(sr.dedicated[backendID], userID)
	}
}
