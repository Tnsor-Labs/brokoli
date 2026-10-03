package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// SecretStoreStore persists secret stores (ADR-041 section 1).
//
// Optional capability, not embedded in Store (the same shape as
// PhysicalPlanStore): the built-in SQLite and Postgres stores implement
// it, and a caller type-asserts (`if ss, ok := s.(store.SecretStoreStore)`).
// A store that does not implement it has no secret stores, and secret://
// references fail on it with a message saying so.
//
// Names are unique within a workspace, and every read is scoped to one:
// there is no lookup across workspaces, so a store name cannot leak access
// from one workspace to another.
type SecretStoreStore interface {
	CreateSecretStore(s *models.SecretStore) error
	GetSecretStore(workspaceID, id string) (*models.SecretStore, error)
	GetSecretStoreByName(workspaceID, name string) (*models.SecretStore, error)
	ListSecretStores(workspaceID string) ([]models.SecretStore, error)
	UpdateSecretStore(s *models.SecretStore) error
	DeleteSecretStore(workspaceID, id string) error
}

// ErrSecretStoreNotFound is returned when no store matches in the workspace.
var ErrSecretStoreNotFound = errors.New("secret store not found")

// ErrSecretStoreNameTaken is returned when the workspace already has a
// store with that name.
var ErrSecretStoreNameTaken = errors.New("a secret store with this name already exists in the workspace")

type secretStoreSQL struct {
	db *sql.DB
	pg bool
}

