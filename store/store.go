package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

// Store wraps the SQLite database and provides typed CRUD operations for
// backends, users, user tokens, OAuth sessions, OAuth codes, and OAuth clients.
type Store struct {
	db *sql.DB
}

// New opens (or creates) a SQLite database at path and runs migrations.
func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the raw *sql.DB for advanced use (e.g. transactions).
func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Incremental migrations for existing databases.
	// Each ALTER TABLE may fail with "duplicate column" — that is expected.
	s.db.Exec(`ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'user'`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN env_mappings TEXT NOT NULL DEFAULT '{}'`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN is_system INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN min_pool_size INTEGER NOT NULL DEFAULT 1`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN max_pool_size INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN tool_hints TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN backend_instructions TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN self_reporting INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`)

	// Rate limiting tables
	s.db.Exec(`CREATE TABLE IF NOT EXISTS rate_limit_buckets (
		id          TEXT PRIMARY KEY,
		backend_id  TEXT NOT NULL REFERENCES backends(id) ON DELETE CASCADE,
		bucket_type TEXT NOT NULL CHECK (bucket_type IN ('risk', 'resource')),
		capacity    INTEGER NOT NULL DEFAULT 100,
		refill_rate INTEGER NOT NULL DEFAULT 20,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(backend_id, bucket_type)
	)`)

	s.db.Exec(`CREATE TABLE IF NOT EXISTS rate_limit_states (
		id            TEXT PRIMARY KEY,
		user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		backend_id    TEXT NOT NULL REFERENCES backends(id) ON DELETE CASCADE,
		bucket_type   TEXT NOT NULL CHECK (bucket_type IN ('risk', 'resource')),
		current_level INTEGER NOT NULL DEFAULT 0,
		last_refill_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(user_id, backend_id, bucket_type)
	)`)

	s.db.Exec(`CREATE TABLE IF NOT EXISTS rate_limit_overrides (
		id                TEXT PRIMARY KEY,
		user_id           TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		backend_id        TEXT NOT NULL REFERENCES backends(id) ON DELETE CASCADE,
		bucket_type       TEXT NOT NULL CHECK (bucket_type IN ('risk', 'resource')),
		capacity_multiplier REAL NOT NULL DEFAULT 1.0,
		created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(user_id, backend_id, bucket_type)
	)`)

	// Migration: rename token to session_id in web_sessions
	// SQLite doesn't support ALTER TABLE rename column, so we handle this in queries
	// The code below checks and migrates if needed
	var hasOldSchema int
	s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('web_sessions') WHERE name='token'`).Scan(&hasOldSchema)
	if hasOldSchema > 0 {
		s.db.Exec(`ALTER TABLE web_sessions RENAME TO web_sessions_old`)
		s.db.Exec(`
			CREATE TABLE web_sessions (
				session_id TEXT PRIMARY KEY,
				user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				expires_at DATETIME NOT NULL,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			);
			INSERT INTO web_sessions (session_id, user_id, expires_at, created_at)
			SELECT token, user_id, expires_at, created_at FROM web_sessions_old;
			DROP TABLE web_sessions_old;
		`)
	}

	// Enforcer tables (policy enforcement system)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS enforcer_policies (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		scope       TEXT NOT NULL DEFAULT 'global',
		expression  TEXT NOT NULL,
		action      TEXT NOT NULL DEFAULT 'ALLOW',
		severity    TEXT NOT NULL DEFAULT 'MEDIUM',
		message     TEXT NOT NULL DEFAULT '',
		enabled     INTEGER NOT NULL DEFAULT 1,
		priority    INTEGER NOT NULL DEFAULT 100,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS enforcer_overrides (
		id          TEXT PRIMARY KEY,
		tool_name   TEXT NOT NULL,
		backend_id  TEXT,
		risk_level  TEXT NOT NULL DEFAULT 'medium',
		impact_scope TEXT NOT NULL DEFAULT 'read',
		resource_cost INTEGER NOT NULL DEFAULT 5,
		requires_hitl INTEGER NOT NULL DEFAULT 0,
		pii_exposure INTEGER NOT NULL DEFAULT 0,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(tool_name, backend_id)
	)`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS enforcer_approvals (
		id              TEXT PRIMARY KEY,
		user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		user_email      TEXT NOT NULL DEFAULT '',
		user_role       TEXT NOT NULL DEFAULT '',
		trust_level     INTEGER NOT NULL DEFAULT 50,
		tool_name       TEXT NOT NULL,
		tool_args       TEXT NOT NULL DEFAULT '{}',
		backend_id      TEXT NOT NULL,
		safety_profile  TEXT NOT NULL DEFAULT '{}',
		status          TEXT NOT NULL DEFAULT 'PENDING',
		requested_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at      DATETIME NOT NULL,
		approved_by     TEXT,
		approved_at     DATETIME,
		denial_reason   TEXT DEFAULT '',
		comments        TEXT DEFAULT '',
		policy_id       TEXT DEFAULT '',
		violation_msg   TEXT DEFAULT '',
		request_body    TEXT DEFAULT '',
		response_status INTEGER DEFAULT 0,
		response_body   TEXT DEFAULT '',
		executed_at     DATETIME,
		error_msg       TEXT DEFAULT ''
	)`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS enforcer_kill_switches (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		scope       TEXT NOT NULL DEFAULT 'global',
		enabled     INTEGER NOT NULL DEFAULT 0,
		enabled_at  DATETIME,
		enabled_by  TEXT,
		reason      TEXT DEFAULT '',
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(scope)
	)`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS enforcer_audit_log (
		id          TEXT PRIMARY KEY,
		request_id  TEXT NOT NULL,
		user_id     TEXT NOT NULL,
		tool_name   TEXT NOT NULL,
		action      TEXT NOT NULL,
		policy_id   TEXT,
		message     TEXT,
		context     TEXT DEFAULT '{}',
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS enforcer_tool_profiles (
		id              TEXT PRIMARY KEY,
		backend_id      TEXT NOT NULL,
		tool_name       TEXT NOT NULL,
		risk_level      TEXT NOT NULL DEFAULT 'medium',
		impact_scope    TEXT NOT NULL DEFAULT 'read',
		resource_cost   INTEGER NOT NULL DEFAULT 5,
		requires_hitl   INTEGER NOT NULL DEFAULT 0,
		pii_exposure    INTEGER NOT NULL DEFAULT 0,
		idempotent      INTEGER NOT NULL DEFAULT 0,
		raw_profile     TEXT DEFAULT '{}',
		scanned_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(backend_id, tool_name)
	)`)

	// Backend availability status table
	s.db.Exec(`CREATE TABLE IF NOT EXISTS backend_status (
		backend_id      TEXT PRIMARY KEY REFERENCES backends(id) ON DELETE CASCADE,
		status          TEXT NOT NULL DEFAULT 'unknown',
		last_attempt    DATETIME,
		last_success    DATETIME,
		retry_count     INTEGER NOT NULL DEFAULT 0,
		next_retry      DATETIME,
		error_message   TEXT DEFAULT '',
		updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)

	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS backends (
	id          TEXT PRIMARY KEY,
	command     TEXT NOT NULL,
	pool_size   INTEGER NOT NULL DEFAULT 1,
	tool_prefix TEXT NOT NULL DEFAULT '',
	env         TEXT NOT NULL DEFAULT '{}',
	env_mappings TEXT NOT NULL DEFAULT '{}',
	tool_hints  TEXT NOT NULL DEFAULT '',
	backend_instructions TEXT NOT NULL DEFAULT '',
	enabled     INTEGER NOT NULL DEFAULT 1,
	is_system   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS users (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL DEFAULT '',
	email      TEXT NOT NULL DEFAULT '',
	password   TEXT NOT NULL DEFAULT '',
	role       TEXT NOT NULL DEFAULT 'user',
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(email)
);

CREATE TABLE IF NOT EXISTS user_tokens (
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	backend_id TEXT NOT NULL REFERENCES backends(id) ON DELETE CASCADE,
	env_key    TEXT NOT NULL,
	value      TEXT NOT NULL,
	PRIMARY KEY (user_id, backend_id, env_key)
);

CREATE TABLE IF NOT EXISTS oauth_clients (
	client_id     TEXT PRIMARY KEY,
	client_secret TEXT NOT NULL DEFAULT '',
	redirect_uris TEXT NOT NULL DEFAULT '[]',
	client_name   TEXT NOT NULL DEFAULT '',
	created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS oauth_codes (
	code           TEXT PRIMARY KEY,
	user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	client_id      TEXT NOT NULL,
	redirect_uri   TEXT NOT NULL,
	code_challenge TEXT NOT NULL DEFAULT '',
	scope          TEXT NOT NULL DEFAULT '',
	expires_at     DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS oauth_sessions (
	access_token  TEXT PRIMARY KEY,
	refresh_token TEXT NOT NULL DEFAULT '',
	user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	client_id     TEXT NOT NULL,
	scope         TEXT NOT NULL DEFAULT '',
	expires_at    DATETIME NOT NULL,
	created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS web_sessions (
	session_id TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at DATETIME NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_keys (
	id          TEXT PRIMARY KEY,
	user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name        TEXT NOT NULL DEFAULT '',
	key_hash    TEXT NOT NULL,
	expires_at  DATETIME,
	last_used_at DATETIME,
	created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Cached capabilities for each backend (global - same tools for all users)
CREATE TABLE IF NOT EXISTS backend_capabilities (
	backend_id  TEXT PRIMARY KEY REFERENCES backends(id) ON DELETE CASCADE,
	tools       TEXT NOT NULL,
	tool_count  INTEGER NOT NULL DEFAULT 0,
	updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Global settings for MCP Bridge
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// ---------- Helpers ----------

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// GenerateID returns a random hex string (exported for external use).
func GenerateID() string {
	return generateID()
}
