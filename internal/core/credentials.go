// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// Credential is a PostgreSQL login for a library.
type Credential struct {
	ID          string
	LibraryID   string
	Kind        string // owner or member
	PGRole      string
	PersonLabel string
	CreatedAt   time.Time
	RotatedAt   *time.Time
	RevokedAt   *time.Time
	passwordEnc string
}

func credAAD(id string) string { return "library_credentials.password_enc." + id }

func (s *Service) addCredential(ctx context.Context, libraryID, kind, role, person string, pass secrets.String) (*Credential, error) {
	id := ulid.New()
	enc, err := s.Box.Seal(pass, credAAD(id))
	if err != nil {
		return nil, err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO library_credentials(id, library_id, kind, pg_role, person_label, password_enc, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, libraryID, kind, role, person, enc, store.NowT())
	if err != nil {
		return nil, err
	}
	return &Credential{ID: id, LibraryID: libraryID, Kind: kind, PGRole: role, PersonLabel: person, CreatedAt: s.now()}, nil
}

// Credentials lists the credentials of a library.
func (s *Service) Credentials(ctx context.Context, libraryID string) ([]*Credential, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, library_id, kind, pg_role, person_label, password_enc, created_at, rotated_at, revoked_at
		FROM library_credentials WHERE library_id = ? ORDER BY kind DESC, created_at`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Credential
	for rows.Next() {
		var c Credential
		var created string
		var rotated, revoked sql.NullString
		if err := rows.Scan(&c.ID, &c.LibraryID, &c.Kind, &c.PGRole, &c.PersonLabel, &c.passwordEnc, &created, &rotated, &revoked); err != nil {
			return nil, err
		}
		c.CreatedAt = store.ParseT(created)
		c.RotatedAt, c.RevokedAt = store.NullT(rotated), store.NullT(revoked)
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (s *Service) activeRoles(ctx context.Context, libraryID string) ([]string, error) {
	creds, err := s.Credentials(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	var roles []string
	for _, c := range creds {
		if c.RevokedAt == nil {
			roles = append(roles, c.PGRole)
		}
	}
	return roles, nil
}

func (s *Service) credential(ctx context.Context, libraryID, credID string) (*Credential, error) {
	creds, err := s.Credentials(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	for _, c := range creds {
		if c.ID == credID || credID == "" && c.Kind == "owner" {
			return c, nil
		}
	}
	return nil, errors.New("SQS-L030 credential not found")
}

// ConnectionInfo is what a workstation needs.
type ConnectionInfo struct {
	LibraryName string
	Host        string
	Port        int
	Database    string
	User        string
}

// Connection returns the connection data of the owner credential (or a
// specific credential), without the password.
func (s *Service) Connection(ctx context.Context, libraryID, credID string) (*ConnectionInfo, error) {
	l, err := s.Library(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	c, err := s.credential(ctx, libraryID, credID)
	if err != nil {
		return nil, err
	}
	return &ConnectionInfo{LibraryName: l.DisplayName, Host: s.AdvertisedHost(ctx), Port: s.Cfg.PGPort, Database: l.DBName, User: c.PGRole}, nil
}

// RevealPassword decrypts a password. The reveal is in the audit log before
// the password is returned, or it is not returned (there is no
// reauthentication, ADR 0021).
func (s *Service) RevealPassword(ctx context.Context, by audit.Actor, libraryID, credID string) (secrets.String, error) {
	c, err := s.credential(ctx, libraryID, credID)
	if err != nil {
		return secrets.String{}, err
	}
	if c.RevokedAt != nil {
		return secrets.String{}, errors.New("SQS-L031 the credential is revoked")
	}
	p, err := s.Box.Open(c.passwordEnc, credAAD(c.ID))
	if err != nil {
		return secrets.String{}, err
	}
	if err := s.Audit.Require(ctx, by, "library.credential_reveal", "library", libraryID, audit.OK, map[string]any{"role": c.PGRole}); err != nil {
		return secrets.String{}, err
	}
	s.Events.Emit(ctx, events.Event{Type: events.CredentialRevealed, LibraryID: libraryID,
		Message: fmt.Sprintf("%s viewed the password of %s", by.Label(), c.PGRole)})
	return p, nil
}

// RotatePassword sets a new random password.
func (s *Service) RotatePassword(ctx context.Context, by audit.Actor, libraryID, credID string) error {
	c, err := s.credential(ctx, libraryID, credID)
	if err != nil {
		return err
	}
	if c.RevokedAt != nil {
		return errors.New("SQS-L031 the credential is revoked")
	}
	pass := secrets.GeneratePassword(24)
	v, err := pg.ScramVerifier(pass)
	if err != nil {
		return err
	}
	enc, err := s.Box.Seal(pass, credAAD(c.ID))
	if err != nil {
		return err
	}
	// The current password, to put it back if the new one cannot be saved.
	// Rotating stays possible when it cannot be read: that is the way out.
	old, oldErr := s.Box.Open(c.passwordEnc, credAAD(c.ID))
	err = s.Audit.Do(ctx, by, "library.credential_rotate", "library", libraryID, map[string]any{"role": c.PGRole}, func() error {
		if err := s.PG.SetRolePassword(ctx, c.PGRole, v); err != nil {
			return err
		}
		if _, err := s.DB.ExecContext(ctx, `UPDATE library_credentials SET password_enc = ?, rotated_at = ? WHERE id = ?`, enc, store.NowT(), c.ID); err != nil {
			return s.restorePassword(ctx, c.PGRole, old, oldErr, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.Events.Emit(ctx, events.Event{Type: events.CredentialRotated, LibraryID: libraryID,
		Message: fmt.Sprintf("Changed the password of %s. Workstations must use the new one when they reconnect", c.PGRole)})
	return nil
}

// restorePassword puts the previous password back in PostgreSQL after the new
// one could not be saved, so the password the panel shows keeps working.
func (s *Service) restorePassword(ctx context.Context, role string, old secrets.String, oldErr, cause error) error {
	ctx = context.WithoutCancel(ctx)
	err := oldErr
	if err == nil {
		var v string
		if v, err = pg.ScramVerifier(old); err == nil {
			err = s.PG.SetRolePassword(ctx, role, v)
		}
	}
	if err != nil {
		return fmt.Errorf("SQS-L071 the new password of %s could not be saved and the previous one could not be put back (%v); rotate the password again: %w", role, err, cause)
	}
	return fmt.Errorf("SQS-L070 the new password of %s could not be saved; the previous password is still in force: %w", role, cause)
}

// AddMember creates a personal credential (docs 07 §6).
func (s *Service) AddMember(ctx context.Context, by audit.Actor, libraryID, person string) (*Credential, error) {
	l, err := s.Library(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	if !l.Live() {
		return nil, errors.New("SQS-L032 people can only be added to active libraries")
	}
	person = strings.ToLower(strings.TrimSpace(person))
	if err := pg.ValidatePerson(person); err != nil {
		return nil, err
	}
	role := pg.MemberRole(l.Slug, person)
	if ok, err := s.PG.RoleExists(ctx, role); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("SQS-L033 role %s already exists", role)
	}
	pass := secrets.GeneratePassword(24)
	v, err := pg.ScramVerifier(pass)
	if err != nil {
		return nil, err
	}
	// CreateLibraryRole is atomic: when it fails there is nothing to undo.
	// In maintenance the new person waits for it to end, like everyone (ADR 0024).
	if err := s.PG.CreateLibraryRole(ctx, role, pg.RoleOptions{Verifier: v, Login: l.State != StateMaintenance, ConnLimit: l.ConnLimit}); err != nil {
		return nil, err
	}
	// From here on the role is this call's: a failure removes it and the
	// credential row, so the person can be added again (SQS-L033 otherwise).
	var c *Credential
	undo := func(cause error) error {
		bg := context.WithoutCancel(ctx)
		if c != nil {
			if _, err := s.DB.ExecContext(bg, `DELETE FROM library_credentials WHERE id = ?`, c.ID); err != nil {
				s.Log.Warn("could not remove the credential of a member that failed to be added", "role", role, "err", err)
			}
		}
		if err := s.PG.DropRole(bg, role); err != nil {
			s.Log.Warn("could not remove the role of a member that failed to be added", "role", role, "err", err)
		}
		return fmt.Errorf("SQS-L072 could not add %s: %w", role, cause)
	}
	if err := s.PG.GrantRole(ctx, l.OwnerRole, role); err != nil {
		return nil, undo(err)
	}
	if err := s.PG.SetRoleDefaultRole(ctx, role, l.DBName, l.OwnerRole); err != nil {
		return nil, undo(err)
	}
	if c, err = s.addCredential(ctx, libraryID, "member", role, person, pass); err != nil {
		return nil, undo(err)
	}
	if err := s.PG.SecureDatabase(ctx, l.DBName, []string{role}); err != nil {
		return nil, undo(err)
	}
	s.Audit.Record(ctx, by, "library.member_add", "library", libraryID, audit.OK, map[string]any{"role": role})
	return c, s.SyncHBA(ctx)
}

// RevokeMember disables a personal credential and closes its sessions.
func (s *Service) RevokeMember(ctx context.Context, by audit.Actor, libraryID, credID string) error {
	c, err := s.credential(ctx, libraryID, credID)
	if err != nil {
		return err
	}
	if c.Kind != "member" {
		return errors.New("SQS-L034 the primary credential cannot be revoked; rotate its password")
	}
	l, err := s.Library(ctx, libraryID)
	if err != nil {
		return err
	}
	return s.Audit.Do(ctx, by, "library.member_revoke", "library", libraryID, map[string]any{"role": c.PGRole}, func() error {
		if err := s.PG.SetRoleLogin(ctx, c.PGRole, false); err != nil {
			return err
		}
		if sessions, err := s.PG.Sessions(ctx, l.DBName); err == nil {
			for _, ss := range sessions {
				if ss.User == c.PGRole {
					s.PG.TerminateSession(ctx, l.DBName, ss.PID, []string{c.PGRole})
				}
			}
		}
		// ADR 0024: every credential that is not revoked gets LOGIN back
		// when the library's access is applied, so an unrecorded revocation
		// would come undone at the next start.
		if _, err := s.DB.ExecContext(ctx, `UPDATE library_credentials SET revoked_at = ? WHERE id = ?`, store.NowT(), c.ID); err != nil {
			return fmt.Errorf("SQS-L080 the role %s was set NOLOGIN, but its revocation could not be recorded, so the next start would give it its login back; revoke it again: %w", c.PGRole, err)
		}
		return s.SyncHBA(ctx)
	})
}

// NetworkRule limits where a library can be reached from.
type NetworkRule struct {
	ID          string
	LibraryID   string
	CIDR        string
	Description string
}

// NetworkRules lists the rules of a library.
func (s *Service) NetworkRules(ctx context.Context, libraryID string) ([]NetworkRule, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, library_id, cidr, description FROM network_rules WHERE library_id = ? ORDER BY cidr`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NetworkRule
	for rows.Next() {
		var r NetworkRule
		if err := rows.Scan(&r.ID, &r.LibraryID, &r.CIDR, &r.Description); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetNetworkRules replaces the rules of a library. An empty list allows any
// address (the default).
func (s *Service) SetNetworkRules(ctx context.Context, by audit.Actor, libraryID string, cidrs []string) error {
	var norm []string
	seen := map[string]bool{}
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		n, err := pg.ParseCIDR(c)
		if err != nil {
			return err
		}
		if !seen[n] {
			seen[n] = true
			norm = append(norm, n)
		}
	}
	// The rules reach PostgreSQL with pg_hba.conf, outside SQLite.
	return s.Audit.Do(ctx, by, "library.network_rules", "library", libraryID, map[string]any{"cidrs": norm}, func() error {
		err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM network_rules WHERE library_id = ?`, libraryID); err != nil {
				return err
			}
			for _, c := range norm {
				if _, err := tx.ExecContext(ctx, `INSERT INTO network_rules(id, library_id, cidr, created_at) VALUES (?, ?, ?, ?)`, ulid.New(), libraryID, c, store.NowT()); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		return s.SyncHBA(ctx)
	})
}
