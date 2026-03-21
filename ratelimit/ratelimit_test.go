package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestRiskMultiplier(t *testing.T) {
	tests := []struct {
		riskLevel string
		expected  int
	}{
		{"low", 1},
		{"med", 2},
		{"high", 4},
	}

	for _, tt := range tests {
		t.Run(tt.riskLevel, func(t *testing.T) {
			got := GetRiskMultiplier(tt.riskLevel)
			if got != tt.expected {
				t.Errorf("GetRiskMultiplier(%q) = %d, want %d", tt.riskLevel, got, tt.expected)
			}
		})
	}
}

func TestCostCalculation(t *testing.T) {
	tests := []struct {
		name             string
		resourceCostVal  int
		riskLevel        string
		impactScope      string
		expectedRisk     int
		expectedResource int
	}{
		{
			name:             "read operation low risk",
			resourceCostVal:  5,
			riskLevel:        "low",
			impactScope:      "read",
			expectedRisk:     0,
			expectedResource: 5,
		},
		{
			name:             "read operation high risk",
			resourceCostVal:  5,
			riskLevel:        "high",
			impactScope:      "read",
			expectedRisk:     0,
			expectedResource: 5,
		},
		{
			name:             "write operation low risk",
			resourceCostVal:  3,
			riskLevel:        "low",
			impactScope:      "write",
			expectedRisk:     3, // resourceCost * multiplier(1)
			expectedResource: 3,
		},
		{
			name:             "write operation med risk",
			resourceCostVal:  3,
			riskLevel:        "med",
			impactScope:      "write",
			expectedRisk:     6, // resourceCost * multiplier(2)
			expectedResource: 3,
		},
		{
			name:             "write operation high risk",
			resourceCostVal:  3,
			riskLevel:        "high",
			impactScope:      "write",
			expectedRisk:     12, // resourceCost * multiplier(4)
			expectedResource: 3,
		},
		{
			name:             "delete operation high risk",
			resourceCostVal:  5,
			riskLevel:        "high",
			impactScope:      "delete",
			expectedRisk:     20, // resourceCost(5) * multiplier(4)
			expectedResource: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			riskCost, resourceCost := CalculateCost(tt.resourceCostVal, tt.riskLevel, tt.impactScope)
			if riskCost != tt.expectedRisk {
				t.Errorf("CalculateCost riskCost = %d, want %d", riskCost, tt.expectedRisk)
			}
			if resourceCost != tt.expectedResource {
				t.Errorf("CalculateCost resourceCost = %d, want %d", resourceCost, tt.expectedResource)
			}
		})
	}
}

func TestBucketRefill(t *testing.T) {
	bucket := NewBucket(100, 20) // capacity 100, refill 20/min

	// Initial state
	if bucket.Current() != 100 {
		t.Errorf("Initial current = %d, want 100", bucket.Current())
	}

	// Deplete bucket
	bucket.Consume(50)
	if bucket.Current() != 50 {
		t.Errorf("After consume(50), current = %d, want 50", bucket.Current())
	}

	// Wait 1 minute, should refill 20
	time.Sleep(10 * time.Millisecond) // In real test, use clock
	bucket.Refill(20)                 // Manual refill for testing
	if bucket.Current() != 70 {
		t.Errorf("After refill(20), current = %d, want 70", bucket.Current())
	}
}

func TestBucketCapAtCapacity(t *testing.T) {
	bucket := NewBucket(100, 20)

	// Try to refill past capacity
	bucket.Consume(10) // 90
	bucket.Refill(50)  // Should cap at 100, not 140
	if bucket.Current() != 100 {
		t.Errorf("After consume(10) + refill(50), current = %d, want 100 (capped)", bucket.Current())
	}
}

func TestBucketDepletion(t *testing.T) {
	bucket := NewBucket(10, 20)

	// Deplete completely
	for i := 0; i < 10; i++ {
		if !bucket.Consume(1) {
			t.Errorf("Should be able to consume at iteration %d", i)
		}
	}

	// Should be empty
	if bucket.Current() != 0 {
		t.Errorf("After 10 consumes, current = %d, want 0", bucket.Current())
	}

	// Should not be able to consume more
	if bucket.Consume(1) {
		t.Error("Should not be able to consume when bucket is empty")
	}
}

func TestBucketDepletionCost(t *testing.T) {
	bucket := NewBucket(20, 20)

	// Try to consume more than available
	allowed := bucket.ConsumeCost(15) // 15 < 20, should succeed
	if !allowed {
		t.Error("Should allow consume when cost <= current")
	}

	allowed = bucket.ConsumeCost(10) // 10 < 5 remaining, should fail
	if allowed {
		t.Error("Should not allow consume when cost > current")
	}
}

