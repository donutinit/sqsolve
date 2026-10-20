// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/scheduler"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// State of a library (docs 07 §4).
type State string

// States.
const (
	StateProvisioning State = "provisioning"
	StateAwaitingInit State = "awaiting_init"
	StateInitExpired  State = "init_expired"
	StateActive       State = "active"
	StateMaintenance  State = "maintenance"
	StateRestoring    State = "restoring"
	StateTrashed      State = "trashed"
	StatePurged       State = "purged"
	StateMissing      State = "missing"
)

// Label returns the display label of a state.
func (s State) Label() string {
	switch s {
	case StateProvisioning:
		return "Preparing"
	case StateAwaitingInit:
		return "Waiting for Resolve"
	case StateInitExpired:
		return "Initialization expired"
	case StateActive:
		return "Active"
	case StateMaintenance:
		return "In maintenance"
	case StateRestoring:
		return "Restoring"
	case StateTrashed:
		return "In the trash"
	case StatePurged:
		return "Purged"
	case StateMissing:
		return "No database on the server"
	}
	return string(s)
}

// Tone is the status color family for the UI.
func (s State) Tone() string {
	switch s {
	case StateActive:
		return "ok"
	case StateAwaitingInit, StateProvisioning, StateRestoring:
		return "run"
	case StateMaintenance, StateInitExpired:
		return "warn"
	case StateTrashed, StatePurged:
		return "off"
	case StateMissing:
		return "bad"
	}
	return "off"
}

// Strategies of creation.
const (
	StrategyResolve   = "resolve"   // E1: Resolve creates the database during a window
	StrategyPrecreate = "precreate" // E0: sqsolve creates an empty database (experimental)
	StrategyRestore   = "restore"   // created from a backup
)

// Library is a managed Resolve project library.
type Library struct {
	ID                   string
	Slug                 string
	DisplayName          string
	State                State
	DBName               string
	OwnerRole            string
	Strategy             string
	InitWindowExpiresAt  *time.Time
	InitTableCount       int
	ResolveVersion       string
	Fingerprint          string
	TableCount           int
	FingerprintCheckedAt *time.Time
	ConnLimit            int
	Notes                string
	TrashDB              string
	PrevDB               string
	PrevExpiresAt        *time.Time
	PrevBackupID         string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	TrashedAt            *time.Time
	PurgeAfter           *time.Time
	PurgedAt             *time.Time
}

// Live reports whether the library has a live database that Resolve uses.
func (l *Library) Live() bool {
	return l.State == StateActive || l.State == StateMaintenance || l.State == StateRestoring
}

const libCols = `id, slug, display_name, state, db_name, owner_role, creation_strategy, init_window_expires_at, init_table_count,
	resolve_version, schema_fingerprint, schema_table_count, fingerprint_checked_at, connection_limit, notes, trash_db_name,
	prev_db_name, prev_expires_at, prev_backup_id, created_at, updated_at, trashed_at, purge_after, purged_at`

func scanLibrary(row interface{ Scan(...any) error }) (*Library, error) {
	var l Library
	var state, created, updated string
	var initExp, fpAt, prevExp, trashed, purgeAfter, purged sql.NullString
	err := row.Scan(&l.ID, &l.Slug, &l.DisplayName, &state, &l.DBName, &l.OwnerRole, &l.Strategy, &initExp, &l.InitTableCount,
		&l.ResolveVersion, &l.Fingerprint, &l.TableCount, &fpAt, &l.ConnLimit, &l.Notes, &l.TrashDB,
		&l.PrevDB, &prevExp, &l.PrevBackupID, &created, &updated, &trashed, &purgeAfter, &purged)
	if err != nil {
		return nil, err
	}
	l.State = State(state)
	l.InitWindowExpiresAt = store.NullT(initExp)
	l.FingerprintCheckedAt = store.NullT(fpAt)
	l.PrevExpiresAt = store.NullT(prevExp)
	l.CreatedAt = store.ParseT(created)
	l.UpdatedAt = store.ParseT(updated)
	l.TrashedAt = store.NullT(trashed)
	l.PurgeAfter = store.NullT(purgeAfter)
	l.PurgedAt = store.NullT(purged)
	return &l, nil
}

// ErrLibraryNotFound is returned for unknown libraries.
var ErrLibraryNotFound = errors.New("SQS-L002 library not found")

