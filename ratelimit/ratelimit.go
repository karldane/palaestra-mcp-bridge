package ratelimit

import (
	"context"
	"sync"
	"time"
)

var riskMultipliers = map[string]int{
	"low":  1,
	"med":  2,
	"high": 4,
}

var impactScopesRequiringRisk = map[string]bool{
	"write":  true,
	"delete": true,
}

func GetRiskMultiplier(riskLevel string) int {
	if m, ok := riskMultipliers[riskLevel]; ok {
		return m
	}
	return 1
}

func IsRiskScoped(impactScope string) bool {
	return impactScopesRequiringRisk[impactScope]
}

type ToolProfile struct {
	RiskLevel    string
	ImpactScope  string
	ResourceCost int
}

func CalculateCost(resourceCost int, riskLevel, impactScope string) (riskCost int, resourceCostResult int) {
	resourceCostResult = resourceCost
	if IsRiskScoped(impactScope) {
		riskCost = resourceCost * GetRiskMultiplier(riskLevel)
	}
	return
}

type Bucket struct {
	mu         sync.Mutex
	capacity   int
	refillRate int // per minute
	current    int
	lastRefill time.Time
}

func NewBucket(capacity, refillRate int) *Bucket {
	return &Bucket{
		capacity:   capacity,
		refillRate: refillRate,
		current:    capacity,
		lastRefill: time.Now(),
	}
}

func (b *Bucket) Current() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.current
}

func (b *Bucket) Capacity() int {
	return b.capacity
}

func (b *Bucket) RefillRate() int {
	return b.refillRate
}

func (b *Bucket) Refill(amount int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.current = min(b.capacity, b.current+amount)
	b.lastRefill = time.Now()
}

func (b *Bucket) RefillForElapsed() {
	b.mu.Lock()
	defer b.mu.Unlock()

	elapsed := time.Since(b.lastRefill)
	refillAmount := int(elapsed.Seconds()) * b.refillRate / 60
	if refillAmount > 0 {
		b.current = min(b.capacity, b.current+refillAmount)
		b.lastRefill = time.Now()
	}
}

func (b *Bucket) Consume(amount int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.current < amount {
		return false
	}
	b.current -= amount
	return true
}

func (b *Bucket) ConsumeCost(cost int) bool {
	return b.Consume(cost)
}

func (b *Bucket) CanConsume(amount int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.current >= amount
}

func (b *Bucket) NextFullIn() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.current >= b.capacity {
		return 0
	}

	unitsNeeded := b.capacity - b.current
	secondsNeeded := unitsNeeded * 60 / b.refillRate
	return time.Duration(secondsNeeded) * time.Second
}

type BucketState struct {
	UserID       string
	BackendID    string
	BucketType   string
	Capacity     int
	RefillRate   int // per minute
	CurrentLevel int
	LastRefillAt time.Time
}

func NewBucketState(userID, backendID, bucketType string, capacity, refillRate int) *BucketState {
	return &BucketState{
		UserID:       userID,
		BackendID:    backendID,
		BucketType:   bucketType,
		Capacity:     capacity,
		RefillRate:   refillRate,
		CurrentLevel: capacity,
		LastRefillAt: time.Now(),
	}
}

func (b *BucketState) Refill() {
	elapsed := time.Since(b.LastRefillAt)
	refillAmount := int(elapsed.Seconds()) * b.RefillRate / 60
	if refillAmount > 0 {
		b.CurrentLevel = min(b.Capacity, b.CurrentLevel+refillAmount)
		b.LastRefillAt = time.Now()
	}
}

func (b *BucketState) CanConsume(cost int) bool {
	b.Refill()
	return b.CurrentLevel >= cost
}

func (b *BucketState) Consume(cost int) bool {
	if !b.CanConsume(cost) {
		return false
	}
	b.CurrentLevel -= cost
	return true
}

type BucketManager struct {
	mu      sync.RWMutex
	buckets map[string]*BucketState
	config  map[string]*BucketConfig
}

type BucketConfig struct {
	Capacity   int
	RefillRate int
}

func NewBucketManager() *BucketManager {
	return &BucketManager{
		buckets: make(map[string]*BucketState),
		config:  make(map[string]*BucketConfig),
	}
}

// RateLimitManager wraps BucketManager with tools for policy evaluation
type RateLimitManager struct {
	manager       *BucketManager
	stopCh        chan struct{}
	persistStopCh chan struct{}
	persistSaveFn func() error
}

// NewRateLimitManager creates a new RateLimitManager
func NewRateLimitManager() *RateLimitManager {
	return &RateLimitManager{
		manager:       NewBucketManager(),
		stopCh:        make(chan struct{}),
		persistStopCh: make(chan struct{}),
	}
}