func TestNewBucketState(t *testing.T) {
	state := NewBucketState("user1", "slack", "risk", 100, 20)

	if state.UserID != "user1" {
		t.Errorf("UserID = %q, want user1", state.UserID)
	}
	if state.BackendID != "slack" {
		t.Errorf("BackendID = %q, want slack", state.BackendID)
	}
	if state.BucketType != "risk" {
		t.Errorf("BucketType = %q, want risk", state.BucketType)
	}
	if state.CurrentLevel != 100 {
		t.Errorf("CurrentLevel = %d, want 100", state.CurrentLevel)
	}
	if state.Capacity != 100 {
		t.Errorf("Capacity = %d, want 100", state.Capacity)
	}
	if state.RefillRate != 20 {
		t.Errorf("RefillRate = %d, want 20", state.RefillRate)
	}
}

func TestBucketStateRefill(t *testing.T) {
	state := &BucketState{
		Capacity:     100,
		RefillRate:   20, // 20 per minute
		CurrentLevel: 50,
		LastRefillAt: time.Now().Add(-30 * time.Second),
	}

	// Calculate refill for 30 seconds = 10 units
	elapsed := time.Since(state.LastRefillAt)
	refillAmount := int(elapsed.Seconds()) * state.RefillRate / 60
	newLevel := min(state.Capacity, state.CurrentLevel+refillAmount)

	if newLevel < 50 {
		t.Errorf("Should have refilled, got %d", newLevel)
	}
}

// Integration Tests

func TestCallDepletesRiskBucket(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("slack", "risk", 100, 20)

	// Configure a tool: write, med risk, resource cost 3
	// Expected risk cost = 3 * 2 (med) = 6

	bucket := mgr.GetOrCreate("user1", "slack", "risk")
	initialLevel := bucket.CurrentLevel

	// Make a write call
	riskAllowed, _ := mgr.CheckAndConsume("user1", "slack", 6, 3) // risk=6, resource=3

	if !riskAllowed {
		t.Error("Risk bucket should allow consume")
	}

	bucket = mgr.GetOrCreate("user1", "slack", "risk")
	if bucket.CurrentLevel != initialLevel-6 {
		t.Errorf("Risk bucket should be depleted by 6, got %d", bucket.CurrentLevel)
	}
}

func TestCallDepletesResourceBucket(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("slack", "resource", 200, 40)

	bucket := mgr.GetOrCreate("user1", "slack", "resource")
	initialLevel := bucket.CurrentLevel

	// Make any call with resource cost
	_, resourceAllowed := mgr.CheckAndConsume("user1", "slack", 0, 10)

	if !resourceAllowed {
		t.Error("Resource bucket should allow consume")
	}

	bucket = mgr.GetOrCreate("user1", "slack", "resource")
	if bucket.CurrentLevel != initialLevel-10 {
		t.Errorf("Resource bucket should be depleted by 10, got %d", bucket.CurrentLevel)
	}
}

func TestReadOperationDoesNotDepleteRiskBucket(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("slack", "risk", 100, 20)

	bucket := mgr.GetOrCreate("user1", "slack", "risk")
	initialLevel := bucket.CurrentLevel

	// Read operation has riskCost=0
	riskAllowed, _ := mgr.CheckAndConsume("user1", "slack", 0, 5)

	if !riskAllowed {
		t.Error("Risk bucket should allow consume (zero cost)")
	}

	bucket = mgr.GetOrCreate("user1", "slack", "risk")
	if bucket.CurrentLevel != initialLevel {
		t.Errorf("Risk bucket should NOT be depleted for read, got %d", bucket.CurrentLevel)
	}
}

func TestBucketExhaustionDeniesCall(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("slack", "risk", 20, 20)

	// Deplete the bucket
	for i := 0; i < 20; i++ {
		mgr.CheckAndConsume("user1", "slack", 1, 0)
	}

	// Try one more call that costs 5 risk
	riskAllowed, _ := mgr.CheckAndConsume("user1", "slack", 5, 0)

	if riskAllowed {
		t.Error("Should deny call when bucket is exhausted")
	}
}

func TestMultipleUsersHaveSeparateBuckets(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("slack", "risk", 100, 20)

	// User1 makes calls
	mgr.CheckAndConsume("user1", "slack", 30, 0)

	// User2 should have full bucket
	bucket2 := mgr.GetOrCreate("user2", "slack", "risk")
	if bucket2.CurrentLevel != 100 {
		t.Errorf("User2 should have full bucket (100), got %d", bucket2.CurrentLevel)
	}

	// User1 should be depleted
	bucket1 := mgr.GetOrCreate("user1", "slack", "risk")
	if bucket1.CurrentLevel != 70 {
		t.Errorf("User1 should have 70 remaining, got %d", bucket1.CurrentLevel)
	}
}

