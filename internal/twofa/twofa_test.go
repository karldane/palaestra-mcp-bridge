package twofa

import (
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// fakeStore is a minimal in-memory SecretStore used to keep the Manager
// testable without a real SQLite database. Secrets are stored in plaintext.
type fakeStore struct {
	secrets map[string]string // userID -> plaintext secret
	methods map[string]string // userID -> method
	deleted []string
	revoked []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		secrets: map[string]string{},
		methods: map[string]string{},
	}
}

func (f *fakeStore) GetUser2FA(userID string) (string, bool, error) {
	m, ok := f.methods[userID]
	return m, ok, nil
}

func (f *fakeStore) GetUser2FASecret(userID string, userDEK []byte) (string, error) {
	s, ok := f.secrets[userID]
	if !ok {
		return "", ErrNo2FA
	}
	return s, nil
}

func (f *fakeStore) SetUser2FA(userID, method string, secret []byte, userDEK []byte) error {
	f.methods[userID] = method
	f.secrets[userID] = string(secret)
	return nil
}

func (f *fakeStore) DeleteUser2FA(userID string) error {
	delete(f.methods, userID)
	delete(f.secrets, userID)
	f.deleted = append(f.deleted, userID)
	return nil
}

func (f *fakeStore) RevokeWebSessions(userID string) error {
	f.revoked = append(f.revoked, userID)
	return nil
}

func TestTotpSetupGeneratesUsableSecret(t *testing.T) {
	m := NewTotpMethod()
	res, err := m.Setup("karl@example.com")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	if res.Method != "totp" {
		t.Errorf("expected method totp, got %s", res.Method)
	}
	if len(res.Secret) != 32 {
		t.Errorf("expected 32-char base32 secret, got %d", len(res.Secret))
	}
	if !strings.HasPrefix(res.OtpauthURL, "otpauth://totp/") {
		t.Errorf("expected otpauth URL, got %s", res.OtpauthURL)
	}
	// The returned secret must be able to generate a valid code.
	code, err := totp.GenerateCode(res.Secret, time.Now())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	if err := m.Verify(res.Secret, code); err != nil {
		t.Errorf("expected verify to pass for freshly generated secret, got %v", err)
	}
}

func TestTotpVerifyWrongCode(t *testing.T) {
	m := NewTotpMethod()
	res, _ := m.Setup("user@example.com")

	if err := m.Verify(res.Secret, "000000"); err == nil {
		t.Error("expected verify to reject a wrong code")
	}
}

func TestTotpVerifyEmptyCode(t *testing.T) {
	m := NewTotpMethod()
	res, _ := m.Setup("user@example.com")
	if err := m.Verify(res.Secret, ""); err == nil {
		t.Error("expected verify to reject an empty code")
	}
}

func TestTotpVerifyExpiredCode(t *testing.T) {
	m := NewTotpMethod()
	res, _ := m.Setup("user@example.com")

	// A code from way in the past (2000-01-01) must be rejected: skew is ±1
	// step so only the current and adjacent windows are accepted.
	expired, err := totp.GenerateCodeCustom(res.Secret, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), totp.ValidateOpts{
		Period:    30,
		Skew:      0,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("generate expired code: %v", err)
	}
	if err := m.Verify(res.Secret, expired); err == nil {
		t.Error("expected verify to reject a long-expired code")
	}
}

// TestTotpVerifyAcceptsCodeOneStepOld verifies that a code produced one full
// time step in the past (within the skew window) is still accepted, so modest
// device clock drift does not cause false "invalid code" rejections.
func TestTotpVerifyAcceptsCodeOneStepOld(t *testing.T) {
	m := NewTotpMethod()
	res, _ := m.Setup("user@example.com")

	old, err := totp.GenerateCode(res.Secret, time.Now().Add(-1*TOTPPeriod*time.Second))
	if err != nil {
		t.Fatalf("generate one-step-old code: %v", err)
	}
	if err := m.Verify(res.Secret, old); err != nil {
		t.Errorf("expected verify to accept a code one step old, got %v", err)
	}
}

