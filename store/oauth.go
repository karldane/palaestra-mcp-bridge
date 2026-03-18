package store

import (
	"time"
)

// ---------- OAuth Clients (Dynamic Registration) ----------

// OAuthClient represents a registered OAuth 2.1 client.
type OAuthClient struct {
	ClientID     string
	ClientSecret string
	RedirectURIs string // JSON array
	ClientName   string
	CreatedAt    time.Time
}

// CreateOAuthClient registers a new OAuth client.
func (s *Store) CreateOAuthClient(c *OAuthClient) error {
	if c.ClientID == "" {
		c.ClientID = generateID()
	}
	_, err := s.db.Exec(
		`INSERT INTO oauth_clients (client_id, client_secret, redirect_uris, client_name)
		 VALUES (?, ?, ?, ?)`,
		c.ClientID, c.ClientSecret, c.RedirectURIs, c.ClientName,
	)
	return err
}

// GetOAuthClient retrieves an OAuth client by ID.
func (s *Store) GetOAuthClient(clientID string) (*OAuthClient, error) {
	c := &OAuthClient{}
	err := s.db.QueryRow(
		`SELECT client_id, client_secret, redirect_uris, client_name, created_at
		 FROM oauth_clients WHERE client_id = ?`, clientID,
	).Scan(&c.ClientID, &c.ClientSecret, &c.RedirectURIs, &c.ClientName, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListOAuthClients returns all OAuth clients ordered by creation date.
func (s *Store) ListOAuthClients() ([]OAuthClient, error) {
	rows, err := s.db.Query(
		`SELECT client_id, client_secret, redirect_uris, client_name, created_at FROM oauth_clients ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clients []OAuthClient
	for rows.Next() {
		var c OAuthClient
		if err := rows.Scan(&c.ClientID, &c.ClientSecret, &c.RedirectURIs, &c.ClientName, &c.CreatedAt); err != nil {
			return nil, err
		}
		clients = append(clients, c)
	}
	return clients, nil
}

// DeleteOAuthClient removes an OAuth client.
func (s *Store) DeleteOAuthClient(clientID string) error {
	_, err := s.db.Exec(`DELETE FROM oauth_clients WHERE client_id = ?`, clientID)
	return err
}

// ---------- OAuth Codes ----------

// OAuthCode represents an authorization code for OAuth 2.1 flow.
type OAuthCode struct {
	Code          string
	UserID        string
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Scope         string
	ExpiresAt     time.Time
}

// CreateOAuthCode creates a new authorization code.
func (s *Store) CreateOAuthCode(c *OAuthCode) error {
	if c.Code == "" {
		c.Code = generateID()
	}
	_, err := s.db.Exec(
		`INSERT INTO oauth_codes (code, user_id, client_id, redirect_uri, code_challenge, scope, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.Code, c.UserID, c.ClientID, c.RedirectURI, c.CodeChallenge, c.Scope, c.ExpiresAt,
	)
	return err
}

// GetOAuthCode retrieves an authorization code.
func (s *Store) GetOAuthCode(code string) (*OAuthCode, error) {
	c := &OAuthCode{}
	err := s.db.QueryRow(
		`SELECT code, user_id, client_id, redirect_uri, code_challenge, scope, expires_at
		 FROM oauth_codes WHERE code = ?`, code,
	).Scan(&c.Code, &c.UserID, &c.ClientID, &c.RedirectURI, &c.CodeChallenge, &c.Scope, &c.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// DeleteOAuthCode removes an authorization code.
func (s *Store) DeleteOAuthCode(code string) error {
	_, err := s.db.Exec(`DELETE FROM oauth_codes WHERE code = ?`, code)
	return err
}

// DeleteExpiredCodes removes expired authorization codes.
func (s *Store) DeleteExpiredCodes() (int64, error) {
	result, err := s.db.Exec(`DELETE FROM oauth_codes WHERE expires_at < ?`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ---------- OAuth Sessions (access tokens) ----------

// OAuthSession represents an active OAuth access token session.
type OAuthSession struct {
	AccessToken  string
	RefreshToken string
	UserID       string
	ClientID     string
	Scope        string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

// CreateOAuthSession creates a new access token session.
func (s *Store) CreateOAuthSession(sess *OAuthSession) error {
	if sess.AccessToken == "" {
		sess.AccessToken = generateID()
	}
	if sess.RefreshToken == "" {
		sess.RefreshToken = generateID()
	}
	_, err := s.db.Exec(
		`INSERT INTO oauth_sessions (access_token, refresh_token, user_id, client_id, scope, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sess.AccessToken, sess.RefreshToken, sess.UserID, sess.ClientID, sess.Scope, sess.ExpiresAt,
	)
	return err
}

// GetOAuthSession retrieves a session by access token.
func (s *Store) GetOAuthSession(accessToken string) (*OAuthSession, error) {
	sess := &OAuthSession{}
	err := s.db.QueryRow(
		`SELECT access_token, refresh_token, user_id, client_id, scope, expires_at, created_at
		 FROM oauth_sessions WHERE access_token = ?`, accessToken,
	).Scan(&sess.AccessToken, &sess.RefreshToken, &sess.UserID, &sess.ClientID,
		&sess.Scope, &sess.ExpiresAt, &sess.CreatedAt)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// GetOAuthSessionByRefresh retrieves a session by refresh token.
func (s *Store) GetOAuthSessionByRefresh(refreshToken string) (*OAuthSession, error) {
	sess := &OAuthSession{}
	err := s.db.QueryRow(
		`SELECT access_token, refresh_token, user_id, client_id, scope, expires_at, created_at
		 FROM oauth_sessions WHERE refresh_token = ?`, refreshToken,
	).Scan(&sess.AccessToken, &sess.RefreshToken, &sess.UserID, &sess.ClientID,
		&sess.Scope, &sess.ExpiresAt, &sess.CreatedAt)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// DeleteOAuthSession removes a session by access token.
func (s *Store) DeleteOAuthSession(accessToken string) error {
	_, err := s.db.Exec(`DELETE FROM oauth_sessions WHERE access_token = ?`, accessToken)
	return err
}

// DeleteExpiredSessions removes expired access token sessions.
func (s *Store) DeleteExpiredSessions() (int64, error) {
	result, err := s.db.Exec(`DELETE FROM oauth_sessions WHERE expires_at < ?`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