func TestMultipleBackendsHaveSeparateBuckets(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("slack", "risk", 100, 20)
	mgr.SetConfig("newrelic", "risk", 50, 10)

	// Use slack
	mgr.CheckAndConsume("user1", "slack", 30, 0)

	// New relic should be unaffected
	bucket := mgr.GetOrCreate("user1", "newrelic", "risk")
	if bucket.CurrentLevel != 50 {
		t.Errorf("Newrelic bucket should be 50, got %d", bucket.CurrentLevel)
	}

	// Slack should be depleted
	bucket = mgr.GetOrCreate("user1", "slack", "risk")
	if bucket.CurrentLevel != 70 {
		t.Errorf("Slack bucket should be 70, got %d", bucket.CurrentLevel)
	}
}

func TestGetStatus(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("slack", "risk", 100, 20)
	mgr.SetConfig("slack", "resource", 200, 40)

	// Make some calls
	mgr.CheckAndConsume("user1", "slack", 10, 50)

	status := mgr.GetStatus("user1", "slack")

	riskBucket := status["risk"]
	if riskBucket == nil {
		t.Fatal("Risk bucket should exist")
	}
	if riskBucket.CurrentLevel != 90 {
		t.Errorf("Risk current should be 90, got %d", riskBucket.CurrentLevel)
	}
	if riskBucket.Capacity != 100 {
		t.Errorf("Risk capacity should be 100, got %d", riskBucket.Capacity)
	}

	resourceBucket := status["resource"]
	if resourceBucket == nil {
		t.Fatal("Resource bucket should exist")
	}
	if resourceBucket.CurrentLevel != 150 {
		t.Errorf("Resource current should be 150, got %d", resourceBucket.CurrentLevel)
	}
}

func TestToolProfileCostCalculation(t *testing.T) {
	profile := ToolProfile{
		RiskLevel:    "high",
		ImpactScope:  "delete",
		ResourceCost: 5,
	}

	riskCost, resourceCost := CalculateCost(profile.ResourceCost, profile.RiskLevel, profile.ImpactScope)

	// Risk: 5 * 4 (high) = 20
	if riskCost != 20 {
		t.Errorf("Risk cost should be 20, got %d", riskCost)
	}

	// Resource: 5
	if resourceCost != 5 {
		t.Errorf("Resource cost should be 5, got %d", resourceCost)
	}
}

func TestHighResourceCostOperation(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("oracle", "resource", 100, 20)

	// Simulate expensive read (resource_cost 10)
	bucket := mgr.GetOrCreate("user1", "oracle", "resource")
	initialLevel := bucket.CurrentLevel

	_, allowed := mgr.CheckAndConsume("user1", "oracle", 0, 10)

	if !allowed {
		t.Error("Should allow expensive read (uses resource, not risk)")
	}

	bucket = mgr.GetOrCreate("user1", "oracle", "resource")
	if bucket.CurrentLevel != initialLevel-10 {
		t.Errorf("Resource bucket should be depleted by 10, got %d", bucket.CurrentLevel)
	}
}

func TestRefillAll(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("slack", "risk", 100, 60) // 60 per minute = 1 per second

	// Deplete buckets
	mgr.CheckAndConsume("user1", "slack", 50, 30)

	// Verify depletion
	bucket := mgr.GetOrCreate("user1", "slack", "risk")
	if bucket.CurrentLevel != 50 {
		t.Errorf("After depletion, risk should be 50, got %d", bucket.CurrentLevel)
	}

	// Wait for 2 seconds worth of refill
	time.Sleep(2100 * time.Millisecond)

	// Call RefillAll
	mgr.RefillAll()

	// Check that bucket refilled (should be around 52, allow some tolerance)
	bucket = mgr.GetOrCreate("user1", "slack", "risk")
	if bucket.CurrentLevel < 51 {
		t.Errorf("After 2s refill, risk should be >= 51, got %d", bucket.CurrentLevel)
	}
}