// Library returns one library by id.
func (s *Service) Library(ctx context.Context, id string) (*Library, error) {
	l, err := scanLibrary(s.DB.QueryRowContext(ctx, `SELECT `+libCols+` FROM libraries WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLibraryNotFound
	}
	return l, err
}

// LibraryBySlug returns the non-purged library with that slug.
func (s *Service) LibraryBySlug(ctx context.Context, slug string) (*Library, error) {
	l, err := scanLibrary(s.DB.QueryRowContext(ctx, `SELECT `+libCols+` FROM libraries WHERE slug = ? AND state <> 'purged'`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLibraryNotFound
	}
	return l, err
}

// ListOptions filter libraries.
type ListOptions struct {
	States         []State
	IncludePurged  bool
	IncludeTrashed bool
}

// Libraries lists libraries. By default it hides purged ones.
func (s *Service) Libraries(ctx context.Context, o ListOptions) ([]*Library, error) {
	q := `SELECT ` + libCols + ` FROM libraries WHERE 1=1`
	var args []any
	if len(o.States) > 0 {
		q += ` AND state IN (?` + strings.Repeat(",?", len(o.States)-1) + `)`
		for _, st := range o.States {
			args = append(args, string(st))
		}
	} else if !o.IncludePurged {
		q += ` AND state <> 'purged'`
	}
	q += ` ORDER BY display_name COLLATE NOCASE, slug`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Library
	for rows.Next() {
		l, err := scanLibrary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Service) setState(ctx context.Context, id string, st State) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE libraries SET state = ?, updated_at = ? WHERE id = ?`, string(st), store.NowT(), id)
	return err
}

// ValidateDisplayName checks the visible name.
func ValidateDisplayName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || utf8.RuneCountInString(n) > 80 {
		return "", errors.New("SQS-L003 the display name must be 1 to 80 characters long")
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("SQS-L003 the display name cannot contain control characters")
		}
	}
	return n, nil
}

// SuggestSlug turns a display name into a valid slug.
func SuggestSlug(name string) string {
	repl := strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n", "Á", "a", "É", "e", "Í", "i", "Ó", "o", "Ú", "u", "Ñ", "n")
	name = strings.ToLower(repl.Replace(name))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" || out[0] < 'a' || out[0] > 'z' {
		out = "biblioteca_" + out
	}
	if len(out) > 40 {
		out = strings.TrimRight(out[:40], "_")
	}
	for len(out) < 3 {
		out += "_x"
	}
	for _, p := range []string{"pg_", "sqsolve", "template", "postgres"} {
		if strings.HasPrefix(out, p) {
			out = "lib_" + out
			if len(out) > 40 {
				out = out[:40]
			}
		}
	}
	return strings.TrimRight(out, "_")
}

// CreateInput describes a new library.
type CreateInput struct {
	DisplayName string
	Slug        string
	Strategy    string
	Notes       string
}

// SlugAvailable checks the catalog and the server.
func (s *Service) SlugAvailable(ctx context.Context, slug string) error {
	if err := pg.ValidateSlug(slug); err != nil {
		return err
	}
	if _, err := s.LibraryBySlug(ctx, slug); err == nil {
		return fmt.Errorf("SQS-L004 a library with identifier %q already exists", slug)
	}
	if db, err := s.PG.Database(ctx, slug); err != nil {
		return err
	} else if db != nil {
		return fmt.Errorf("SQS-L005 a database %q already exists on the server (you can adopt it from Maintenance)", slug)
	}
	if ok, err := s.PG.RoleExists(ctx, slug); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("SQS-L006 a role %q already exists on the server", slug)
	}
	return nil
}

