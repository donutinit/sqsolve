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
	Name           string
	Owner          string
	AllowConn      bool
	ConnLimit      int
	SizeBytes      int64
	CollationStale bool
	ActiveSessions int
	// Invalid marks a database that a DROP DATABASE in progress or
	// interrupted left behind (datconnlimit -2): it cannot be used, only
	// dropped, and its size is not measured (reported as 0).
	Invalid bool
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

// dbSelect reads DBInfo. For an invalid database (datconnlimit -2, being
// dropped or left behind by an interrupted DROP) the size and collation
// functions are not called: they fail once the drop commits. The size is 0
// when a database has no files (pg_database_size() returns NULL then).
const dbSelect = `SELECT d.datname, r.rolname, d.datallowconn, d.datconnlimit,
	CASE WHEN d.datconnlimit = -2 THEN 0 ELSE COALESCE(pg_database_size(d.oid), 0) END,
	CASE WHEN d.datconnlimit = -2 THEN false ELSE COALESCE(d.datcollversion <> pg_database_collation_actual_version(d.oid), false) END,
	(SELECT count(*) FROM pg_stat_activity a WHERE a.datid = d.oid AND a.backend_type = 'client backend')::int,
	d.datconnlimit = -2
	FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba
	WHERE NOT d.datistemplate`

func scanDB(row pgx.Row) (DBInfo, error) {
	var d DBInfo
	err := row.Scan(&d.Name, &d.Owner, &d.AllowConn, &d.ConnLimit, &d.SizeBytes, &d.CollationStale, &d.ActiveSessions, &d.Invalid)
	return d, err
}

// Databases lists every database except templates (docs 07 §10), including
// invalid ones marked Invalid, so reconciliation can drop invalid auxiliary
// databases and the panel can show the others. Callers that need the
// databases a library can use call Database.
func (c *Client) Databases(ctx context.Context) ([]DBInfo, error) {
	p, err := c.Pool()
	if err != nil {
		return nil, err
	}
	rows, err := p.Query(ctx, dbSelect+` ORDER BY d.datname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DBInfo
	for rows.Next() {
		d, err := scanDB(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Database returns one usable database, or nil when it does not exist, is a
// template or is invalid: for the state of a library an invalid database is
// as good as missing. It reads a single row, so only that database's size is
// computed. An error never means "does not exist".
func (c *Client) Database(ctx context.Context, name string) (*DBInfo, error) {
	p, err := c.Pool()
	if err != nil {
		return nil, err
	}
	d, err := scanDB(p.QueryRow(ctx, dbSelect+` AND d.datconnlimit <> -2 AND d.datname = $1`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// DatabaseNameTaken reports whether any database, in any state, has that
// name: an invalid or template database still holds it until it is dropped.
func (c *Client) DatabaseNameTaken(ctx context.Context, name string) (bool, error) {
	p, err := c.Pool()
	if err != nil {
		return false, err
	}
	var ok bool
	err = p.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&ok)
	return ok, err
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
	// SSL and TLSVersion come from pg_stat_ssl: whether the connection is
	// encrypted and with which protocol ("" when not).
	SSL        bool
	TLSVersion string
}

// Sessions lists client sessions on db.
func (c *Client) Sessions(ctx context.Context, db string) ([]Session, error) {
	p, err := c.Pool()
	if err != nil {
		return nil, err
	}
	rows, err := p.Query(ctx, `SELECT a.pid, COALESCE(a.usename, ''), COALESCE(host(a.client_addr), 'local'), COALESCE(a.application_name, ''),
		a.backend_start, COALESCE(a.state, ''), a.state_change, COALESCE(s.ssl, false), COALESCE(s.version, '')
		FROM pg_stat_activity a LEFT JOIN pg_stat_ssl s USING (pid)
		WHERE a.datname = $1 AND a.backend_type = 'client backend' AND a.pid <> pg_backend_pid()
		ORDER BY a.backend_start`, db)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.PID, &s.User, &s.ClientAddr, &s.Application, &s.Started, &s.State, &s.StateChange, &s.SSL, &s.TLSVersion); err != nil {
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

// TerminateResult counts the sessions TerminateAllSessions ended and those it
// left alone because they belong to a superuser.
type TerminateResult struct {
	Terminated int
	Superuser  int
}

// TerminateAllSessions ends every session on db except sqsolve's own and
// those of superusers: PostgreSQL refuses to let sqsolve_admin end them, and
// that refusal used to abort the whole query. Callers can report Superuser.
// A session whose role is not known yet counts as a superuser one.
func (c *Client) TerminateAllSessions(ctx context.Context, db string) (TerminateResult, error) {
	p, err := c.Pool()
	if err != nil {
		return TerminateResult{}, err
	}
	var r TerminateResult
	err = p.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE CASE WHEN r.rolsuper IS FALSE THEN pg_terminate_backend(a.pid) ELSE false END)::int,
		count(*) FILTER (WHERE r.rolsuper IS NOT FALSE)::int
		FROM pg_stat_activity a LEFT JOIN pg_roles r ON r.oid = a.usesysid
		WHERE a.datname = $1 AND a.pid <> pg_backend_pid() AND a.backend_type = 'client backend'`, db).Scan(&r.Terminated, &r.Superuser)
	return r, err
}