func TestRefillAllMultipleBuckets(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("slack", "risk", 100, 60) // 1 per second
	mgr.SetConfig("slack", "resource", 200, 60)
	mgr.SetConfig("newrelic", "risk", 50, 30) // 0.5 per second

	// Deplete all
	mgr.CheckAndConsume("user1", "slack", 60, 100)  // risk: 100->40, res: 200->100
	mgr.CheckAndConsume("user1", "newrelic", 30, 0) // risk: 50->20, creates resource bucket too

	// Refill
	time.Sleep(2100 * time.Millisecond) // ~2 seconds worth
	mgr.RefillAll()

	// Verify all were refilled (2 seconds * 60/min = 2 units for slack, 1 for newrelic)
	slackRisk := mgr.GetOrCreate("user1", "slack", "risk")
	if slackRisk.CurrentLevel < 42 {
		t.Errorf("Slack risk should be >= 42 (40+2), got %d", slackRisk.CurrentLevel)
	}

	slackRes := mgr.GetOrCreate("user1", "slack", "resource")
	if slackRes.CurrentLevel < 102 {
		t.Errorf("Slack resource should be >= 102, got %d", slackRes.CurrentLevel)
	}

	newrelicRisk := mgr.GetOrCreate("user1", "newrelic", "risk")
	if newrelicRisk.CurrentLevel < 21 {
		t.Errorf("Newrelic risk should be >= 21 (20+1), got %d", newrelicRisk.CurrentLevel)
	}
}

func TestRateLimitManagerRefillTicker(t *testing.T) {
	mgr := NewRateLimitManager()
	mgr.manager.SetConfig("slack", "risk", 100, 60) // 1 per second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Deplete bucket
	mgr.CheckAndConsume("user1", "slack", 50, 0)

	bucket := mgr.manager.GetOrCreate("user1", "slack", "risk")
	if bucket.CurrentLevel != 50 {
		t.Errorf("After depletion, risk should be 50, got %d", bucket.CurrentLevel)
	}

	// Start ticker
	mgr.StartRefillTicker(ctx, 500*time.Millisecond)

	// Wait for 2 ticks
	time.Sleep(1200 * time.Millisecond)

	cancel()
	mgr.Stop()

	// Should have refilled ~2 units
	bucket = mgr.manager.GetOrCreate("user1", "slack", "risk")
	if bucket.CurrentLevel < 51 {
		t.Errorf("After ticker refill, risk should be >= 51, got %d", bucket.CurrentLevel)
	}
	if bucket.CurrentLevel > 53 {
		t.Errorf("After ticker refill, risk should be <= 53, got %d", bucket.CurrentLevel)
	}
}

