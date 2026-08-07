// Package twofa provides a method-abstracted two-factor authentication layer
// for the web login. The first (and currently only) method is TOTP
// (RFC 6238) via github.com/pquerna/otp. Future methods (email codes,
// WebAuthn, etc.) can be added by implementing the Method interface and
// registering them with the Manager.
package twofa

import (
	"errors"
	"fmt"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// ErrNo2FA is returned when a user has no configured 2FA method.
var ErrNo2FA = errors.New("no 2FA configured for user")

// ErrUnknownMethod is returned when an unregistered method ID is requested.
var ErrUnknownMethod = errors.New("unknown 2FA method")

// ErrInvalidCode is returned when a submitted code fails verification.
var ErrInvalidCode = errors.New("invalid 2FA code")

const (
	// Issuer is embedded in the otpauth:// URL so authenticator apps show
	// a recognisable account entry.
	Issuer = "MCP Bridge"
	// TOTPPeriod is the TOTP time step in seconds.
	TOTPPeriod = 30
	// TOTPSkew is how many adjacent time steps are accepted either side of
	// the current window, tolerating modest clock drift.
	TOTPSkew = 1
)

// SetupResult carries the one-time setup payload shown to the user during
// enrollment. The Secret is only ever returned at setup time; the stored
// value is the dual-key wrapped ciphertext.
type SetupResult struct {
	Method     string
	Secret     string
	OtpauthURL string
}

// Method is a pluggable 2FA strategy. Implementations must be safe to hold
// as singletons and must not retain user-specific state between calls.
type Method interface {
	// ID returns the stable method identifier (e.g. "totp").
	ID() string
	// Setup returns a freshly generated setup payload for the given account
	// name (typically the user email). It must NOT persist anything.
	Setup(account string) (SetupResult, error)
	// Verify validates a submitted code against the given plaintext secret.
	Verify(secret, code string) error
}

// SecretStore is the persistence surface the Manager needs. The real
// implementation is store.Store; the wrapping of the plaintext secret with
// the user DEK and master KEK happens inside the store's SetUser2FA and is
// reversed in GetUser2FASecret.
type SecretStore interface {
	// GetUser2FA reports the configured method for a user and whether 2FA is
	// enabled for them.
	GetUser2FA(userID string) (method string, enabled bool, err error)
	// GetUser2FASecret returns the unwrapped plaintext 2FA secret for a user,
	// requiring the user-derived DEK (both keys are required; there is no
	// master-only fallback).
	GetUser2FASecret(userID string, userDEK []byte) (string, error)
	// SetUser2FA persists a 2FA secret, wrapping it with the user DEK over
	// the master KEK.
	SetUser2FA(userID, method string, secret []byte, userDEK []byte) error
	// DeleteUser2FA removes a user's 2FA configuration.
	DeleteUser2FA(userID string) error
	// RevokeWebSessions invalidates all web sessions for a user.
	RevokeWebSessions(userID string) error
}

// Manager coordinates 2FA methods and their persistence.
type Manager struct {
	store    SecretStore
	required bool
	order    []string
	methods  map[string]Method
}

// NewManager builds a Manager with the given allowed method IDs (in order)
// and enforcement flag. Unknown method IDs are rejected.
func NewManager(st SecretStore, methodIDs []string, required bool) (*Manager, error) {
	if st == nil {
		return nil, errors.New("twofa: nil store")
	}
	m := &Manager{
		store:    st,
		required: required,
		methods:  make(map[string]Method),
	}
	for _, id := range methodIDs {
		method, ok := registeredMethods[id]
		if !ok {
			return nil, fmt.Errorf("twofa: %w: %s", ErrUnknownMethod, id)
		}
		m.order = append(m.order, id)
		m.methods[id] = method
	}
	if len(m.order) == 0 {
		return nil, errors.New("twofa: no methods configured")
	}
	return m, nil
}

// Required reports whether 2FA enforcement is on.
func (m *Manager) Required() bool { return m.required }

// Methods returns the configured method IDs in configuration order.
func (m *Manager) Methods() []string {
	out := make([]string, len(m.order))
	copy(out, m.order)
	return out
}

// HasMethod reports whether the user has an enabled 2FA method and which one.
func (m *Manager) HasMethod(userID string) (bool, string, error) {
	method, enabled, err := m.store.GetUser2FA(userID)
	if err != nil || !enabled {
		return false, "", nil
	}
	return true, method, nil
}

// Setup generates a fresh setup payload for the given method. Nothing is
// persisted; the caller must show the payload and then call Enable with a
// confirmed code.
func (m *Manager) Setup(userID, methodID string) (SetupResult, error) {
	method, ok := m.methods[methodID]
	if !ok {
		return SetupResult{}, fmt.Errorf("%w: %s", ErrUnknownMethod, methodID)
	}
	return method.Setup(userID)
}

// Enable verifies a submitted code against the freshly generated secret and,
// on success, persists the secret wrapped with the user DEK. This is the
// enrollment confirmation step.
func (m *Manager) Enable(userID, methodID string, secret []byte, code string, userDEK []byte) error {
	method, ok := m.methods[methodID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownMethod, methodID)
	}
	if err := method.Verify(string(secret), code); err != nil {
		return ErrInvalidCode
	}
	return m.store.SetUser2FA(userID, methodID, secret, userDEK)
}

// VerifyLogin validates a submitted code against the user's stored 2FA
// secret during login. The user DEK (available because the password was just
// verified) is required to unwrap the stored secret.
func (m *Manager) VerifyLogin(userID, methodID, code string, userDEK []byte) error {
	method, ok := m.methods[methodID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownMethod, methodID)
	}
	secret, err := m.store.GetUser2FASecret(userID, userDEK)
	if err != nil {
		return err
	}
	if err := method.Verify(secret, code); err != nil {
		return ErrInvalidCode
	}
	return nil
}

// Delete removes a user's 2FA configuration and revokes all of their web
// sessions so a lost or compromised device cannot ride an existing session.
func (m *Manager) Delete(userID string) error {
	if err := m.store.DeleteUser2FA(userID); err != nil {
		return err
	}
	return m.store.RevokeWebSessions(userID)
}

// registeredMethods holds the known 2FA methods by ID.
var registeredMethods = map[string]Method{
	"totp": NewTotpMethod(),
}

// ---------- TOTP method ----------

// totpMethod implements Method using RFC 6238 TOTP.
type totpMethod struct{}

// NewTotpMethod returns the singleton TOTP method implementation.
func NewTotpMethod() Method { return &totpMethod{} }

func (t *totpMethod) ID() string { return "totp" }

func (t *totpMethod) Setup(account string) (SetupResult, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      Issuer,
		AccountName: account,
		Period:      TOTPPeriod,
		SecretSize:  20, // 32 base32 chars
	})
	if err != nil {
		return SetupResult{}, err
	}
	return SetupResult{
		Method:     "totp",
		Secret:     key.Secret(),
		OtpauthURL: key.URL(),
	}, nil
}

func (t *totpMethod) Verify(secret, code string) error {
	if code == "" {
		return ErrInvalidCode
	}
	valid, err := totp.ValidateCustom(code, secret, time.Now(), totp.ValidateOpts{
		Period:    TOTPPeriod,
		Skew:      TOTPSkew,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil || !valid {
		return ErrInvalidCode
	}
	return nil
}
