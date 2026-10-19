// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pg is sqsolve's control client for PostgreSQL. It connects only
// through the local Unix socket, as sqsolve_admin for daily work and as the
// superuser for the few audited operations that need it.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// AdminRole is the control role.
const AdminRole = "sqsolve_admin"

// Superuser is the role created by the postgres image.
const Superuser = "postgres"

// ErrUnavailable means PostgreSQL cannot be reached right now.
var ErrUnavailable = errors.New("SQS-P001 PostgreSQL no está disponible")

// Client manages connections to the server.
type Client struct {
	SocketDir string
	Port      int
	adminPass secrets.String
	superPass secrets.String

	mu   sync.RWMutex
	pool *pgxpool.Pool
}

// New creates a client. Call Bootstrap and Open before use.
func New(socketDir string, port int, adminPass, superPass secrets.String) *Client {
	return &Client{SocketDir: socketDir, Port: port, adminPass: adminPass, superPass: superPass}
}

func (c *Client) connConfig(user string, pass secrets.String, db string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig("host=" + c.SocketDir + " port=" + strconv.Itoa(c.Port) + " sslmode=disable")
	if err != nil {
		return nil, err
	}
	cfg.User = user
	cfg.Password = pass.Reveal()
	cfg.Database = db
	cfg.ConnectTimeout = 10 * time.Second
	cfg.RuntimeParams["application_name"] = "sqsolve"
	return cfg, nil
}

// Bootstrap creates or repairs sqsolve_admin using the superuser. It is
// idempotent and safe to run on every start.
func (c *Client) Bootstrap(ctx context.Context) error {
	conn, err := c.Superuser(ctx, "postgres")
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	verifier, err := ScramVerifier(c.adminPass)
	if err != nil {
		return err
	}
	lit, err := Literal(verifier)
	if err != nil {
		return err
	}
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, AdminRole).Scan(&exists); err != nil {
		return err
	}
	role := MustIdent(AdminRole)
	attrs := "LOGIN NOSUPERUSER CREATEDB CREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD " + lit
	stmt := "CREATE ROLE " + role + " " + attrs
	if exists {
		stmt = "ALTER ROLE " + role + " " + attrs
	}
	stmts := []string{
		stmt,
		"GRANT pg_monitor, pg_signal_backend, pg_read_all_data TO " + role,
		"GRANT EXECUTE ON FUNCTION pg_catalog.pg_reload_conf() TO " + role,
		"GRANT EXECUTE ON FUNCTION pg_catalog.pg_hba_file_rules() TO " + role,
		"GRANT SELECT ON pg_catalog.pg_hba_file_rules TO " + role,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("SQS-P002 preparando %s: %w", AdminRole, redact(err, lit))
		}
	}
	return nil
}

func redact(err error, secret string) error {
	if err == nil {
		return nil
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "[redactado]"))
}

// Open creates the admin pool.
func (c *Client) Open(ctx context.Context) error {
	cc, err := c.connConfig(AdminRole, c.adminPass, "postgres")
	if err != nil {
		return err
	}
	pcfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return err
	}
	pcfg.ConnConfig = cc
	pcfg.MaxConns = 6
	pcfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	c.mu.Lock()
	old := c.pool
	c.pool = pool
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return nil
}

// Close closes the pool.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pool != nil {
		c.pool.Close()
		c.pool = nil
	}
}

// Pool returns the admin pool or ErrUnavailable.
func (c *Client) Pool() (*pgxpool.Pool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.pool == nil {
		return nil, ErrUnavailable
	}
	return c.pool, nil
}

// Ping checks the server.
func (c *Client) Ping(ctx context.Context) error {
	p, err := c.Pool()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := p.Ping(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

// ConnectDB opens a dedicated connection to db as sqsolve_admin.
func (c *Client) ConnectDB(ctx context.Context, db string) (*pgx.Conn, error) {
	if err := ValidateIdent(db); err != nil && db != "postgres" {
		return nil, err
	}
	cc, err := c.connConfig(AdminRole, c.adminPass, db)
	if err != nil {
		return nil, err
	}
	return pgx.ConnectConfig(ctx, cc)
}

// Superuser opens a connection as the superuser. Callers must audit the use.
func (c *Client) Superuser(ctx context.Context, db string) (*pgx.Conn, error) {
	cc, err := c.connConfig(Superuser, c.superPass, db)
	if err != nil {
		return nil, err
	}
	conn, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return conn, nil
}

// AdminCredential is used by pg_dump and pg_restore.
func (c *Client) AdminCredential() *execx.Credential {
	return &execx.Credential{User: AdminRole, Password: c.adminPass}
}

// ConnArgs are the libpq connection arguments for external tools.
func (c *Client) ConnArgs(db string) []string {
	return []string{"--host=" + c.SocketDir, "--port=" + strconv.Itoa(c.Port), "--username=" + AdminRole, "--no-password", "--dbname=" + db}
}

// IsCode reports whether err is a PostgreSQL error with the given SQLSTATE.
func IsCode(err error, code string) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == code
}