// CreateLibrary registers a library and enqueues its creation.
func (s *Service) CreateLibrary(ctx context.Context, by audit.Actor, in CreateInput) (*Library, *jobs.Job, error) {
	name, err := ValidateDisplayName(in.DisplayName)
	if err != nil {
		return nil, nil, err
	}
	if in.Strategy == "" {
		in.Strategy = StrategyResolve
	}
	if in.Strategy != StrategyResolve && in.Strategy != StrategyPrecreate {
		return nil, nil, errors.New("SQS-L007 unknown creation strategy")
	}
	if err := s.SlugAvailable(ctx, in.Slug); err != nil {
		return nil, nil, err
	}
	id := ulid.New()
	now := store.NowT()
	_, err = s.DB.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, notes, created_at, updated_at)
		VALUES (?, ?, ?, 'provisioning', ?, ?, ?, ?, ?, ?)`, id, in.Slug, name, in.Slug, in.Slug, in.Strategy, strings.TrimSpace(in.Notes), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, nil, fmt.Errorf("SQS-L004 a library with identifier %q already exists", in.Slug)
		}
		return nil, nil, err
	}
	s.Audit.Record(ctx, by, "library.create", "library", id, audit.OK, map[string]any{"slug": in.Slug, "name": name, "strategy": in.Strategy})
	j, err := s.Jobs.Enqueue(ctx, JobLibraryCreate, id, map[string]string{"library_id": id}, by)
	if err != nil {
		return nil, nil, err
	}
	l, err := s.Library(ctx, id)
	return l, j, err
}

func (s *Service) runLibraryCreate(ctx context.Context, r *jobs.Run) error {
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	if l.State != StateProvisioning {
		return fmt.Errorf("SQS-L010 the library is not being prepared (%s)", l.State.Label())
	}
	createdRole := false
	rollback := func(cause error) error {
		c := context.WithoutCancel(ctx)
		if createdRole {
			s.PG.DropDatabase(c, l.DBName)
			s.PG.DropRole(c, l.OwnerRole)
		}
		s.DB.ExecContext(c, `UPDATE library_credentials SET revoked_at = ? WHERE library_id = ?`, store.NowT(), l.ID)
		s.DB.ExecContext(c, `UPDATE libraries SET state = 'purged', purged_at = ?, notes = notes || ? WHERE id = ?`, store.NowT(), "\n[creación fallida] "+cause.Error(), l.ID)
		s.SyncHBA(c)
		return cause
	}
	err = r.Step("validate", func() error {
		if db, err := s.PG.Database(ctx, l.DBName); err != nil {
			return err
		} else if db != nil {
			return fmt.Errorf("SQS-L005 a database %q already exists on the server", l.DBName)
		}
		if ok, err := s.PG.RoleExists(ctx, l.OwnerRole); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("SQS-L006 a role %q already exists on the server", l.OwnerRole)
		}
		return nil
	})
	if err != nil {
		return rollback(err)
	}
	r.Progress(20)
	err = r.Step("create_role", func() error {
		pass := secrets.GeneratePassword(24)
		v, err := pg.ScramVerifier(pass)
		if err != nil {
			return err
		}
		if err := s.PG.CreateLibraryRole(ctx, l.OwnerRole, pg.RoleOptions{Verifier: v, Login: true, CreateDB: l.Strategy == StrategyResolve, ConnLimit: -1}); err != nil {
			return err
		}
		createdRole = true
		_, err = s.addCredential(ctx, l.ID, "owner", l.OwnerRole, "", pass)
		r.Infof("Role %s created", l.OwnerRole)
		return err
	})
	if err != nil {
		return rollback(err)
	}
	r.Progress(50)
	if l.Strategy == StrategyPrecreate {
		err = r.Step("create_database", func() error {
			if err := s.PG.CreateDatabase(ctx, l.DBName, l.OwnerRole); err != nil {
				return err
			}
			if err := s.PG.SecureDatabase(ctx, l.DBName, []string{l.OwnerRole}); err != nil {
				return err
			}
			s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'active', updated_at = ? WHERE id = ?`, store.NowT(), l.ID)
			return s.ensurePolicy(ctx, l.ID)
		})
		if err != nil {
			return rollback(err)
		}
	} else {
		err = r.Step("open_window", func() error {
			mins := s.Settings(ctx).InitWindowMinutes
			exp := s.now().Add(time.Duration(mins) * time.Minute)
			_, err := s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'awaiting_init', init_window_expires_at = ?, init_table_count = -1, updated_at = ? WHERE id = ?`,
				store.T(exp), store.NowT(), l.ID)
			r.Infof("Initialization window open until %s", exp.In(s.Cfg.Location).Format("15:04"))
			return err
		})
		if err != nil {
			return rollback(err)
		}
	}
	r.Progress(80)
	if err := r.Step("network_access", func() error { return s.SyncHBA(ctx) }); err != nil {
		return rollback(err)
	}
	s.Events.Emit(ctx, events.Event{Type: events.LibraryCreated, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Created library %q", l.DisplayName)})
	r.SetResult(map[string]string{"library_id": l.ID})
	return nil
}

// ReopenInitWindow lets Resolve initialize the library again.
func (s *Service) ReopenInitWindow(ctx context.Context, by audit.Actor, id string) error {
	l, err := s.Library(ctx, id)
	if err != nil {
		return err
	}
	if l.State != StateInitExpired && l.State != StateAwaitingInit {
		return errors.New("SQS-L011 the library is not waiting for initialization")
	}
	if err := s.PG.SetRoleCreateDB(ctx, l.OwnerRole, true); err != nil {
		return err
	}
	exp := s.now().Add(time.Duration(s.Settings(ctx).InitWindowMinutes) * time.Minute)
	s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'awaiting_init', init_window_expires_at = ?, init_table_count = -1, updated_at = ? WHERE id = ?`, store.T(exp), store.NowT(), id)
	s.Audit.Record(ctx, by, "library.init_window", "library", id, audit.OK, nil)
	return s.SyncHBA(ctx)
}

// WatchInit checks libraries waiting for Resolve to create their database.
func (s *Service) WatchInit(ctx context.Context) {
	libs, err := s.Libraries(ctx, ListOptions{States: []State{StateAwaitingInit}})
	if err != nil {
		return
	}
	for _, l := range libs {
		if err := s.checkInit(ctx, l); err != nil {
			s.Log.Warn("watching initialization", "library", l.Slug, "err", err)
		}
	}
}

func (s *Service) checkInit(ctx context.Context, l *Library) error {
	if l.InitWindowExpiresAt != nil && s.now().After(*l.InitWindowExpiresAt) {
		if err := s.PG.SetRoleCreateDB(ctx, l.OwnerRole, false); err != nil {
			return err
		}
		s.setState(ctx, l.ID, StateInitExpired)
		s.Events.Emit(ctx, events.Event{Type: events.LibraryInitExpired, Severity: events.Warning, LibraryID: l.ID,
			Message: fmt.Sprintf("The window to initialize %q from Resolve expired; you can reopen it", l.DisplayName)})
		return nil
	}
	db, err := s.PG.Database(ctx, l.DBName)
	if err != nil {
		return err
	}
	if db == nil {
		s.warnMisnamed(ctx, l)
		return nil
	}
	if db.Owner != l.OwnerRole {
		return fmt.Errorf("database %s is owned by %s, not %s", l.DBName, db.Owner, l.OwnerRole)
	}
	conn, err := s.PG.ConnectDB(ctx, l.DBName)
	if err != nil {
		return err
	}
	fp, err := pg.SchemaFingerprint(ctx, conn)
	conn.Close(context.Background())
	if err != nil || fp.Tables == 0 {
		return err
	}
	// Wait until the table count is stable for three checks in a row.
	s.stableMu.Lock()
	if s.stable == nil {
		s.stable = map[string]int{}
	}
	if fp.Tables != l.InitTableCount {
		s.stable[l.ID] = 0
		s.stableMu.Unlock()
		_, err := s.DB.ExecContext(ctx, `UPDATE libraries SET init_table_count = ? WHERE id = ?`, fp.Tables, l.ID)
		return err
	}
	s.stable[l.ID]++
	n := s.stable[l.ID]
	s.stableMu.Unlock()
	if n < 2 {
		return nil
	}
	return s.activate(ctx, l, fp)
}

// MisnamedDatabases lists databases the library role created with a name
// other than the expected one, which happens when the name typed in Resolve
// is not exactly the identifier.
func (s *Service) MisnamedDatabases(ctx context.Context, l *Library) []string {
	owned, err := s.PG.DatabasesOwnedBy(ctx, l.OwnerRole)
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range owned {
		if d != l.DBName && !pg.IsAuxiliary(d) {
			out = append(out, d)
		}
	}
	return out
}

func (s *Service) warnMisnamed(ctx context.Context, l *Library) {
	for _, d := range s.MisnamedDatabases(ctx, l) {
		key := l.ID + "/" + d
		s.stableMu.Lock()
		if s.warned == nil {
			s.warned = map[string]bool{}
		}
		seen := s.warned[key]
		s.warned[key] = true
		s.stableMu.Unlock()
		if seen {
			continue
		}
		s.Events.Emit(ctx, events.Event{Type: events.LibraryExtraDatabase, Severity: events.Warning, LibraryID: l.ID,
			Message: fmt.Sprintf("Resolve created the database %q, but the library must be named exactly %q. Create it again in Resolve with that name", d, l.DBName)})
	}
}

func (s *Service) activate(ctx context.Context, l *Library, fp pg.Fingerprint) error {
	if err := s.PG.SetRoleCreateDB(ctx, l.OwnerRole, false); err != nil {
		return err
	}
	roles, err := s.activeRoles(ctx, l.ID)
	if err != nil {
		return err
	}
	if err := s.PG.SecureDatabase(ctx, l.DBName, roles); err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'active', schema_fingerprint = ?, schema_table_count = ?, fingerprint_checked_at = ?,
		init_window_expires_at = NULL, updated_at = ? WHERE id = ?`, fp.Hash, fp.Tables, store.NowT(), store.NowT(), l.ID)
	if err != nil {
		return err
	}
	if extra, err := s.PG.DatabasesOwnedBy(ctx, l.OwnerRole); err == nil {
		for _, d := range extra {
			if d != l.DBName {
				s.Events.Emit(ctx, events.Event{Type: events.LibraryExtraDatabase, Severity: events.Warning, LibraryID: l.ID,
					Message: fmt.Sprintf("Role %s created another database during the window: %s. Check it on the server", l.OwnerRole, d)})
			}
		}
	}
	if err := s.ensurePolicy(ctx, l.ID); err != nil {
		return err
	}
	s.stableMu.Lock()
	delete(s.stable, l.ID)
	s.stableMu.Unlock()
	s.Events.Emit(ctx, events.Event{Type: events.LibraryActivated, LibraryID: l.ID,
		Message: fmt.Sprintf("Resolve initialized %q; the library is active", l.DisplayName), Data: map[string]any{"tables": fp.Tables}})
	_, err = s.Jobs.Enqueue(ctx, JobBackupCreate, l.ID, BackupParams{Kind: KindManual}, audit.System, jobs.Unique())
	return err
}

// UpdateInput changes editable properties.
type UpdateInput struct {
	DisplayName    string
	Notes          string
	ResolveVersion string
	ConnLimit      int
}

// UpdateLibrary edits a library.
func (s *Service) UpdateLibrary(ctx context.Context, by audit.Actor, id string, in UpdateInput) error {
	l, err := s.Library(ctx, id)
	if err != nil {
		return err
	}
	name, err := ValidateDisplayName(in.DisplayName)
	if err != nil {
		return err
	}
	if in.ConnLimit < -1 || in.ConnLimit > 1000 {
		return errors.New("SQS-L012 invalid connection limit (-1 for unlimited, up to 1000)")
	}
	rv := strings.TrimSpace(in.ResolveVersion)
	if len(rv) > 20 {
		return errors.New("SQS-L013 Resolve version too long")
	}
	if len(in.Notes) > 10000 {
		return errors.New("SQS-L014 notes too long")
	}
	if in.ConnLimit != l.ConnLimit && l.Live() {
		if err := s.PG.SetDatabaseConnLimit(ctx, l.DBName, in.ConnLimit); err != nil {
			return err
		}
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE libraries SET display_name = ?, notes = ?, resolve_version = ?, connection_limit = ?, updated_at = ? WHERE id = ?`,
		name, in.Notes, rv, in.ConnLimit, store.NowT(), id)
	if err != nil {
		return err
	}
	return s.Audit.Record(ctx, by, "library.update", "library", id, audit.OK, map[string]any{"name": name, "resolve_version": rv, "connection_limit": in.ConnLimit})
}