// TestTotpVerifyAcceptsCodeTwoStepsOld verifies tolerance for a device clock
// ~45-60s behind the server. This requires TOTPSkew >= 2 (previously skew 1
// rejected it, which is a plausible cause of the live "invalid code" reports).
func TestTotpVerifyAcceptsCodeTwoStepsOld(t *testing.T) {
	m := NewTotpMethod()
	res, _ := m.Setup("user@example.com")

	old, err := totp.GenerateCode(res.Secret, time.Now().Add(-2*TOTPPeriod*time.Second).Add(-5*time.Second))
	if err != nil {
		t.Fatalf("generate two-steps-old code: %v", err)
	}
	if err := m.Verify(res.Secret, old); err != nil {
		t.Errorf("expected verify to accept a code two steps old (needs TOTPSkew>=2), got %v", err)
	}
}

func TestManagerDefaults(t *testing.T) {
	mgr, err := NewManager(newFakeStore(), []string{"totp"}, true)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if !mgr.Required() {
		t.Error("expected manager Required to be true")
	}
	methods := mgr.Methods()
	if len(methods) != 1 || methods[0] != "totp" {
		t.Errorf("expected methods [totp], got %v", methods)
	}
}

func TestManagerUnknownMethodRejected(t *testing.T) {
	if _, err := NewManager(newFakeStore(), []string{"webauthn"}, true); err == nil {
		t.Error("expected NewManager to reject an unknown method")
	}
}

func TestManagerHasMethod(t *testing.T) {
	fs := newFakeStore()
	mgr, _ := NewManager(fs, []string{"totp"}, true)

	has, _, _ := mgr.HasMethod("user@example.com")
	if has {
		t.Error("expected HasMethod false when nothing configured")
	}

	// Simulate a configured 2FA via the store.
	if err := fs.SetUser2FA("user@example.com", "totp", []byte("SECRETBASE32"), nil); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	has, method, _ := mgr.HasMethod("user@example.com")
	if !has {
		t.Error("expected HasMethod true after config")
	}
	if method != "totp" {
		t.Errorf("expected method totp, got %s", method)
	}
}

func TestManagerSetupAndEnable(t *testing.T) {
	fs := newFakeStore()
	mgr, _ := NewManager(fs, []string{"totp"}, true)

	res, err := mgr.Setup("user@example.com", "totp")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if res.Secret == "" {
		t.Error("expected a generated secret")
	}

	code, _ := totp.GenerateCode(res.Secret, time.Now())

	// Enable persists with the user DEK once a valid code is confirmed.
	if err := mgr.Enable("user@example.com", "totp", []byte(res.Secret), code, []byte("dek")); err != nil {
		t.Fatalf("enable: %v", err)
	}
	has, method, _ := mgr.HasMethod("user@example.com")
	if !has || method != "totp" {
		t.Errorf("expected 2FA configured after enable, got %v %v", has, method)
	}
}

func TestManagerVerifyLogin(t *testing.T) {
	fs := newFakeStore()
	mgr, _ := NewManager(fs, []string{"totp"}, true)

	res, _ := mgr.Setup("user@example.com", "totp")
	code, _ := totp.GenerateCode(res.Secret, time.Now())
	_ = mgr.Enable("user@example.com", "totp", []byte(res.Secret), code, []byte("dek"))

	code, _ = totp.GenerateCode(res.Secret, time.Now())
	if err := mgr.VerifyLogin("user@example.com", "totp", code, []byte("dek")); err != nil {
		t.Errorf("expected verify login to pass, got %v", err)
	}
	if err := mgr.VerifyLogin("user@example.com", "totp", "999999", []byte("dek")); err == nil {
		t.Error("expected verify login to reject a wrong code")
	}
}

func TestManagerVerifyLoginNoMethod(t *testing.T) {
	mgr, _ := NewManager(newFakeStore(), []string{"totp"}, true)
	if err := mgr.VerifyLogin("nobody@example.com", "totp", "123456", []byte("dek")); err == nil {
		t.Error("expected verify login to fail when no 2FA configured")
	}
}

func TestManagerDelete(t *testing.T) {
	fs := newFakeStore()
	mgr, _ := NewManager(fs, []string{"totp"}, true)

	_ = fs.SetUser2FA("user@example.com", "totp", []byte("SECRET"), nil)
	if err := mgr.Delete("user@example.com"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(fs.deleted) != 1 {
		t.Error("expected delete to remove the 2FA record")
	}
	if len(fs.revoked) != 1 {
		t.Error("expected delete to revoke sessions")
	}
	if has, _, _ := mgr.HasMethod("user@example.com"); has {
		t.Error("expected method to be removed after delete")
	}
}