// SetPersistFunc sets the function to call for persisting bucket states
func (m *RateLimitManager) SetPersistFunc(fn func() error) {
	m.persistSaveFn = fn
}

// StartRefillTicker starts a background goroutine that refills all buckets
// at the specified interval. Call Stop() to terminate.
func (m *RateLimitManager) StartRefillTicker(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.RefillAll()
			}
		}
	}()
}

// StartPersistTicker starts a background goroutine that persists bucket states
// at the specified interval. Call StopPersist() to terminate.
func (m *RateLimitManager) StartPersistTicker(ctx context.Context, interval time.Duration) {
	if m.persistSaveFn == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-m.persistStopCh:
				return
			case <-ticker.C:
				m.persistSaveFn()
			}
		}
	}()
}

// Stop terminates the refill ticker goroutine
func (m *RateLimitManager) Stop() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
}

// StopPersist terminates the persistence ticker goroutine
func (m *RateLimitManager) StopPersist() {
	select {
	case <-m.persistStopCh:
	default:
		close(m.persistStopCh)
	}
}

// RefillAll refills all buckets based on elapsed time
func (m *RateLimitManager) RefillAll() {
	m.manager.RefillAll()
}

// BucketStateForDB represents a bucket state for database persistence
type BucketStateForDB struct {
	UserID       string
	BackendID    string
	BucketType   string
	CurrentLevel int
	Capacity     int
	RefillRate   int
	LastRefillAt time.Time
}

// LoadStates loads bucket states from database
func (m *RateLimitManager) LoadStates(states []BucketStateForDB) {
	for _, state := range states {
		bucket := m.manager.GetOrCreate(state.UserID, state.BackendID, state.BucketType)
		bucket.CurrentLevel = state.CurrentLevel
		bucket.LastRefillAt = state.LastRefillAt
	}
}

// GetAllStates returns all bucket states for database persistence
func (m *RateLimitManager) GetAllStates() []BucketStateForDB {
	buckets := m.manager.GetAllBuckets()
	states := make([]BucketStateForDB, 0, len(buckets))
	for _, b := range buckets {
		states = append(states, BucketStateForDB{
			UserID:       b.UserID,
			BackendID:    b.BackendID,
			BucketType:   b.BucketType,
			Capacity:     b.Capacity,
			RefillRate:   b.RefillRate,
			CurrentLevel: b.CurrentLevel,
			LastRefillAt: b.LastRefillAt,
		})
	}
	return states
}

// ConfigDisplay represents a backend's rate limit configuration for display
type ConfigDisplay struct {
	BackendID    string
	RiskCapacity int
	RiskRefill   int
	ResCapacity  int
	ResRefill    int
}

// GetAllConfigs returns all configured backend rate limits
func (m *RateLimitManager) GetAllConfigs() []ConfigDisplay {
	configs := m.manager.GetAllConfigs()
	result := make([]ConfigDisplay, 0, len(configs))
	for _, c := range configs {
		result = append(result, ConfigDisplay{
			BackendID:    c.BackendID,
			RiskCapacity: c.RiskCapacity,
			RiskRefill:   c.RiskRefill,
			ResCapacity:  c.ResCapacity,
			ResRefill:    c.ResRefill,
		})
	}
	return result
}

// ResetUserBuckets resets all buckets for a specific user and optional backend
func (m *RateLimitManager) ResetUserBuckets(userID, backendID string) {
	m.manager.ResetUserBuckets(userID, backendID)
}

// SetDefaultConfig sets the default configuration for a backend
func (m *RateLimitManager) SetDefaultConfig(backendID string, riskCapacity, riskRefill, resourceCapacity, resourceRefill int) {
	m.manager.SetConfig(backendID, "risk", riskCapacity, riskRefill)
	m.manager.SetConfig(backendID, "resource", resourceCapacity, resourceRefill)
}

// CheckAndConsume checks if the call is allowed and consumes from buckets
// Returns: riskAllowed, resourceAllowed, riskCost, resourceCost
func (m *RateLimitManager) CheckAndConsume(userID, backendID string, riskCost, resourceCost int) (bool, bool) {
	return m.manager.CheckAndConsume(userID, backendID, riskCost, resourceCost)
}

// GetBucketStatus returns the current status of both buckets for CEL evaluation
func (m *RateLimitManager) GetBucketStatus(userID, backendID string) (riskAvailable, riskCapacity, riskRefill, resourceAvailable, resourceCapacity, resourceRefill int) {
	status := m.manager.GetStatus(userID, backendID)

	if status["risk"] != nil {
		riskAvailable = status["risk"].CurrentLevel
		riskCapacity = status["risk"].Capacity
		riskRefill = status["risk"].RefillRate
	}

	if status["resource"] != nil {
		resourceAvailable = status["resource"].CurrentLevel
		resourceCapacity = status["resource"].Capacity
		resourceRefill = status["resource"].RefillRate
	}

	return
}