// SetMaintenance blocks or unblocks connections.
func (s *Service) SetMaintenance(ctx context.Context, by audit.Actor, id string, on, terminate bool) (int, error) {
	l, err := s.Library(ctx, id)
	if err != nil {
		return 0, err
	}
	if on && l.State != StateActive || !on && l.State != StateMaintenance {
		return 0, fmt.Errorf("SQS-L015 cannot change maintenance in state %s", l.State.Label())
	}
	if err := s.PG.SetAllowConnections(ctx, l.DBName, !on); err != nil {
		return 0, err
	}
	n := 0
	if on && terminate {
		n, _ = s.PG.TerminateAll(ctx, l.DBName)
	}
	st, typ, msg := StateActive, events.LibraryMaintenanceOff, "Connections reopened for %q"
	if on {
		st, typ, msg = StateMaintenance, events.LibraryMaintenanceOn, "%q is in maintenance: it does not accept new connections"
	}
	s.setState(ctx, id, st)
	s.Audit.Record(ctx, by, "library.maintenance", "library", id, audit.OK, map[string]any{"on": on, "terminated": n})
	s.Events.Emit(ctx, events.Event{Type: typ, LibraryID: id, Message: fmt.Sprintf(msg, l.DisplayName)})
	return n, nil
}

// Sessions lists sessions of a library.
func (s *Service) Sessions(ctx context.Context, id string) ([]pg.Session, error) {
	l, err := s.Library(ctx, id)
	if err != nil {
		return nil, err
	}
	if !l.Live() && l.State != StateAwaitingInit {
		return nil, nil
	}
	return s.PG.Sessions(ctx, l.DBName)
}

