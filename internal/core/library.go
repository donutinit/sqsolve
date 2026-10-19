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
)

// Label in Spanish.
func (s State) Label() string {
	switch s {
	case StateProvisioning:
		return "Preparando"
	case StateAwaitingInit:
		return "Esperando a Resolve"
	case StateInitExpired:
		return "Inicialización vencida"
	case StateActive:
		return "Activa"
	case StateMaintenance:
		return "En mantenimiento"
	case StateRestoring:
		return "Restaurando"
	case StateTrashed:
		return "En la papelera"
	case StatePurged:
		return "Eliminada"
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
var ErrLibraryNotFound = errors.New("SQS-L002 biblioteca no encontrada")

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
		return "", errors.New("SQS-L003 el nombre visible debe tener de 1 a 80 caracteres")
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("SQS-L003 el nombre visible no puede tener caracteres de control")
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
		return fmt.Errorf("SQS-L004 ya existe una biblioteca con el identificador %q", slug)
	}
	if db, err := s.PG.Database(ctx, slug); err != nil {
		return err
	} else if db != nil {
		return fmt.Errorf("SQS-L005 ya existe una base de datos %q en el servidor (puedes adoptarla desde Mantenimiento)", slug)
	}
	if ok, err := s.PG.RoleExists(ctx, slug); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("SQS-L006 ya existe un rol %q en el servidor", slug)
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
		return nil, nil, errors.New("SQS-L007 estrategia de creación desconocida")
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
			return nil, nil, fmt.Errorf("SQS-L004 ya existe una biblioteca con el identificador %q", in.Slug)
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
		return fmt.Errorf("SQS-L010 la biblioteca no está en preparación (%s)", l.State.Label())
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
	err = r.Step("validar", func() error {
		if db, err := s.PG.Database(ctx, l.DBName); err != nil {
			return err
		} else if db != nil {
			return fmt.Errorf("SQS-L005 ya existe una base de datos %q en el servidor", l.DBName)
		}
		if ok, err := s.PG.RoleExists(ctx, l.OwnerRole); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("SQS-L006 ya existe un rol %q en el servidor", l.OwnerRole)
		}
		return nil
	})
	if err != nil {
		return rollback(err)
	}
	r.Progress(20)
	err = r.Step("crear_rol", func() error {
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
		r.Infof("Rol %s creado", l.OwnerRole)
		return err
	})
	if err != nil {
		return rollback(err)
	}
	r.Progress(50)
	if l.Strategy == StrategyPrecreate {
		err = r.Step("crear_base", func() error {
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
		err = r.Step("abrir_ventana", func() error {
			mins := s.Settings(ctx).InitWindowMinutes
			exp := s.now().Add(time.Duration(mins) * time.Minute)
			_, err := s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'awaiting_init', init_window_expires_at = ?, init_table_count = -1, updated_at = ? WHERE id = ?`,
				store.T(exp), store.NowT(), l.ID)
			r.Infof("Ventana de inicialización abierta hasta %s", exp.In(s.Cfg.Location).Format("15:04"))
			return err
		})
		if err != nil {
			return rollback(err)
		}
	}
	r.Progress(80)
	if err := r.Step("acceso_red", func() error { return s.SyncHBA(ctx) }); err != nil {
		return rollback(err)
	}
	s.Events.Emit(ctx, events.Event{Type: events.LibraryCreated, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Se creó la biblioteca %q", l.DisplayName)})
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
		return errors.New("SQS-L011 la biblioteca no está esperando inicialización")
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
			s.Log.Warn("vigilando inicialización", "library", l.Slug, "err", err)
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
			Message: fmt.Sprintf("Venció la ventana para inicializar %q desde Resolve; puedes reabrirla", l.DisplayName)})
		return nil
	}
	db, err := s.PG.Database(ctx, l.DBName)
	if err != nil || db == nil {
		return err
	}
	if db.Owner != l.OwnerRole {
		return fmt.Errorf("la base %s pertenece a %s, no a %s", l.DBName, db.Owner, l.OwnerRole)
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
					Message: fmt.Sprintf("El rol %s creó otra base durante la ventana: %s. Revísala en el servidor", l.OwnerRole, d)})
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
		Message: fmt.Sprintf("Resolve inicializó %q; la biblioteca está activa", l.DisplayName), Data: map[string]any{"tables": fp.Tables}})
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
		return errors.New("SQS-L012 límite de conexiones inválido (-1 sin límite, hasta 1000)")
	}
	rv := strings.TrimSpace(in.ResolveVersion)
	if len(rv) > 20 {
		return errors.New("SQS-L013 versión de Resolve demasiado larga")
	}
	if len(in.Notes) > 10000 {
		return errors.New("SQS-L014 notas demasiado largas")
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
		return 0, fmt.Errorf("SQS-L015 no se puede cambiar el mantenimiento en estado %s", l.State.Label())
	}
	if err := s.PG.SetAllowConnections(ctx, l.DBName, !on); err != nil {
		return 0, err
	}
	n := 0
	if on && terminate {
		n, _ = s.PG.TerminateAll(ctx, l.DBName)
	}
	st, typ, msg := StateActive, events.LibraryMaintenanceOff, "Se reabrieron las conexiones de %q"
	if on {
		st, typ, msg = StateMaintenance, events.LibraryMaintenanceOn, "%q quedó en mantenimiento: no acepta conexiones nuevas"
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
		return errors.New("SQS-L016 la sesión ya no existe o no pertenece a esta biblioteca")
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
			Message: fmt.Sprintf("Cambió la estructura de %q (%d tablas). Probablemente una estación con otra versión de Resolve la actualizó", l.DisplayName, fp.Tables),
			Data:    map[string]any{"before": l.Fingerprint, "after": fp.Hash, "tables_before": l.TableCount, "tables_after": fp.Tables}})
	}
	s.DB.ExecContext(ctx, `UPDATE libraries SET schema_fingerprint = ?, schema_table_count = ?, fingerprint_checked_at = ? WHERE id = ?`,
		fp.Hash, fp.Tables, store.NowT(), l.ID)
	l.Fingerprint, l.TableCount = fp.Hash, fp.Tables
}

// Policy is the backup policy of a library.
type Policy struct {
	Enabled     bool   `json:"enabled"`
	Cron        string `json:"cron"`
	Timezone    string `json:"timezone"`
	KeepLast    int    `json:"keep_last"`
	KeepDaily   int    `json:"keep_daily"`
	KeepWeekly  int    `json:"keep_weekly"`
	KeepMonthly int    `json:"keep_monthly"`
	MinVerified int    `json:"min_verified"`
}

// Validate checks a policy.
func (p Policy) Validate() error {
	if _, err := validateCron(p.Cron, p.Timezone); err != nil {
		return err
	}
	for _, v := range []int{p.KeepLast, p.KeepDaily, p.KeepWeekly, p.KeepMonthly} {
		if v < 0 || v > 1000 {
			return errors.New("SQS-B030 los valores de retención van de 0 a 1000")
		}
	}
	if p.KeepLast+p.KeepDaily+p.KeepWeekly+p.KeepMonthly == 0 {
		return errors.New("SQS-B031 la retención debe conservar al menos un respaldo")
	}
	if p.MinVerified < 1 || p.MinVerified > 100 {
		return errors.New("SQS-B032 conserva al menos un respaldo verificado (1 a 100)")
	}
	return nil
}

// Policy returns the backup policy of a library (defaults if none).
func (s *Service) Policy(ctx context.Context, libraryID string) (Policy, error) {
	var p Policy
	err := s.DB.QueryRowContext(ctx, `SELECT enabled, schedule_cron, timezone, keep_last, keep_daily, keep_weekly, keep_monthly, min_verified
		FROM backup_policies WHERE library_id = ?`, libraryID).Scan(&p.Enabled, &p.Cron, &p.Timezone, &p.KeepLast, &p.KeepDaily, &p.KeepWeekly, &p.KeepMonthly, &p.MinVerified)
	if errors.Is(err, sql.ErrNoRows) {
		return s.Settings(ctx).DefaultPolicy, nil
	}
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
	_, err = s.DB.ExecContext(ctx, `INSERT INTO backup_policies(library_id, enabled, schedule_cron, timezone, keep_last, keep_daily, keep_weekly, keep_monthly, min_verified, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(library_id) DO UPDATE SET enabled = excluded.enabled, schedule_cron = excluded.schedule_cron, timezone = excluded.timezone,
		keep_last = excluded.keep_last, keep_daily = excluded.keep_daily, keep_weekly = excluded.keep_weekly, keep_monthly = excluded.keep_monthly,
		min_verified = excluded.min_verified, updated_at = excluded.updated_at`,
		libraryID, p.Enabled, p.Cron, p.Timezone, p.KeepLast, p.KeepDaily, p.KeepWeekly, p.KeepMonthly, p.MinVerified, store.NowT())
	if err != nil {
		return err
	}
	enabled := p.Enabled && (l.State == StateActive || l.State == StateMaintenance)
	if err := s.Sched.Upsert(ctx, ScheduleBackup, libraryID, p.Cron, p.Timezone, enabled, 12*time.Hour); err != nil {
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