// TerminateAll is TerminateAllSessions for callers that only need how many
// sessions were ended.
func (c *Client) TerminateAll(ctx context.Context, db string) (int, error) {
	r, err := c.TerminateAllSessions(ctx, db)
	return r.Terminated, err
}

func (c *Client) exec(ctx context.Context, stmt string) error {
	p, err := c.Pool()
	if err != nil {
		return err
	}
	_, err = p.Exec(ctx, stmt)
	return WrapTimeout(err)
}

// execTx runs stmts in one transaction, so either all of them take effect or
// none does. On failure it returns the index of the statement that failed, or
// -1 when the failure is not one of them (no pool, BEGIN or COMMIT).
func (c *Client) execTx(ctx context.Context, stmts ...string) (int, error) {
	p, err := c.Pool()
	if err != nil {
		return -1, err
	}
	failed := -1
	err = pgx.BeginFunc(ctx, p, func(tx pgx.Tx) error {
		for i, s := range stmts {
			if _, err := tx.Exec(ctx, s); err != nil {
				failed = i
				return err
			}
		}
		return nil
	})
	return failed, WrapTimeout(err)
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

// CreateLibraryRole creates a role and lets sqsolve_admin act as it. Both
// happen in one transaction: a failed membership does not leave a role behind
// that would block the next attempt (SQS-L006, SQS-L033).
func (c *Client) CreateLibraryRole(ctx context.Context, name string, o RoleOptions) error {
	q, err := Ident(name)
	if err != nil {
		return err
	}
	attrs, err := roleAttrs(o)
	if err != nil {
		return err
	}
	i, err := c.execTx(ctx, "CREATE ROLE "+q+" "+attrs, "GRANT "+q+" TO "+MustIdent(AdminRole)+" WITH INHERIT TRUE, SET TRUE")
	switch {
	case err == nil:
		return nil
	case i == 0:
		return fmt.Errorf("SQS-P020 creating role %s: %w", name, redact(err, o.Verifier))
	case i == 1:
		return fmt.Errorf("SQS-P021 membership of %s: %w", name, err)
	}
	return err
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

// SetRolesLogin enables or disables login for those of names that exist and
// are not in that state yet, and returns the roles it changed. Roles that do
// not exist are skipped.
func (c *Client) SetRolesLogin(ctx context.Context, names []string, on bool) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	p, err := c.Pool()
	if err != nil {
		return nil, err
	}
	rows, err := p.Query(ctx, `SELECT rolname FROM pg_roles WHERE rolname = ANY($1) AND rolcanlogin <> $2 ORDER BY 1`, names, on)
	if err != nil {
		return nil, err
	}
	todo, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, r := range todo {
		if err := c.SetRoleLogin(ctx, r, on); err != nil {
			return changed, err
		}
		changed = append(changed, r)
	}
	return changed, nil
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
		return errors.New("SQS-P030 pg_reload_conf returned false")
	}
	return nil
}

// HBAErrors validates the pg_hba.conf currently on disk.
func (c *Client) HBAErrors(ctx context.Context) ([]string, error) {
	p, err := c.Pool()
	if err != nil {
		return nil, err
	}
	rows, err := p.Query(ctx, `SELECT format('line %s: %s', line_number, error) FROM pg_hba_file_rules WHERE error IS NOT NULL`)
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
			// The connection's lock_timeout bounds a count behind a lock.
			return nil, fmt.Errorf("count of %s: %w", t, WrapTimeout(err))
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
	return "", fmt.Errorf("SQS-L020 invalid network: %q", s)
}

// ActivityMark summarizes the writes a database has received since its
// statistics were last reset: rows inserted, updated and deleted, plus the
// reset time so a reset never reads as "unchanged". It only reads
// pg_stat_database (docs 07 §7); two equal marks mean no row changed in
// between. Empty when the database is unknown.
func (c *Client) ActivityMark(ctx context.Context, db string) (string, error) {
	marks, err := c.ActivityMarks(ctx)
	if err != nil {
		return "", err
	}
	return marks[db], nil
}

// ActivityMarks returns the activity mark of every database.
func (c *Client) ActivityMarks(ctx context.Context) (map[string]string, error) {
	p, err := c.Pool()
	if err != nil {
		return nil, err
	}
	rows, err := p.Query(ctx, `SELECT datname, tup_inserted + tup_updated + tup_deleted, COALESCE(stats_reset::text, '')
		FROM pg_stat_database WHERE datname IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, reset string
		var tup int64
		if err := rows.Scan(&name, &tup, &reset); err != nil {
			return nil, err
		}
		out[name] = activityMark(tup, reset)
	}
	return out, rows.Err()
}

// ActivityMarkFrom reads the mark of the current database on an open
// connection or transaction, so a backup records the mark of its own snapshot.
func ActivityMarkFrom(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}) (string, error) {
	var reset string
	var tup int64
	err := q.QueryRow(ctx, `SELECT tup_inserted + tup_updated + tup_deleted, COALESCE(stats_reset::text, '')
		FROM pg_stat_database WHERE datname = current_database()`).Scan(&tup, &reset)
	if err != nil {
		return "", err
	}
	return activityMark(tup, reset), nil
}

func activityMark(tup int64, reset string) string { return fmt.Sprintf("%d@%s", tup, reset) }