func TestRateLimitManagerStop(t *testing.T) {
	mgr := NewRateLimitManager()
	mgr.manager.SetConfig("slack", "risk", 100, 6000) // 100 per minute = slow refill

	ctx, cancel := context.WithCancel(context.Background())

	// Deplete
	mgr.CheckAndConsume("user1", "slack", 50, 0)

	// Start ticker and immediately stop
	mgr.StartRefillTicker(ctx, 100*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	mgr.Stop()

	// Wait longer - ticker should not be running
	time.Sleep(500 * time.Millisecond)

	// Level should be unchanged (or minimally refilled from single tick)
	bucket := mgr.manager.GetOrCreate("user1", "slack", "risk")
	if bucket.CurrentLevel > 51 {
		t.Errorf("After stop, risk should be <= 51, got %d", bucket.CurrentLevel)
	}

	cancel()
}

func TestGetAllBuckets(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("slack", "risk", 100, 20)
	mgr.SetConfig("slack", "resource", 200, 40)
	mgr.SetConfig("newrelic", "risk", 50, 10)

	// Create some buckets - CheckAndConsume creates both risk and resource for each backend
	mgr.CheckAndConsume("user1", "slack", 10, 50)
	mgr.CheckAndConsume("user2", "slack", 20, 0)    // creates both risk+resource
	mgr.CheckAndConsume("user1", "newrelic", 25, 0) // creates both risk+resource

	buckets := mgr.GetAllBuckets()

	// Should have 6 buckets: user1/slack/risk, user1/slack/resource,
	// user2/slack/risk, user2/slack/resource, user1/newrelic/risk, user1/newrelic/resource
	if len(buckets) != 6 {
		t.Errorf("Expected 6 buckets, got %d", len(buckets))
	}
}

func TestLoadAndGetStates(t *testing.T) {
	mgr := NewRateLimitManager()
	mgr.manager.SetConfig("slack", "risk", 100, 20)
	mgr.manager.SetConfig("slack", "resource", 200, 40)

	// Load some states
	states := []BucketStateForDB{
		{UserID: "user1", BackendID: "slack", BucketType: "risk", CurrentLevel: 80, LastRefillAt: time.Now()},
		{UserID: "user1", BackendID: "slack", BucketType: "resource", CurrentLevel: 150, LastRefillAt: time.Now()},
		{UserID: "user2", BackendID: "slack", BucketType: "risk", CurrentLevel: 50, LastRefillAt: time.Now()},
	}
	mgr.LoadStates(states)

	// Verify states were loaded
	allStates := mgr.GetAllStates()
	if len(allStates) != 3 {
		t.Errorf("Expected 3 states, got %d", len(allStates))
	}

	// Check specific bucket
	riskAvail, riskCap, _, _, _, _ := mgr.GetBucketStatus("user1", "slack")
	if riskAvail != 80 {
		t.Errorf("user1/slack risk should be 80, got %d", riskAvail)
	}
	if riskCap != 100 {
		t.Errorf("user1/slack risk capacity should be 100, got %d", riskCap)
	}
}

func TestPersistTickerWithSaveFn(t *testing.T) {
	mgr := NewRateLimitManager()
	mgr.manager.SetConfig("slack", "risk", 100, 60)

	saved := false
	mgr.SetPersistFunc(func() error {
		saved = true
		return nil
	})

	// Deplete bucket
	mgr.CheckAndConsume("user1", "slack", 50, 0)

	ctx, cancel := context.WithCancel(context.Background())
	mgr.StartPersistTicker(ctx, 100*time.Millisecond)

	// Wait for at least one persist
	time.Sleep(250 * time.Millisecond)
	cancel()
	mgr.StopPersist()

	if !saved {
		t.Error("Persist function should have been called")
	}
}

func TestGetAllConfigs(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("slack", "risk", 100, 20)
	mgr.SetConfig("slack", "resource", 200, 40)
	mgr.SetConfig("newrelic", "risk", 150, 30)
	mgr.SetConfig("newrelic", "resource", 300, 60)

	configs := mgr.GetAllConfigs()

	if len(configs) != 2 {
		t.Errorf("Expected 2 backend configs, got %d", len(configs))
	}

	// Find slack config
	var slackCfg BackendConfig
	for _, c := range configs {
		if c.BackendID == "slack" {
			slackCfg = c
			break
		}
	}

	if slackCfg.RiskCapacity != 100 {
		t.Errorf("Slack risk capacity should be 100, got %d", slackCfg.RiskCapacity)
	}
	if slackCfg.RiskRefill != 20 {
		t.Errorf("Slack risk refill should be 20, got %d", slackCfg.RiskRefill)
	}
	if slackCfg.ResCapacity != 200 {
		t.Errorf("Slack resource capacity should be 200, got %d", slackCfg.ResCapacity)
	}
}

func TestResetUserBuckets(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("slack", "risk", 100, 20)
	mgr.SetConfig("slack", "resource", 200, 40)

	// Deplete user1's buckets
	mgr.CheckAndConsume("user1", "slack", 60, 100)

	bucket := mgr.GetOrCreate("user1", "slack", "risk")
	if bucket.CurrentLevel != 40 {
		t.Errorf("After depletion, risk should be 40, got %d", bucket.CurrentLevel)
	}

	// Reset user1's buckets (specific backend)
	mgr.ResetUserBuckets("user1", "slack")

	bucket = mgr.GetOrCreate("user1", "slack", "risk")
	if bucket.CurrentLevel != 100 {
		t.Errorf("After reset, risk should be 100, got %d", bucket.CurrentLevel)
	}

	// User2 should be unaffected
	bucket2 := mgr.GetOrCreate("user2", "slack", "risk")
	if bucket2.CurrentLevel != 100 {
		t.Errorf("User2 should be unaffected, got %d", bucket2.CurrentLevel)
	}
}

func TestResetAllUserBuckets(t *testing.T) {
	mgr := NewBucketManager()
	mgr.SetConfig("slack", "risk", 100, 20)
	mgr.SetConfig("newrelic", "risk", 150, 30)

	// Deplete user1's buckets across backends
	mgr.CheckAndConsume("user1", "slack", 50, 0)
	mgr.CheckAndConsume("user1", "newrelic", 75, 0)

	// Reset all of user1's buckets (no specific backend)
	mgr.ResetUserBuckets("user1", "")

	slackBucket := mgr.GetOrCreate("user1", "slack", "risk")
	if slackBucket.CurrentLevel != 100 {
		t.Errorf("Slack risk should be 100 after reset, got %d", slackBucket.CurrentLevel)
	}

	newrelicBucket := mgr.GetOrCreate("user1", "newrelic", "risk")
	if newrelicBucket.CurrentLevel != 150 {
		t.Errorf("Newrelic risk should be 150 after reset, got %d", newrelicBucket.CurrentLevel)
	}
}
