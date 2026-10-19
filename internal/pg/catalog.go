// SPDX-License-Identifier: AGPL-3.0-only

package pg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DBInfo describes a database.
type DBInfo struct {
	Name            string
	Owner           string
	AllowConn       bool
	ConnLimit       int
	SizeBytes       int64
	CollationStale  bool
	ActiveSessions  int
}

// ServerInfo describes the server.
type ServerInfo struct {
	Version        string
	VersionNum     int
	StartedAt      time.Time
	MaxConnections int
	Connections    int
	DataChecksums  bool
	SSL            bool
}

// Server returns server information.
func (c *Client) Server(ctx context.Context) (ServerInfo, error) {
	p, err := c.Pool()
	if err != nil {
		return ServerInfo{}, err
	}
	var s ServerInfo
	var checksums, ssl string
	err = p.QueryRow(ctx, `SELECT current_setting('server_version'), current_setting('server_version_num')::int,
		pg_postmaster_start_time(), current_setting('max_connections')::int,
		(SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend')::int,
		current_setting('data_checksums'), current_setting('ssl')`).
		Scan(&s.Version, &s.VersionNum, &s.StartedAt, &s.MaxConnections, &s.Connections, &checksums, &ssl)
	if err != nil {
		return s, err
	}
	s.DataChecksums = checksums == "on"
	s.SSL = ssl == "on"
	if i := strings.IndexByte(s.Version, ' '); i > 0 {
		s.Version = s.Version[:i]
	}
	return s, nil
}

