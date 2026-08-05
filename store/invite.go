package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

// Invite statuses.
const (
	InvitePending  = "pending"
	InviteAccepted = "accepted"
	InviteRevoked  = "revoked"
)

// Invite represents a one-time user invitation for internal auth. The plaintext
// token is never stored; only token_hash (SHA-256) is persisted. The raw token
// is delivered to the invitee in the email link.
type Invite struct {
	ID             string
	Email          string
	Name           string
	Role           string
	TokenHash      string
	Status         string
	InvitedBy      string
	TokenExpiresAt time.Time
	AcceptedAt     time.Time
	AcceptedUserID string
	CreatedAt      time.Time
}

// GenerateInviteToken creates a cryptographically random raw invite token and
// its SHA-256 hex hash. Only the hash should be stored.
func GenerateInviteToken() (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw = "inv_" + hex.EncodeToString(b)
	hash, err = HashInviteToken(raw)
	return raw, hash, err
}

// HashInviteToken returns the SHA-256 hex digest of a raw invite token.
func HashInviteToken(raw string) (string, error) {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:]), nil
}

// Expired reports whether the invite is past its expiry and not already
// consumed. Accepted invitations never "expire" for display purposes.
func (i *Invite) Expired(now time.Time) bool {
	if i.Status == InviteAccepted {
		return false
	}
	return now.UTC().After(i.TokenExpiresAt.UTC())
}

// CreateInvite inserts a new invitation row.
func (s *Store) CreateInvite(inv *Invite) error {
	if inv.ID == "" {
		inv.ID = generateID()
	}
	if inv.Status == "" {
		inv.Status = InvitePending
	}
	_, err := s.db.Exec(
		`INSERT INTO invitations (id, email, name, role, token_hash, status, invited_by, token_expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		inv.ID, inv.Email, inv.Name, inv.Role, inv.TokenHash, inv.Status, inv.InvitedBy, inv.TokenExpiresAt,
	)
	return err
}

// GetInviteByTokenHash retrieves an invitation by its hashed token.
func (s *Store) GetInviteByTokenHash(hash string) (*Invite, error) {
	inv := &Invite{}
	var acceptedAt sql.NullTime
	err := s.db.QueryRow(
		`SELECT id, email, name, role, token_hash, status, COALESCE(invited_by, ''), token_expires_at, accepted_at, COALESCE(accepted_user_id, ''), created_at
		 FROM invitations WHERE token_hash = ?`, hash,
	).Scan(&inv.ID, &inv.Email, &inv.Name, &inv.Role, &inv.TokenHash, &inv.Status, &inv.InvitedBy,
		&inv.TokenExpiresAt, &acceptedAt, &inv.AcceptedUserID, &inv.CreatedAt)
	if err != nil {
		return nil, err
	}
	if acceptedAt.Valid {
		inv.AcceptedAt = acceptedAt.Time
	}
	return inv, nil
}

// GetInviteByID retrieves an invitation by its ID.
func (s *Store) GetInviteByID(id string) (*Invite, error) {
	inv := &Invite{}
	var acceptedAt sql.NullTime
	err := s.db.QueryRow(
		`SELECT id, email, name, role, token_hash, status, COALESCE(invited_by, ''), token_expires_at, accepted_at, COALESCE(accepted_user_id, ''), created_at
		 FROM invitations WHERE id = ?`, id,
	).Scan(&inv.ID, &inv.Email, &inv.Name, &inv.Role, &inv.TokenHash, &inv.Status, &inv.InvitedBy,
		&inv.TokenExpiresAt, &acceptedAt, &inv.AcceptedUserID, &inv.CreatedAt)
	if err != nil {
		return nil, err
	}
	if acceptedAt.Valid {
		inv.AcceptedAt = acceptedAt.Time
	}
	return inv, nil
}

// ListInvites returns all invitations ordered by creation time (newest first).
func (s *Store) ListInvites() ([]*Invite, error) {
	rows, err := s.db.Query(
		`SELECT id, email, name, role, token_hash, status, COALESCE(invited_by, ''), token_expires_at, accepted_at, COALESCE(accepted_user_id, ''), created_at
		 FROM invitations ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []*Invite
	for rows.Next() {
		inv := &Invite{}
		var acceptedAt sql.NullTime
		if err := rows.Scan(&inv.ID, &inv.Email, &inv.Name, &inv.Role, &inv.TokenHash, &inv.Status, &inv.InvitedBy,
			&inv.TokenExpiresAt, &acceptedAt, &inv.AcceptedUserID, &inv.CreatedAt); err != nil {
			return nil, err
		}
		if acceptedAt.Valid {
			inv.AcceptedAt = acceptedAt.Time
		}
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

// MarkInviteAccepted transitions an invitation to accepted and records who
// claimed it.
func (s *Store) MarkInviteAccepted(id, userID string) error {
	_, err := s.db.Exec(
		`UPDATE invitations SET status = ?, accepted_at = ?, accepted_user_id = ? WHERE id = ?`,
		InviteAccepted, time.Now().UTC(), userID, id,
	)
	return err
}

// RevokeInvite marks a pending invitation as revoked.
func (s *Store) RevokeInvite(id string) error {
	_, err := s.db.Exec(
		`UPDATE invitations SET status = ? WHERE id = ? AND status = ?`,
		InviteRevoked, id, InvitePending,
	)
	return err
}
