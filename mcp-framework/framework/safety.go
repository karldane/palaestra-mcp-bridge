package framework

// RiskLevel represents the potential for system damage
type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMed      RiskLevel = "med"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

// ImpactScope represents the nature of the operation
type ImpactScope string

const (
	ImpactRead   ImpactScope = "read"
	ImpactWrite  ImpactScope = "write"
	ImpactDelete ImpactScope = "delete"
	ImpactAdmin  ImpactScope = "admin"
)

// EnforcerProfile contains self-reported safety metadata for a tool
// This profile is transmitted during the tools/list handshake via annotations
type EnforcerProfile struct {
	// RiskLevel: potential for system damage (default: med)
	RiskLevel RiskLevel `json:"risk_level"`

	// ImpactScope: nature of the operation (default: read)
	ImpactScope ImpactScope `json:"impact_scope"`

	// ResourceCost: CPU/Memory/API-Credit weight 1-10 (default: 5)
	ResourceCost int `json:"resource_cost"`

	// PIIExposure: does the tool return sensitive user data? (default: true - assume sensitive)
	PIIExposure bool `json:"pii_exposure"`

	// Idempotent: is it safe to retry on timeout? (default: false)
	Idempotent bool `json:"idempotent"`

	// ApprovalReq: force Human-in-the-Loop regardless of role? (default: false)
	ApprovalReq bool `json:"approval_req"`
}

// DefaultEnforcerProfile returns the default safety profile
// Defaults: med risk, read impact, cost 5, PII true, idempotent false, approval false
func DefaultEnforcerProfile() EnforcerProfile {
	return EnforcerProfile{
		RiskLevel:    RiskMed,
		ImpactScope:  ImpactRead,
		ResourceCost: 5,
		PIIExposure:  true,
		Idempotent:   false,
		ApprovalReq:  false,
	}
}

// ProfileOption is a functional option for configuring EnforcerProfile
type ProfileOption func(*EnforcerProfile)

// WithRisk sets the risk level
func WithRisk(level RiskLevel) ProfileOption {
	return func(p *EnforcerProfile) {
		p.RiskLevel = level
	}
}

// WithImpact sets the impact scope
func WithImpact(scope ImpactScope) ProfileOption {
	return func(p *EnforcerProfile) {
		p.ImpactScope = scope
	}
}

// WithResourceCost sets the resource cost (1-10)
func WithResourceCost(cost int) ProfileOption {
	return func(p *EnforcerProfile) {
		if cost < 1 {
			cost = 1
		}
		if cost > 10 {
			cost = 10
		}
		p.ResourceCost = cost
	}
}

// WithPII sets whether the tool exposes PII
func WithPII(exposed bool) ProfileOption {
	return func(p *EnforcerProfile) {
		p.PIIExposure = exposed
	}
}

// WithIdempotent sets whether the tool is idempotent
func WithIdempotent(idempotent bool) ProfileOption {
	return func(p *EnforcerProfile) {
		p.Idempotent = idempotent
	}
}

// WithApprovalReq sets whether approval is required
func WithApprovalReq(required bool) ProfileOption {
	return func(p *EnforcerProfile) {
		p.ApprovalReq = required
	}
}

// NewEnforcerProfile creates a profile with the given options
func NewEnforcerProfile(opts ...ProfileOption) EnforcerProfile {
	profile := DefaultEnforcerProfile()
	for _, opt := range opts {
		opt(&profile)
	}
	return profile
}
