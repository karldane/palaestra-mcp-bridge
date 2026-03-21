package store

import (
	"time"
)

// BackendStatus represents the availability status of a backend
type BackendStatus struct {
	BackendID    string
	Status       string // "available", "unavailable", "unknown"
	LastAttempt  *time.Time
	LastSuccess  *time.Time
	RetryCount   int
	NextRetry    *time.Time
	ErrorMessage string
	UpdatedAt    time.Time
}

// ListBackendStatuses returns status for all backends
func (s *Store) ListBackendStatuses() ([]BackendStatus, error) {
	rows, err := s.db.Query(`SELECT backend_id, status, last_attempt, last_success, retry_count, next_retry, error_message, updated_at FROM backend_status ORDER BY backend_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var statuses []BackendStatus
	for rows.Next() {
		var bs BackendStatus
		var lastAttempt, lastSuccess, nextRetry *time.Time
		if err := rows.Scan(&bs.BackendID, &bs.Status, &lastAttempt, &lastSuccess, &bs.RetryCount, &nextRetry, &bs.ErrorMessage, &bs.UpdatedAt); err != nil {
			return nil, err
		}
		bs.LastAttempt = lastAttempt
		bs.LastSuccess = lastSuccess
		bs.NextRetry = nextRetry
		statuses = append(statuses, bs)
	}
	return statuses, rows.Err()
}

// GetBackendStatus returns status for a specific backend
func (s *Store) GetBackendStatus(backendID string) (*BackendStatus, error) {
	var bs BackendStatus
	var lastAttempt, lastSuccess, nextRetry *time.Time
	err := s.db.QueryRow(`SELECT backend_id, status, last_attempt, last_success, retry_count, next_retry, error_message, updated_at FROM backend_status WHERE backend_id = ?`, backendID).
		Scan(&bs.BackendID, &bs.Status, &lastAttempt, &lastSuccess, &bs.RetryCount, &nextRetry, &bs.ErrorMessage, &bs.UpdatedAt)
	if err != nil {
		return nil, err
	}
	bs.LastAttempt = lastAttempt
	bs.LastSuccess = lastSuccess
	bs.NextRetry = nextRetry
	return &bs, nil
}

// UpdateBackendStatus updates or inserts backend status
func (s *Store) UpdateBackendStatus(bs BackendStatus) error {
	_, err := s.db.Exec(`
		INSERT INTO backend_status (backend_id, status, last_attempt, last_success, retry_count, next_retry, error_message, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(backend_id) DO UPDATE SET
			status = excluded.status,
			last_attempt = excluded.last_attempt,
			last_success = excluded.last_success,
			retry_count = excluded.retry_count,
			next_retry = excluded.next_retry,
			error_message = excluded.error_message,
			updated_at = CURRENT_TIMESTAMP`,
		bs.BackendID, bs.Status, bs.LastAttempt, bs.LastSuccess, bs.RetryCount, bs.NextRetry, bs.ErrorMessage)
	return err
}

// SetBackendAvailable marks a backend as available and resets retry count
func (s *Store) SetBackendAvailable(backendID string) error {
	now := time.Now()
	_, err := s.db.Exec(`
		INSERT INTO backend_status (backend_id, status, last_success, retry_count, updated_at)
		VALUES (?, 'available', ?, 0, CURRENT_TIMESTAMP)
		ON CONFLICT(backend_id) DO UPDATE SET
			status = 'available',
			last_success = excluded.last_success,
			retry_count = 0,
			updated_at = CURRENT_TIMESTAMP`,
		backendID, now)
	return err
}

// SetBackendUnavailable marks a backend as unavailable with retry info
func (s *Store) SetBackendUnavailable(backendID string, errMsg string) error {
	now := time.Now()
	// Get current retry count
	var retryCount int
	s.db.QueryRow(`SELECT retry_count FROM backend_status WHERE backend_id = ?`, backendID).Scan(&retryCount)

	// Calculate next retry time with exponential backoff capped at 30 minutes
	backoffSeconds := 1 << retryCount // 1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048
	if backoffSeconds > 1800 {        // 30 minutes
		backoffSeconds = 1800
	}
	nextRetry := now.Add(time.Duration(backoffSeconds) * time.Second)

	_, err := s.db.Exec(`
		INSERT INTO backend_status (backend_id, status, last_attempt, retry_count, next_retry, error_message, updated_at)
		VALUES (?, 'unavailable', ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(backend_id) DO UPDATE SET
			status = 'unavailable',
			last_attempt = excluded.last_attempt,
			retry_count = excluded.retry_count,
			next_retry = excluded.next_retry,
			error_message = excluded.error_message,
			updated_at = CURRENT_TIMESTAMP`,
		backendID, now, retryCount+1, nextRetry, errMsg)
	return err
}

// GetBackendsNeedingRetry returns backends that are due for a retry attempt
func (s *Store) GetBackendsNeedingRetry() ([]string, error) {
	rows, err := s.db.Query(`SELECT backend_id FROM backend_status WHERE status = 'unavailable' AND (next_retry IS NULL OR next_retry <= CURRENT_TIMESTAMP) ORDER BY retry_count`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backends []string
	for rows.Next() {
		var backendID string
		if err := rows.Scan(&backendID); err != nil {
			return nil, err
		}
		backends = append(backends, backendID)
	}
	return backends, rows.Err()
}

// GetUncachedBackends returns backends that have no cached capabilities
func (s *Store) GetUncachedBackends() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT b.id FROM backends b
		LEFT JOIN backend_capabilities c ON b.id = c.backend_id
		WHERE b.enabled = 1 AND c.backend_id IS NULL
		ORDER BY b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backends []string
	for rows.Next() {
		var backendID string
		if err := rows.Scan(&backendID); err != nil {
			return nil, err
		}
		backends = append(backends, backendID)
	}
	return backends, rows.Err()
}