// TerminateSession ends one session of the library.
func (s *Service) TerminateSession(ctx context.Context, by audit.Actor, id string, pid int32) error {
	l, err := s.Library(ctx, id)
	if err != nil {
		return err
	}
	roles, err := s.activeRoles(ctx, id)
	if err != nil {
		return err
	}
	ok, err := s.PG.TerminateSession(ctx, l.DBName, pid, roles)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("SQS-L016 the session no longer exists or does not belong to this library")
	}
	return s.Audit.Record(ctx, by, "library.session_terminate", "library", id, audit.OK, map[string]any{"pid": pid})
}

// FingerprintAll refreshes the schema fingerprint of live libraries.
func (s *Service) FingerprintAll(ctx context.Context) {
	libs, err := s.Libraries(ctx, ListOptions{States: []State{StateActive, StateMaintenance}})
	if err != nil {
		return
	}
	for _, l := range libs {
		conn, err := s.PG.ConnectDB(ctx, l.DBName)
		if err != nil {
			continue
		}
		fp, err := pg.SchemaFingerprint(ctx, conn)
		conn.Close(context.Background())
		if err != nil {
			continue
		}
		s.recordFingerprint(ctx, l, fp, "")
	}
}

func (s *Service) recordFingerprint(ctx context.Context, l *Library, fp pg.Fingerprint, jobID string) {
	if fp.Hash != "" && l.Fingerprint != "" && fp.Hash != l.Fingerprint {
		s.Events.Emit(ctx, events.Event{Type: events.LibrarySchemaChanged, Severity: events.Warning, LibraryID: l.ID, JobID: jobID,
			Message: fmt.Sprintf("The structure of %q changed (%d tables). A workstation with another Resolve version probably upgraded it", l.DisplayName, fp.Tables),
			Data:    map[string]any{"before": l.Fingerprint, "after": fp.Hash, "tables_before": l.TableCount, "tables_after": fp.Tables}})
	}
	s.DB.ExecContext(ctx, `UPDATE libraries SET schema_fingerprint = ?, schema_table_count = ?, fingerprint_checked_at = ? WHERE id = ?`,
		fp.Hash, fp.Tables, store.NowT(), l.ID)
	l.Fingerprint, l.TableCount = fp.Hash, fp.Tables
}