func (q secretStoreSQL) ph(n int) string {
	if q.pg {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

func (q secretStoreSQL) migrate() error {
	ts := "TEXT"
	if q.pg {
		ts = "TIMESTAMPTZ"
	}
	if _, err := q.db.Exec(`CREATE TABLE IF NOT EXISTS secret_stores (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		org_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		provider TEXT NOT NULL,
		settings TEXT NOT NULL DEFAULT '{}',
		auth_method TEXT NOT NULL,
		auth_settings TEXT NOT NULL DEFAULT '{}',
		credential_ref TEXT NOT NULL DEFAULT '',
		created_at ` + ts + ` NOT NULL,
		updated_at ` + ts + ` NOT NULL)`); err != nil {
		return fmt.Errorf("create secret_stores: %w", err)
	}
	if _, err := q.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_secret_stores_ws_name ON secret_stores(workspace_id, name)`); err != nil {
		return fmt.Errorf("index secret_stores: %w", err)
	}
	return nil
}

func (q secretStoreSQL) timeArg(t time.Time) interface{} {
	if q.pg {
		return t.UTC()
	}
	return t.UTC().Format(timeFormat)
}

func encodeStringMap(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func decodeStringMap(s string) map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate key")
}

func (q secretStoreSQL) create(s *models.SecretStore) error {
	_, err := q.db.Exec(fmt.Sprintf(`INSERT INTO secret_stores
		(id, workspace_id, org_id, name, description, provider, settings, auth_method, auth_settings, credential_ref, created_at, updated_at)
		VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)`,
		q.ph(1), q.ph(2), q.ph(3), q.ph(4), q.ph(5), q.ph(6), q.ph(7), q.ph(8), q.ph(9), q.ph(10), q.ph(11), q.ph(12)),
		s.ID, s.WorkspaceID, s.OrgID, s.Name, s.Description, s.Provider, encodeStringMap(s.Settings),
		s.AuthMethod, encodeStringMap(s.AuthSettings), s.CredentialRef, q.timeArg(s.CreatedAt), q.timeArg(s.UpdatedAt))
	if isUniqueViolation(err) {
		return ErrSecretStoreNameTaken
	}
	return err
}

// #nosec G101 -- a column list; "credential_ref" names a column holding an encrypted:// reference, not a credential.
const secretStoreColumns = `id, workspace_id, org_id, name, description, provider, settings, auth_method, auth_settings, credential_ref, created_at, updated_at`

func (q secretStoreSQL) scan(row interface{ Scan(...interface{}) error }) (*models.SecretStore, error) {
	var s models.SecretStore
	var settings, authSettings string
	if q.pg {
		var c, u time.Time
		if err := row.Scan(&s.ID, &s.WorkspaceID, &s.OrgID, &s.Name, &s.Description, &s.Provider, &settings,
			&s.AuthMethod, &authSettings, &s.CredentialRef, &c, &u); err != nil {
			return nil, err
		}
		s.CreatedAt, s.UpdatedAt = c.UTC(), u.UTC()
	} else {
		var c, u string
		if err := row.Scan(&s.ID, &s.WorkspaceID, &s.OrgID, &s.Name, &s.Description, &s.Provider, &settings,
			&s.AuthMethod, &authSettings, &s.CredentialRef, &c, &u); err != nil {
			return nil, err
		}
		s.CreatedAt, _ = time.Parse(timeFormat, c)
		s.UpdatedAt, _ = time.Parse(timeFormat, u)
	}
	s.Settings, s.AuthSettings = decodeStringMap(settings), decodeStringMap(authSettings)
	return &s, nil
}

func (q secretStoreSQL) getWhere(where string, args ...interface{}) (*models.SecretStore, error) {
	s, err := q.scan(q.db.QueryRow(`SELECT `+secretStoreColumns+` FROM secret_stores WHERE `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSecretStoreNotFound
	}
	return s, err
}

func (q secretStoreSQL) get(workspaceID, id string) (*models.SecretStore, error) {
	return q.getWhere(fmt.Sprintf("workspace_id = %s AND id = %s", q.ph(1), q.ph(2)), workspaceID, id)
}

func (q secretStoreSQL) getByName(workspaceID, name string) (*models.SecretStore, error) {
	return q.getWhere(fmt.Sprintf("workspace_id = %s AND name = %s", q.ph(1), q.ph(2)), workspaceID, name)
}

func (q secretStoreSQL) list(workspaceID string) ([]models.SecretStore, error) {
	// #nosec G202 -- the concatenated parts are a constant column list and a placeholder; the workspace is a bound argument.
	rows, err := q.db.Query(`SELECT `+secretStoreColumns+` FROM secret_stores WHERE workspace_id = `+q.ph(1)+` ORDER BY name`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := []models.SecretStore{}
	for rows.Next() {
		s, err := q.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (q secretStoreSQL) update(s *models.SecretStore) error {
	res, err := q.db.Exec(fmt.Sprintf(`UPDATE secret_stores SET name = %s, description = %s, provider = %s, settings = %s,
		auth_method = %s, auth_settings = %s, credential_ref = %s, updated_at = %s
		WHERE workspace_id = %s AND id = %s`,
		q.ph(1), q.ph(2), q.ph(3), q.ph(4), q.ph(5), q.ph(6), q.ph(7), q.ph(8), q.ph(9), q.ph(10)),
		s.Name, s.Description, s.Provider, encodeStringMap(s.Settings), s.AuthMethod, encodeStringMap(s.AuthSettings),
		s.CredentialRef, q.timeArg(s.UpdatedAt), s.WorkspaceID, s.ID)
	if isUniqueViolation(err) {
		return ErrSecretStoreNameTaken
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSecretStoreNotFound
	}
	return nil
}

func (q secretStoreSQL) delete(workspaceID, id string) error {
	res, err := q.db.Exec(fmt.Sprintf(`DELETE FROM secret_stores WHERE workspace_id = %s AND id = %s`, q.ph(1), q.ph(2)), workspaceID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSecretStoreNotFound
	}
	return nil
}

func (s *SQLiteStore) secretStores() secretStoreSQL { return secretStoreSQL{db: s.db} }

func (s *SQLiteStore) CreateSecretStore(st *models.SecretStore) error {
	return s.secretStores().create(st)
}
func (s *SQLiteStore) GetSecretStore(workspaceID, id string) (*models.SecretStore, error) {
	return s.secretStores().get(workspaceID, id)
}
func (s *SQLiteStore) GetSecretStoreByName(workspaceID, name string) (*models.SecretStore, error) {
	return s.secretStores().getByName(workspaceID, name)
}
func (s *SQLiteStore) ListSecretStores(workspaceID string) ([]models.SecretStore, error) {
	return s.secretStores().list(workspaceID)
}
func (s *SQLiteStore) UpdateSecretStore(st *models.SecretStore) error {
	return s.secretStores().update(st)
}
func (s *SQLiteStore) DeleteSecretStore(workspaceID, id string) error {
	return s.secretStores().delete(workspaceID, id)
}

func (s *PostgresStore) secretStores() secretStoreSQL { return secretStoreSQL{db: s.db, pg: true} }

func (s *PostgresStore) CreateSecretStore(st *models.SecretStore) error {
	return s.secretStores().create(st)
}
func (s *PostgresStore) GetSecretStore(workspaceID, id string) (*models.SecretStore, error) {
	return s.secretStores().get(workspaceID, id)
}
func (s *PostgresStore) GetSecretStoreByName(workspaceID, name string) (*models.SecretStore, error) {
	return s.secretStores().getByName(workspaceID, name)
}
func (s *PostgresStore) ListSecretStores(workspaceID string) ([]models.SecretStore, error) {
	return s.secretStores().list(workspaceID)
}
func (s *PostgresStore) UpdateSecretStore(st *models.SecretStore) error {
	return s.secretStores().update(st)
}
func (s *PostgresStore) DeleteSecretStore(workspaceID, id string) error {
	return s.secretStores().delete(workspaceID, id)
}

var (
	_ SecretStoreStore = (*SQLiteStore)(nil)
	_ SecretStoreStore = (*PostgresStore)(nil)
)
