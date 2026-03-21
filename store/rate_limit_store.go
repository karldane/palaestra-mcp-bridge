package store

import (
	"github.com/mcp-bridge/mcp-bridge/enforcer"
)

func (s *EnforcerStore) ListRateLimitBucketConfigs() ([]enforcer.RateLimitBucketConfigRow, error) {
	rows, err := s.db.Query(`SELECT id, backend_id, bucket_type, capacity, refill_rate, created_at, updated_at FROM rate_limit_buckets ORDER BY backend_id, bucket_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []enforcer.RateLimitBucketConfigRow
	for rows.Next() {
		var c enforcer.RateLimitBucketConfigRow
		if err := rows.Scan(&c.ID, &c.BackendID, &c.BucketType, &c.Capacity, &c.RefillRate, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		configs = append(configs, c)
	}
	return configs, rows.Err()
}

func (s *EnforcerStore) UpsertRateLimitBucketConfig(config enforcer.RateLimitBucketConfigRow) error {
	_, err := s.db.Exec(`
		INSERT INTO rate_limit_buckets (id, backend_id, bucket_type, capacity, refill_rate, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(backend_id, bucket_type) DO UPDATE SET
			capacity=excluded.capacity,
			refill_rate=excluded.refill_rate,
			updated_at=excluded.updated_at`,
		config.ID, config.BackendID, config.BucketType, config.Capacity, config.RefillRate, config.CreatedAt, config.UpdatedAt)
	return err
}

func (s *EnforcerStore) ListRateLimitStates() ([]enforcer.RateLimitStateRow, error) {
	rows, err := s.db.Query(`SELECT id, user_id, backend_id, bucket_type, current_level, last_refill_at, created_at FROM rate_limit_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []enforcer.RateLimitStateRow
	for rows.Next() {
		var state enforcer.RateLimitStateRow
		if err := rows.Scan(&state.ID, &state.UserID, &state.BackendID, &state.BucketType, &state.CurrentLevel, &state.LastRefillAt, &state.CreatedAt); err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func (s *EnforcerStore) UpsertRateLimitState(state enforcer.RateLimitStateRow) error {
	_, err := s.db.Exec(`
		INSERT INTO rate_limit_states (id, user_id, backend_id, bucket_type, current_level, last_refill_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, backend_id, bucket_type) DO UPDATE SET
			current_level=excluded.current_level,
			last_refill_at=excluded.last_refill_at`,
		state.ID, state.UserID, state.BackendID, state.BucketType, state.CurrentLevel, state.LastRefillAt, state.CreatedAt)
	return err
}