// Backup policy modes (ADR 0022).
const (
	// ModeCron runs a backup at every tick of the cron expression.
	ModeCron = "cron"
	// ModeActivity checks every CheckMinutes and backs up only when the
	// library's rows changed since the last successful backup.
	ModeActivity = "activity"
)

// ActivityCron is the check cadence of a policy in activity mode.
func ActivityCron(minutes int) string { return fmt.Sprintf("*/%d * * * *", minutes) }

// Policy is the backup policy of a library.
type Policy struct {
	Enabled      bool   `json:"enabled"`
	Mode         string `json:"mode"`
	CheckMinutes int    `json:"check_minutes"`
	Cron         string `json:"cron"`
	Timezone     string `json:"timezone"`
	KeepLast     int    `json:"keep_last"`
	KeepDaily    int    `json:"keep_daily"`
	KeepWeekly   int    `json:"keep_weekly"`
	KeepMonthly  int    `json:"keep_monthly"`
	MinVerified  int    `json:"min_verified"`
	// LastCheckAt and LastCheckSame describe the last activity check in
	// activity mode: when it ran and whether it found the library unchanged.
	LastCheckAt   *time.Time `json:"last_check_at,omitempty"`
	LastCheckSame bool       `json:"last_check_same"`
}

// Adaptive reports whether the policy backs up according to activity.
func (p Policy) Adaptive() bool { return p.Mode == ModeActivity }

// EffectiveCron is the expression the scheduler runs: the check cadence in
// activity mode, the user's expression otherwise.
func (p Policy) EffectiveCron() string {
	if p.Adaptive() {
		return ActivityCron(p.CheckMinutes)
	}
	return p.Cron
}