// GetStatusMap returns bucket status as a map for JSON serialization
func (m *RateLimitManager) GetStatusMap(userID, backendID string) map[string]interface{} {
	riskAvail, riskCap, riskRefill, resAvail, resCap, resRefill := m.GetBucketStatus(userID, backendID)

	return map[string]interface{}{
		"risk_bucket": map[string]interface{}{
			"available":   riskAvail,
			"capacity":    riskCap,
			"refill_rate": riskRefill,
		},
		"resource_bucket": map[string]interface{}{
			"available":   resAvail,
			"capacity":    resCap,
			"refill_rate": resRefill,
		},
	}
}

func (m *BucketManager) bucketKey(userID, backendID, bucketType string) string {
	return userID + ":" + backendID + ":" + bucketType
}

func (m *BucketManager) SetConfig(backendID, bucketType string, capacity, refillRate int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := backendID + ":" + bucketType
	m.config[key] = &BucketConfig{
		Capacity:   capacity,
		RefillRate: refillRate,
	}
}

func (m *BucketManager) GetOrCreate(userID, backendID, bucketType string) *BucketState {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := m.bucketKey(userID, backendID, bucketType)
	if b, ok := m.buckets[key]; ok {
		return b
	}

	configKey := backendID + ":" + bucketType
	cfg, ok := m.config[configKey]
	if !ok {
		cfg = &BucketConfig{Capacity: 100, RefillRate: 20}
	}

	b := &BucketState{
		UserID:       userID,
		BackendID:    backendID,
		BucketType:   bucketType,
		Capacity:     cfg.Capacity,
		RefillRate:   cfg.RefillRate,
		CurrentLevel: cfg.Capacity,
		LastRefillAt: time.Now(),
	}
	m.buckets[key] = b
	return b
}

func (m *BucketManager) CheckAndConsume(userID, backendID string, riskCost, resourceCost int) (riskAllowed, resourceAllowed bool) {
	riskBucket := m.GetOrCreate(userID, backendID, "risk")
	resourceBucket := m.GetOrCreate(userID, backendID, "resource")

	riskAllowed = riskBucket.Consume(riskCost)
	resourceAllowed = resourceBucket.Consume(resourceCost)

	return
}

func (m *BucketManager) GetStatus(userID, backendID string) map[string]*BucketState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return map[string]*BucketState{
		"risk":     m.buckets[m.bucketKey(userID, backendID, "risk")],
		"resource": m.buckets[m.bucketKey(userID, backendID, "resource")],
	}
}

func (m *BucketManager) RefillAll() {
	m.mu.RLock()
	buckets := make([]*BucketState, 0, len(m.buckets))
	for _, b := range m.buckets {
		buckets = append(buckets, b)
	}
	m.mu.RUnlock()

	for _, b := range buckets {
		b.Refill()
	}
}

func (m *BucketManager) GetAllBuckets() []BucketState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]BucketState, 0, len(m.buckets))
	for _, b := range m.buckets {
		result = append(result, *b)
	}
	return result
}

// BackendConfig represents a backend's rate limit configuration
type BackendConfig struct {
	BackendID    string
	RiskCapacity int
	RiskRefill   int
	ResCapacity  int
	ResRefill    int
}

// GetAllConfigs returns all configured backend rate limits
func (m *BucketManager) GetAllConfigs() []BackendConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	configMap := make(map[string]BackendConfig)
	for key, cfg := range m.config {
		parts := splitConfigKey(key)
		if len(parts) != 2 {
			continue
		}
		backendID := parts[0]
		bucketType := parts[1]

		existing, ok := configMap[backendID]
		if !ok {
			existing = BackendConfig{BackendID: backendID}
		}

		if bucketType == "risk" {
			existing.RiskCapacity = cfg.Capacity
			existing.RiskRefill = cfg.RefillRate
		} else if bucketType == "resource" {
			existing.ResCapacity = cfg.Capacity
			existing.ResRefill = cfg.RefillRate
		}
		configMap[backendID] = existing
	}

	result := make([]BackendConfig, 0, len(configMap))
	for _, c := range configMap {
		result = append(result, c)
	}
	return result
}

func splitConfigKey(key string) []string {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return []string{key[:i], key[i+1:]}
		}
	}
	return nil
}

// ResetUserBuckets resets all buckets for a specific user and optional backend
func (m *BucketManager) ResetUserBuckets(userID, backendID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	prefix := userID + ":"
	if backendID != "" {
		prefix = userID + ":" + backendID + ":"
	}

	for key, b := range m.buckets {
		if hasPrefix(key, prefix) {
			b.CurrentLevel = b.Capacity
			b.LastRefillAt = time.Now()
		}
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