// Databases lists non-template databases.
func (c *Client) Databases(ctx context.Context) ([]DBInfo, error) {
	p, err := c.Pool()
	if err != nil {
		return nil, err
	}
	rows, err := p.Query(ctx, `SELECT d.datname, r.rolname, d.datallowconn, d.datconnlimit, pg_database_size(d.oid),
		COALESCE(d.datcollversion <> pg_database_collation_actual_version(d.oid), false),
		(SELECT count(*) FROM pg_stat_activity a WHERE a.datid = d.oid AND a.backend_type = 'client backend')::int
		FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba
		WHERE NOT d.datistemplate ORDER BY d.datname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DBInfo
	for rows.Next() {
		var d DBInfo
		if err := rows.Scan(&d.Name, &d.Owner, &d.AllowConn, &d.ConnLimit, &d.SizeBytes, &d.CollationStale, &d.ActiveSessions); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Database returns one database or nil if it does not exist.
func (c *Client) Database(ctx context.Context, name string) (*DBInfo, error) {
	all, err := c.Databases(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, nil
}

// RoleExists reports whether a role exists.
func (c *Client) RoleExists(ctx context.Context, name string) (bool, error) {
	p, err := c.Pool()
	if err != nil {
		return false, err
	}
	var ok bool
	err = p.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, name).Scan(&ok)
	return ok, err
}

// DatabasesOwnedBy lists databases owned by role.
func (c *Client) DatabasesOwnedBy(ctx context.Context, role string) ([]string, error) {
	p, err := c.Pool()
	if err != nil {
		return nil, err
	}
	rows, err := p.Query(ctx, `SELECT d.datname FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba WHERE r.rolname = $1 ORDER BY 1`, role)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Session is a client backend.
type Session struct {
	PID         int32
	User        string
	ClientAddr  string
	Application string
	Started     time.Time
	State       string
	StateChange *time.Time
}

// Sessions lists client sessions on db.
func (c *Client) Sessions(ctx context.Context, db string) ([]Session, error) {
	p, err := c.Pool()
	if err != nil {
		return nil, err
	}
	rows, err := p.Query(ctx, `SELECT pid, COALESCE(usename, ''), COALESCE(host(client_addr), 'local'), COALESCE(application_name, ''),
		backend_start, COALESCE(state, ''), state_change
		FROM pg_stat_activity WHERE datname = $1 AND backend_type = 'client backend' AND pid <> pg_backend_pid()
		ORDER BY backend_start`, db)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.PID, &s.User, &s.ClientAddr, &s.Application, &s.Started, &s.State, &s.StateChange); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TerminateSession ends one session if it belongs to db and to one of roles.
func (c *Client) TerminateSession(ctx context.Context, db string, pid int32, roles []string) (bool, error) {
	p, err := c.Pool()
	if err != nil {
		return false, err
	}
	var ok bool
	err = p.QueryRow(ctx, `SELECT COALESCE((SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE pid = $1 AND datname = $2 AND usename = ANY($3)), false)`, pid, db, roles).Scan(&ok)
	return ok, err
}

// TerminateAll ends every session on db except sqsolve's own.
func (c *Client) TerminateAll(ctx context.Context, db string) (int, error) {
	p, err := c.Pool()
	if err != nil {
		return 0, err
	}
	var n int
	err = p.QueryRow(ctx, `SELECT count(*) FILTER (WHERE pg_terminate_backend(pid))::int FROM pg_stat_activity
		WHERE datname = $1 AND pid <> pg_backend_pid() AND backend_type = 'client backend'`, db).Scan(&n)
	return n, err
}

func (c *Client) exec(ctx context.Context, stmt string) error {
	p, err := c.Pool()
	if err != nil {
		return err
	}
	_, err = p.Exec(ctx, stmt)
	return err
}

// RoleOptions are the attributes of a library role.
type RoleOptions struct {
	Verifier  string // SCRAM verifier
	CreateDB  bool
	Login     bool
	ConnLimit int // -1 unlimited
}

func roleAttrs(o RoleOptions) (string, error) {
	lit, err := Literal(o.Verifier)
	if err != nil {
		return "", err
	}
	attrs := []string{"NOSUPERUSER", "NOCREATEROLE", "NOREPLICATION", "NOBYPASSRLS", "INHERIT"}
	if o.Login {
		attrs = append(attrs, "LOGIN")
	} else {
		attrs = append(attrs, "NOLOGIN")
	}
	if o.CreateDB {
		attrs = append(attrs, "CREATEDB")
	} else {
		attrs = append(attrs, "NOCREATEDB")
	}
	attrs = append(attrs, fmt.Sprintf("CONNECTION LIMIT %d", o.ConnLimit), "PASSWORD "+lit)
	return strings.Join(attrs, " "), nil
}

// CreateLibraryRole creates a role and lets sqsolve_admin act as it.
func (c *Client) CreateLibraryRole(ctx context.Context, name string, o RoleOptions) error {
	q, err := Ident(name)
	if err != nil {
		return err
	}
	attrs, err := roleAttrs(o)
	if err != nil {
		return err
	}
	if err := c.exec(ctx, "CREATE ROLE "+q+" "+attrs); err != nil {
		return fmt.Errorf("SQS-P020 crear rol %s: %w", name, redact(err, o.Verifier))
	}
	if err := c.exec(ctx, "GRANT "+q+" TO "+MustIdent(AdminRole)+" WITH INHERIT TRUE, SET TRUE"); err != nil {
		return fmt.Errorf("SQS-P021 membresía de %s: %w", name, err)
	}
	return nil
}

// SetRolePassword changes the password through a verifier.
func (c *Client) SetRolePassword(ctx context.Context, name, verifier string) error {
	q, err := Ident(name)
	if err != nil {
		return err
	}
	lit, err := Literal(verifier)
	if err != nil {
		return err
	}
	if err := c.exec(ctx, "ALTER ROLE "+q+" PASSWORD "+lit); err != nil {
		return redact(err, lit)
	}
	return nil
}

// SetRoleCreateDB grants or revokes CREATEDB.
func (c *Client) SetRoleCreateDB(ctx context.Context, name string, on bool) error {
	q, err := Ident(name)
	if err != nil {
		return err
	}
	attr := "NOCREATEDB"
	if on {
		attr = "CREATEDB"
	}
	return c.exec(ctx, "ALTER ROLE "+q+" "+attr)
}

// SetRoleLogin enables or disables login.
func (c *Client) SetRoleLogin(ctx context.Context, name string, on bool) error {
	q, err := Ident(name)
	if err != nil {
		return err
	}
	attr := "NOLOGIN"
	if on {
		attr = "LOGIN"
	}
	return c.exec(ctx, "ALTER ROLE "+q+" "+attr)
}

// SetRoleConnLimit sets the connection limit of a role.
func (c *Client) SetRoleConnLimit(ctx context.Context, name string, limit int) error {
	q, err := Ident(name)
	if err != nil {
		return err
	}
	return c.exec(ctx, fmt.Sprintf("ALTER ROLE %s CONNECTION LIMIT %d", q, limit))
}

// SetRoleDefaultRole makes sessions of member act as owner inside db.
func (c *Client) SetRoleDefaultRole(ctx context.Context, member, db, owner string) error {
	qm, err := Ident(member)
	if err != nil {
		return err
	}
	qd, err := Ident(db)
	if err != nil {
		return err
	}
	lit, err := Literal(owner)
	if err != nil {
		return err
	}
	return c.exec(ctx, "ALTER ROLE "+qm+" IN DATABASE "+qd+" SET role = "+lit)
}

// GrantRole makes member a member of role.
func (c *Client) GrantRole(ctx context.Context, role, member string) error {
	qr, err := Ident(role)
	if err != nil {
		return err
	}
	qm, err := Ident(member)
	if err != nil {
		return err
	}
	return c.exec(ctx, "GRANT "+qr+" TO "+qm)
}

// DropRole drops a role if it exists.
func (c *Client) DropRole(ctx context.Context, name string) error {
	q, err := Ident(name)
	if err != nil {
		return err
	}
	return c.exec(ctx, "DROP ROLE IF EXISTS "+q)
}

// CreateDatabase creates an empty UTF8 database owned by owner.
func (c *Client) CreateDatabase(ctx context.Context, name, owner string) error {
	qn, err := Ident(name)
	if err != nil {
		return err
	}
	qo, err := Ident(owner)
	if err != nil {
		return err
	}
	return c.exec(ctx, "CREATE DATABASE "+qn+" OWNER "+qo+" TEMPLATE template0 ENCODING 'UTF8'")
}

// SecureDatabase removes PUBLIC access and grants CONNECT to roles.
func (c *Client) SecureDatabase(ctx context.Context, name string, roles []string) error {
	qn, err := Ident(name)
	if err != nil {
		return err
	}
	if err := c.exec(ctx, "REVOKE ALL ON DATABASE "+qn+" FROM PUBLIC"); err != nil {
		return err
	}
	for _, r := range roles {
		qr, err := Ident(r)
		if err != nil {
			return err
		}
		if err := c.exec(ctx, "GRANT CONNECT, TEMPORARY ON DATABASE "+qn+" TO "+qr); err != nil {
			return err
		}
	}
	return nil
}

// SetAllowConnections toggles datallowconn.
func (c *Client) SetAllowConnections(ctx context.Context, name string, allow bool) error {
	q, err := Ident(name)
	if err != nil {
		return err
	}
	return c.exec(ctx, fmt.Sprintf("ALTER DATABASE %s ALLOW_CONNECTIONS %t", q, allow))
}

// SetDatabaseConnLimit sets datconnlimit.
func (c *Client) SetDatabaseConnLimit(ctx context.Context, name string, limit int) error {
	q, err := Ident(name)
	if err != nil {
		return err
	}
	return c.exec(ctx, fmt.Sprintf("ALTER DATABASE %s CONNECTION LIMIT %d", q, limit))
}

// RenameDatabase renames a database. It must have no sessions.
func (c *Client) RenameDatabase(ctx context.Context, from, to string) error {
	qf, err := Ident(from)
	if err != nil {
		return err
	}
	qt, err := Ident(to)
	if err != nil {
		return err
	}
	return c.exec(ctx, "ALTER DATABASE "+qf+" RENAME TO "+qt)
}

// DropDatabase drops a database, ending its sessions.
func (c *Client) DropDatabase(ctx context.Context, name string) error {
	q, err := Ident(name)
	if err != nil {
		return err
	}
	return c.exec(ctx, "DROP DATABASE IF EXISTS "+q+" WITH (FORCE)")
}

// ReloadConf asks the server to reread its configuration files.
func (c *Client) ReloadConf(ctx context.Context) error {
	p, err := c.Pool()
	if err != nil {
		return err
	}
	var ok bool
	if err := p.QueryRow(ctx, `SELECT pg_reload_conf()`).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errors.New("SQS-P030 pg_reload_conf devolvió false")
	}
	return nil
}

// HBAErrors validates the pg_hba.conf currently on disk.
func (c *Client) HBAErrors(ctx context.Context) ([]string, error) {
	p, err := c.Pool()
	if err != nil {
		return nil, err
	}
	rows, err := p.Query(ctx, `SELECT format('línea %s: %s', line_number, error) FROM pg_hba_file_rules WHERE error IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Fingerprint is the structural hash of a library's public schema.
type Fingerprint struct {
	Hash   string
	Tables int
}

// SchemaFingerprint computes the fingerprint of db (see docs 07 §9).
func SchemaFingerprint(ctx context.Context, conn *pgx.Conn) (Fingerprint, error) {
	rows, err := conn.Query(ctx, `SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY c.relname, a.attnum`)
	if err != nil {
		return Fingerprint{}, err
	}
	defer rows.Close()
	h := sha256.New()
	tables := map[string]bool{}
	for rows.Next() {
		var t, col, typ string
		if err := rows.Scan(&t, &col, &typ); err != nil {
			return Fingerprint{}, err
		}
		tables[t] = true
		fmt.Fprintf(h, "%s|%s|%s\n", t, col, typ)
	}
	if err := rows.Err(); err != nil {
		return Fingerprint{}, err
	}
	if len(tables) == 0 {
		return Fingerprint{Hash: "", Tables: 0}, nil
	}
	return Fingerprint{Hash: "sha256:" + hex.EncodeToString(h.Sum(nil)), Tables: len(tables)}, nil
}

// PublicTables lists the tables of the public schema with their estimated
// row counts.
func PublicTables(ctx context.Context, conn *pgx.Conn) (names []string, estimate int64, err error) {
	rows, err := conn.Query(ctx, `SELECT c.relname, GREATEST(c.reltuples, 0)::bigint FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') ORDER BY 1`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		var e int64
		if err := rows.Scan(&n, &e); err != nil {
			return nil, 0, err
		}
		names = append(names, n)
		estimate += e
	}
	return names, estimate, rows.Err()
}

// CountRows counts rows of each table exactly (in the caller's snapshot) or,
// if exact is false, returns the planner estimates.
func CountRows(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tables []string, exact bool) (map[string]int64, error) {
	out := make(map[string]int64, len(tables))
	for _, t := range tables {
		var n int64
		var err error
		if exact {
			err = q.QueryRow(ctx, "SELECT count(*) FROM public."+pgx.Identifier{t}.Sanitize()).Scan(&n)
		} else {
			err = q.QueryRow(ctx, `SELECT GREATEST(c.reltuples, 0)::bigint FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = 'public' AND c.relname = $1`, t).Scan(&n)
		}
		if err != nil {
			return nil, fmt.Errorf("conteo de %s: %w", t, err)
		}
		out[t] = n
	}
	return out, nil
}

// ParseCIDR accepts an address or a prefix and returns it in pg_hba form.
func ParseCIDR(s string) (string, error) {
	s = strings.TrimSpace(s)
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked().String(), nil
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return netip.PrefixFrom(a, a.BitLen()).String(), nil
	}
	return "", fmt.Errorf("SQS-L020 red inválida: %q", s)
}