// Validate checks a policy.
func (p Policy) Validate() error {
	switch p.Mode {
	case "", ModeCron:
	case ModeActivity:
		switch p.CheckMinutes {
		case 5, 10, 15, 30, 60:
		default:
			return errors.New("SQS-B033 activity-based mode checks every 5, 10, 15, 30 or 60 minutes")
		}
	default:
		return errors.New("SQS-B034 unknown schedule mode")
	}
	if _, err := validateCron(p.EffectiveCron(), p.Timezone); err != nil {
		return err
	}
	for _, v := range []int{p.KeepLast, p.KeepDaily, p.KeepWeekly, p.KeepMonthly} {
		if v < 0 || v > 1000 {
			return errors.New("SQS-B030 retention values must be between 0 and 1000")
		}
	}
	if p.KeepLast+p.KeepDaily+p.KeepWeekly+p.KeepMonthly == 0 {
		return errors.New("SQS-B031 retention must keep at least one backup")
	}
	if p.MinVerified < 1 || p.MinVerified > 100 {
		return errors.New("SQS-B032 keep at least one verified backup (1 to 100)")
	}
	return nil
}

// Policy returns the backup policy of a library (defaults if none).
func (s *Service) Policy(ctx context.Context, libraryID string) (Policy, error) {
	var p Policy
	var lastCheck sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT enabled, schedule_cron, timezone, keep_last, keep_daily, keep_weekly, keep_monthly, min_verified,
		mode, check_minutes, last_check_at, last_check_same FROM backup_policies WHERE library_id = ?`, libraryID).
		Scan(&p.Enabled, &p.Cron, &p.Timezone, &p.KeepLast, &p.KeepDaily, &p.KeepWeekly, &p.KeepMonthly, &p.MinVerified,
			&p.Mode, &p.CheckMinutes, &lastCheck, &p.LastCheckSame)
	if errors.Is(err, sql.ErrNoRows) {
		return s.Settings(ctx).DefaultPolicy, nil
	}
	p.LastCheckAt = store.NullT(lastCheck)
	return p, err
}

// SetPolicy stores the policy and updates the schedules.
func (s *Service) SetPolicy(ctx context.Context, by audit.Actor, libraryID string, p Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	l, err := s.Library(ctx, libraryID)
	if err != nil {
		return err
	}
	if p.Mode == "" {
		p.Mode = ModeCron
	}
	// In activity mode the stored expression is the check cadence, so the
	// schedule and the "next runs" preview agree. A missed check is no
	// loss: the next one backs up if anything changed.
	misfire := 12 * time.Hour
	if p.Adaptive() {
		p.Cron = p.EffectiveCron()
		misfire = 24 * time.Hour
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO backup_policies(library_id, enabled, schedule_cron, timezone, keep_last, keep_daily, keep_weekly, keep_monthly, min_verified, mode, check_minutes, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(library_id) DO UPDATE SET enabled = excluded.enabled, schedule_cron = excluded.schedule_cron, timezone = excluded.timezone,
		keep_last = excluded.keep_last, keep_daily = excluded.keep_daily, keep_weekly = excluded.keep_weekly, keep_monthly = excluded.keep_monthly,
		min_verified = excluded.min_verified, mode = excluded.mode, check_minutes = excluded.check_minutes, updated_at = excluded.updated_at`,
		libraryID, p.Enabled, p.Cron, p.Timezone, p.KeepLast, p.KeepDaily, p.KeepWeekly, p.KeepMonthly, p.MinVerified, p.Mode, p.CheckMinutes, store.NowT())
	if err != nil {
		return err
	}
	enabled := p.Enabled && (l.State == StateActive || l.State == StateMaintenance)
	if err := s.Sched.Upsert(ctx, ScheduleBackup, libraryID, p.Cron, p.Timezone, enabled, misfire); err != nil {
		return err
	}
	if by.Type != "" {
		s.Audit.Record(ctx, by, "library.policy", "library", libraryID, audit.OK, map[string]any{"policy": p})
	}
	return nil
}

func (s *Service) ensurePolicy(ctx context.Context, libraryID string) error {
	p, err := s.Policy(ctx, libraryID)
	if err != nil {
		return err
	}
	return s.SetPolicy(ctx, audit.Actor{}, libraryID, p)
}

func validateCron(cron, tz string) (*time.Location, error) { return scheduler.Validate(cron, tz) }
