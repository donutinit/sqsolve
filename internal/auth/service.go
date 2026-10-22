// SPDX-License-Identifier: AGPL-3.0-only

// Package auth handles panel accounts, sessions, second factor, API tokens
// and the authorization policy shared by the web UI, the API and the CLI.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// Session lifetimes (docs 08 T-03).
const (
	SessionIdle = 30 * 24 * time.Hour
	// LockoutAttempts is the number of consecutive wrong passwords that locks
	// an account for LockoutDuration (ADR 0025, amending ADR 0021).
	LockoutAttempts = 10
	// LockoutDuration is how long the lock refuses password logins. When it
	// expires the failure count starts again from zero; `sqsolve admin
	// unlock` lifts it sooner.
	LockoutDuration = 15 * time.Minute
	SessionAbsolute = 180 * 24 * time.Hour
)

// User is a panel account.
type User struct {
	ID          string
	Username    string
	DisplayName string
	Role        Role
	TOTPEnabled bool
	CreatedAt   time.Time
	LastLoginAt *time.Time
	DisabledAt  *time.Time
	// FailedLogins counts consecutive wrong passwords; at LockoutAttempts the
	// account is locked (LockedAt) for LockoutDuration (ADR 0025). The lock
	// only refuses password logins: open sessions and API tokens keep working.
	FailedLogins int
	LockedAt     *time.Time
	hash         string
	totpEnc      string
	totpLast     int64
}

// LockedUntil is when the current login lock expires (zero if none).
func (u *User) LockedUntil() time.Time {
	if u.LockedAt == nil {
		return time.Time{}
	}
	return u.LockedAt.Add(LockoutDuration)
}

// IsLocked reports whether the login lock still refuses passwords at now.
func (u *User) IsLocked(now time.Time) bool {
	return u.LockedAt != nil && now.Before(u.LockedUntil())
}

// Name returns the display name or the username.
func (u *User) Name() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// Principal is an authenticated caller.
type Principal struct {
	User      *User
	SessionID string
	TokenID   string
	Scopes    []string
	ReauthAt  time.Time
	IP        string
	CSRF      string
}

// Can reports whether the principal may perform a. A login lock does not
// matter here: it only refuses new password logins (ADR 0025).
func (p *Principal) Can(a Action) bool {
	if p == nil || p.User == nil || p.User.DisabledAt != nil {
		return false
	}
	if !RoleAllows(p.User.Role, a) {
		return false
	}
	if p.TokenID != "" {
		return ScopeAllows(p.Scopes, a)
	}
	return true
}

// Actor for audit entries.
func (p *Principal) Actor() audit.Actor {
	if p == nil || p.User == nil {
		return audit.Actor{Type: "anonymous", ID: "", Name: "anonymous", IP: ipOf(p)}
	}
	t := "user"
	if p.TokenID != "" {
		t = "token"
	}
	return audit.Actor{Type: t, ID: p.User.ID, Name: p.User.Username, IP: p.IP}
}

func ipOf(p *Principal) string {
	if p == nil {
		return ""
	}
	return p.IP
}

// Service is the authentication service.
type Service struct {
	db      *store.DB
	box     *secrets.Box
	audit   *audit.Log
	ev      *events.Recorder
	csrfKey []byte
	limiter *limiter
	Now     func() time.Time
}

// New creates the service.
func New(db *store.DB, box *secrets.Box, a *audit.Log, ev *events.Recorder, masterKey []byte) *Service {
	m := hmac.New(sha256.New, masterKey)
	m.Write([]byte("sqsolve csrf v1"))
	return &Service{db: db, box: box, audit: a, ev: ev, csrfKey: m.Sum(nil), limiter: newLimiter(), Now: time.Now}
}

const userCols = `id, username, display_name, password_hash, role, totp_secret_enc, totp_enabled, totp_last_counter, created_at, last_login_at, disabled_at, failed_logins, locked_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var role, created string
	var last, disabled, locked sql.NullString
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.hash, &role, &u.totpEnc, &u.TOTPEnabled, &u.totpLast, &created, &last, &disabled, &u.FailedLogins, &locked); err != nil {
		return nil, err
	}
	u.LockedAt = store.NullT(locked)
	u.Role = Role(role)
	u.CreatedAt = store.ParseT(created)
	u.LastLoginAt = store.NullT(last)
	u.DisabledAt = store.NullT(disabled)
	return &u, nil
}

// ErrUserNotFound is returned for unknown users.
var ErrUserNotFound = errors.New("SQS-A001 user not found")

// UserByID returns a user.
func (s *Service) UserByID(ctx context.Context, id string) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM admin_users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	return u, err
}

// UserByName returns a user by username (case-insensitive).
func (s *Service) UserByName(ctx context.Context, name string) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM admin_users WHERE username = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	return u, err
}

// Users lists accounts.
func (s *Service) Users(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM admin_users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// HasAdmin reports whether at least one active administrator exists.
func (s *Service) HasAdmin(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM admin_users WHERE role = 'admin' AND disabled_at IS NULL`).Scan(&n)
	return n > 0, err
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]{1,31}$`)

// CreateUser adds an account.
func (s *Service) CreateUser(ctx context.Context, by audit.Actor, username, display, password string, role Role) (*User, error) {
	username = strings.TrimSpace(username)
	if !usernameRe.MatchString(username) {
		return nil, errors.New("SQS-A011 invalid username: 2 to 32 characters, letters, digits, dot, hyphen or underscore")
	}
	if !role.Valid() {
		return nil, errors.New("SQS-A012 invalid role")
	}
	if err := ValidatePassword(password, username); err != nil {
		return nil, err
	}
	hash, err := HashPassword(ctx, password)
	if err != nil {
		return nil, err
	}
	id := ulid.New()
	now := store.NowT()
	// The user and its audit entry are one transaction (docs 08 §9).
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO admin_users(id, username, display_name, password_hash, role, created_at, password_changed_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, username, strings.TrimSpace(display), hash, string(role), now, now)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return errors.New("SQS-A013 a user with that name already exists")
			}
			return err
		}
		return s.audit.RecordTx(ctx, tx, by, "user.create", "user", id, audit.OK, map[string]any{"username": username, "role": role})
	})
	if err != nil {
		return nil, err
	}
	return s.UserByID(ctx, id)
}

// SetPassword changes a password, clears a login lock and ends the user's
// other sessions. API tokens are separate credentials and keep working; each
// one is revoked on its own (docs 08 §4).
func (s *Service) SetPassword(ctx context.Context, by audit.Actor, userID, password, keepSession string) error {
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := ValidatePassword(password, u.Username); err != nil {
		return err
	}
	hash, err := HashPassword(ctx, password)
	if err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE admin_users SET password_hash = ?, password_changed_at = ?, failed_logins = 0, locked_at = NULL WHERE id = ?`, hash, store.NowT(), userID); err != nil {
			return err
		}
		return s.audit.RecordTx(ctx, tx, by, "user.password", "user", userID, audit.OK, map[string]any{"username": u.Username})
	})
	if err != nil {
		return err
	}
	s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id_hash <> ?`, userID, keepSession)
	return nil
}

// hookAdminGuard, when set, runs between the last-administrator check and
// the write (tests).
var hookAdminGuard func()

// UpdateUser changes display name and role, and enables or disables.
func (s *Service) UpdateUser(ctx context.Context, by audit.Actor, userID, display string, role Role, disabled bool) error {
	if !role.Valid() {
		return errors.New("SQS-A012 invalid role")
	}
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	// The check and the write share one immediate transaction: two requests
	// that each remove one of two administrators cannot both see the other.
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		var cur string
		var curDisabled sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT role, disabled_at FROM admin_users WHERE id = ?`, userID).Scan(&cur, &curDisabled); err != nil {
			return err
		}
		if Role(cur) == RoleAdmin && !curDisabled.Valid && (role != RoleAdmin || disabled) {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM admin_users WHERE role = 'admin' AND disabled_at IS NULL AND id <> ?`, userID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return errors.New("SQS-A014 at least one active administrator must remain")
			}
		}
		if hookAdminGuard != nil {
			hookAdminGuard()
		}
		var dis any
		if disabled {
			dis = store.NowT()
			if curDisabled.Valid {
				dis = curDisabled.String
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE admin_users SET display_name = ?, role = ?, disabled_at = ? WHERE id = ?`, strings.TrimSpace(display), string(role), dis, userID); err != nil {
			return err
		}
		return s.audit.RecordTx(ctx, tx, by, "user.update", "user", userID, audit.OK, map[string]any{"username": u.Username, "role": role, "disabled": disabled})
	})
	if err != nil {
		return err
	}
	// A disabled user is refused on every request (Principal.Can, Session,
	// TokenPrincipal); these only tidy up.
	if disabled {
		s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
		s.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, store.NowT(), userID)
	}
	return nil
}

// ErrBadCredentials hides whether the user exists.
var ErrBadCredentials = errors.New("SQS-A002 incorrect username or password")

// ErrAccountLocked is returned while an account is locked after
// LockoutAttempts wrong passwords in a row (ADR 0025).
var ErrAccountLocked = errors.New("SQS-A004 account locked for 15 minutes after 10 incorrect passwords in a row; try again later, or unlock it now in the container console: sqsolve admin unlock <username>")

// ErrLocked is returned while a login is rate limited.
type ErrLocked struct{ Until time.Time }

func (e ErrLocked) Error() string {
	return fmt.Sprintf("SQS-A003 too many attempts; wait %s", time.Until(e.Until).Round(time.Second))
}

// Login checks credentials and creates a session. When the user has TOTP the
// session starts as pending until VerifySecondFactor succeeds. The rate
// limiter counts the attempt before the password is hashed, so parallel
// attempts cannot slip past it, and the hash runs inside one of the
// HashSlots (ErrBusy when none frees up in time). An attempt against a
// locked account is refused before it is counted: it checks no password, and
// retrying during the lock must not leave a wait behind once `sqsolve admin
// unlock` lifts it.
func (s *Service) Login(ctx context.Context, username, password, ip, ua string) (token string, p *Principal, mfa bool, err error) {
	now := s.Now()
	if until := s.limiter.blocked(ip, username, now); !until.IsZero() {
		return "", nil, false, ErrLocked{until}
	}
	u, uerr := s.UserByName(ctx, username)
	if uerr == nil && u.LockedAt != nil {
		if u.IsLocked(now) {
			s.audit.Record(ctx, audit.Actor{Type: "anonymous", Name: username, IP: ip}, "auth.login", "user", u.ID, audit.Denied, map[string]any{"locked": true})
			return "", nil, false, ErrAccountLocked
		}
		s.expireLock(ctx, u, now)
	}
	until, burst := s.limiter.reserve(ip, username, now)
	if !until.IsZero() {
		return "", nil, false, ErrLocked{until}
	}
	hash := dummyHash
	if uerr == nil {
		hash = u.hash
	}
	ok, err := CheckPassword(ctx, hash, password)
	if err != nil {
		return "", nil, false, err
	}
	if uerr != nil || !ok || u.DisabledAt != nil {
		if burst {
			s.ev.Emit(ctx, events.Event{Type: events.AuthBruteforce, Severity: events.Warning,
				Message: fmt.Sprintf("Many failed login attempts from %s", ip), Data: map[string]any{"ip": ip, "username": username}})
		}
		s.audit.Record(ctx, audit.Actor{Type: "anonymous", Name: username, IP: ip}, "auth.login", "user", username, audit.Denied, nil)
		if uerr == nil && !ok && s.countFailure(ctx, u, ip, now) {
			return "", nil, false, ErrAccountLocked
		}
		return "", nil, false, ErrBadCredentials
	}
	s.limiter.success(ip, username)
	if u.FailedLogins != 0 {
		s.db.ExecContext(ctx, `UPDATE admin_users SET failed_logins = 0 WHERE id = ?`, u.ID)
	}
	token, p, err = s.newSession(ctx, u, ip, ua, u.TOTPEnabled)
	if err != nil {
		return "", nil, false, err
	}
	if !u.TOTPEnabled {
		s.db.ExecContext(ctx, `UPDATE admin_users SET last_login_at = ? WHERE id = ?`, store.NowT(), u.ID)
		s.audit.Record(ctx, p.Actor(), "auth.login", "user", u.ID, audit.OK, nil)
	}
	return token, p, u.TOTPEnabled, nil
}

// expireLock clears a lock whose LockoutDuration has passed, together with
// its failure count, so the account starts again from zero.
func (s *Service) expireLock(ctx context.Context, u *User, now time.Time) {
	s.db.ExecContext(ctx, `UPDATE admin_users SET failed_logins = 0, locked_at = NULL WHERE id = ? AND locked_at IS NOT NULL AND locked_at <= ?`,
		u.ID, store.T(now.Add(-LockoutDuration)))
	u.FailedLogins, u.LockedAt = 0, nil
}

// countFailure records a wrong password and, at LockoutAttempts consecutive
// failures, locks the account for LockoutDuration. The database increments
// the count, so parallel failures are not lost. Locking never closes
// sessions nor revokes API tokens (ADR 0025): an anonymous caller who only
// knows the username must not be able to log its owner out.
func (s *Service) countFailure(ctx context.Context, u *User, ip string, now time.Time) bool {
	var n int
	if err := s.db.QueryRowContext(ctx, `UPDATE admin_users SET failed_logins = failed_logins + 1 WHERE id = ? RETURNING failed_logins`, u.ID).Scan(&n); err != nil {
		return false
	}
	if n < LockoutAttempts {
		return false
	}
	res, err := s.db.ExecContext(ctx, `UPDATE admin_users SET locked_at = ? WHERE id = ? AND locked_at IS NULL`, store.T(now), u.ID)
	if err != nil {
		return true
	}
	if first, _ := res.RowsAffected(); first == 1 {
		minutes := int(LockoutDuration.Minutes())
		s.audit.Record(ctx, audit.Actor{Type: "anonymous", Name: u.Username, IP: ip}, "auth.locked", "user", u.ID, audit.OK, map[string]any{"attempts": n, "minutes": minutes})
		s.ev.Emit(ctx, events.Event{Type: events.AuthLocked, Severity: events.Critical,
			Message: fmt.Sprintf("Account %q was locked for %d minutes after %d consecutive incorrect passwords. Its open sessions and API tokens keep working. To unlock it sooner: \"sqsolve admin unlock %s\" in the container console", u.Username, minutes, n, u.Username),
			Data:    map[string]any{"username": u.Username, "ip": ip}})
	}
	return true
}

// Unlock lifts a login lock before it expires and resets the failure count.
// It is only offered from the container console (ADR 0021).
func (s *Service) Unlock(ctx context.Context, by audit.Actor, userID string) error {
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE admin_users SET failed_logins = 0, locked_at = NULL WHERE id = ?`, userID); err != nil {
		return err
	}
	return s.audit.Record(ctx, by, "auth.unlock", "user", userID, audit.OK, map[string]any{"username": u.Username})
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func (s *Service) newSession(ctx context.Context, u *User, ip, ua string, pending bool) (string, *Principal, error) {
	raw := secrets.RandomToken(32)
	id := hashToken(raw)
	now := s.Now()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(id_hash, user_id, created_at, expires_at, last_seen_at, reauth_at, mfa_pending, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, u.ID, store.T(now), store.T(now.Add(SessionAbsolute)), store.T(now), store.T(now), pending, ip, ua)
	if err != nil {
		return "", nil, err
	}
	return raw, &Principal{User: u, SessionID: id, ReauthAt: now, IP: ip, CSRF: s.csrfFor(raw)}, nil
}

func (s *Service) csrfFor(raw string) string {
	m := hmac.New(sha256.New, s.csrfKey)
	m.Write([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// CheckCSRF validates a form or header token against the session cookie.
func (s *Service) CheckCSRF(rawSession, token string) bool {
	if rawSession == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(s.csrfFor(rawSession)), []byte(token)) == 1
}

// ErrNoSession means the cookie is missing, expired or revoked.
var ErrNoSession = errors.New("SQS-A004 invalid or expired session")

// ErrMFAPending means the session still needs the second factor.
var ErrMFAPending = errors.New("SQS-A005 second factor required")

// Session resolves a session cookie. allowPending accepts sessions waiting
// for the second factor (only for the TOTP page).
func (s *Service) Session(ctx context.Context, raw, ip string, allowPending bool) (*Principal, error) {
	if raw == "" {
		return nil, ErrNoSession
	}
	id := hashToken(raw)
	var userID, expires, last, reauth string
	var pending bool
	err := s.db.QueryRowContext(ctx, `SELECT user_id, expires_at, last_seen_at, reauth_at, mfa_pending FROM sessions WHERE id_hash = ?`, id).
		Scan(&userID, &expires, &last, &reauth, &pending)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}
	now := s.Now()
	if now.After(store.ParseT(expires)) || now.Sub(store.ParseT(last)) > SessionIdle {
		s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, id)
		return nil, ErrNoSession
	}
	if pending && !allowPending {
		return nil, ErrMFAPending
	}
	u, err := s.UserByID(ctx, userID)
	if err != nil || u.DisabledAt != nil {
		return nil, ErrNoSession
	}
	if now.Sub(store.ParseT(last)) > time.Minute {
		s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?, ip = ? WHERE id_hash = ?`, store.T(now), ip, id)
	}
	return &Principal{User: u, SessionID: id, ReauthAt: store.ParseT(reauth), IP: ip, CSRF: s.csrfFor(raw)}, nil
}

// Logout deletes a session.
func (s *Service) Logout(ctx context.Context, p *Principal) {
	if p == nil {
		return
	}
	s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, p.SessionID)
	s.audit.Record(ctx, p.Actor(), "auth.logout", "user", p.User.ID, audit.OK, nil)
}

// LogoutEverywhere deletes every session of the user except the current one.
func (s *Service) LogoutEverywhere(ctx context.Context, p *Principal) {
	s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id_hash <> ?`, p.User.ID, p.SessionID)
	s.audit.Record(ctx, p.Actor(), "auth.logout_all", "user", p.User.ID, audit.OK, nil)
}

// ErrBadCode is returned for a wrong second factor.
var ErrBadCode = errors.New("SQS-A006 incorrect code")

func (s *Service) checkSecondFactor(ctx context.Context, u *User, code string) error {
	code = strings.TrimSpace(code)
	secret, err := s.box.Open(u.totpEnc, "admin_users.totp_secret_enc."+u.ID)
	if err != nil {
		return err
	}
	if n, ok := VerifyTOTP(secret.Reveal(), code, s.Now()); ok {
		// One conditional update: of two sessions presenting the same code,
		// only the first moves the counter past it.
		res, err := s.db.ExecContext(ctx, `UPDATE admin_users SET totp_last_counter = ? WHERE id = ? AND totp_last_counter < ?`, int64(n), u.ID, int64(n))
		if err != nil {
			return err
		}
		if k, err := res.RowsAffected(); err != nil {
			return err
		} else if k != 1 {
			return ErrBadCode
		}
		u.totpLast = int64(n)
		return nil
	}
	// Recovery code.
	h := hashToken(strings.ToLower(strings.ReplaceAll(code, " ", "")))
	res, err := s.db.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`, store.NowT(), u.ID, h)
	if err == nil {
		if n, _ := res.RowsAffected(); n == 1 {
			s.audit.Record(ctx, audit.Actor{Type: "user", ID: u.ID, Name: u.Username}, "auth.recovery_code", "user", u.ID, audit.OK, nil)
			return nil
		}
	}
	return ErrBadCode
}

// VerifySecondFactor completes a pending login. As in Login, the attempt is
// counted before the code is checked.
func (s *Service) VerifySecondFactor(ctx context.Context, p *Principal, code string) error {
	now := s.Now()
	if until, _ := s.limiter.reserve(p.IP, p.User.Username, now); !until.IsZero() {
		return ErrLocked{until}
	}
	if err := s.checkSecondFactor(ctx, p.User, code); err != nil {
		s.audit.Record(ctx, p.Actor(), "auth.totp", "user", p.User.ID, audit.Denied, nil)
		return err
	}
	s.limiter.success(p.IP, p.User.Username)
	s.db.ExecContext(ctx, `UPDATE sessions SET mfa_pending = 0, reauth_at = ? WHERE id_hash = ?`, store.T(now), p.SessionID)
	s.db.ExecContext(ctx, `UPDATE admin_users SET last_login_at = ? WHERE id = ?`, store.NowT(), p.User.ID)
	return s.audit.Record(ctx, p.Actor(), "auth.login", "user", p.User.ID, audit.OK, map[string]any{"mfa": true})
}

// BeginTOTP generates a secret for enrolment (not yet enabled).
func (s *Service) BeginTOTP(u *User) (secret, uri string) {
	secret = NewTOTPSecret()
	return secret, OTPAuthURI(secret, u.Username, "sqsolve")
}

// EnableTOTP stores the secret after the user proves it works and returns
// fresh recovery codes.
func (s *Service) EnableTOTP(ctx context.Context, p *Principal, secret, code string) ([]string, error) {
	n, ok := VerifyTOTP(secret, code, s.Now())
	if !ok {
		return nil, ErrBadCode
	}
	enc, err := s.box.Seal(secrets.New(secret), "admin_users.totp_secret_enc."+p.User.ID)
	if err != nil {
		return nil, err
	}
	codes := make([]string, 10)
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE admin_users SET totp_secret_enc = ?, totp_enabled = 1, totp_last_counter = ? WHERE id = ?`, enc, int64(n), p.User.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, p.User.ID); err != nil {
			return err
		}
		for i := range codes {
			c := strings.ToLower(b32.EncodeToString(secrets.RandomBytes(5)))
			codes[i] = c[:4] + " " + c[4:]
			if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes(user_id, code_hash) VALUES (?, ?)`, p.User.ID, hashToken(c)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.audit.Record(ctx, p.Actor(), "auth.totp_enable", "user", p.User.ID, audit.OK, nil)
	return codes, nil
}

// ErrTOTPRequired refuses removing an administrator's second factor while
// the installation requires it (docs 08 §4).
var ErrTOTPRequired = errors.New("SQS-A040 the second factor is required for administrators on this installation")

// ErrEnrollTOTP refuses an administrator's browser session that has no
// second factor while the installation requires one: the panel sends it to
// enrollment and the API answers 403.
var ErrEnrollTOTP = errors.New("SQS-A051 the second factor is required for administrators on this installation: enable it in the panel before continuing")

// MustEnrollTOTP reports whether p is an administrator's session without a
// second factor while the installation requires one for administrators
// ("Second factor required for administrators", RF-USR-02). required reads
// that setting and is only called when the answer depends on it. API tokens
// are not affected: they are separate credentials with their own scopes.
func MustEnrollTOTP(p *Principal, required func() bool) bool {
	if p == nil || p.User == nil || p.TokenID != "" || p.User.Role != RoleAdmin || p.User.TOTPEnabled {
		return false
	}
	return required()
}

// DisableTOTP turns the second factor off.
func (s *Service) DisableTOTP(ctx context.Context, by audit.Actor, userID string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE admin_users SET totp_secret_enc = '', totp_enabled = 0 WHERE id = ?`, userID); err != nil {
		return err
	}
	s.db.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID)
	return s.audit.Record(ctx, by, "auth.totp_disable", "user", userID, audit.OK, nil)
}

// Token is an API token (without its secret).
type Token struct {
	ID         string
	UserID     string
	Name       string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// CreateToken issues a token; the secret is returned only once.
func (s *Service) CreateToken(ctx context.Context, p *Principal, name string, scopes []string, ttl time.Duration) (string, *Token, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return "", nil, errors.New("SQS-A020 the token needs a name of up to 80 characters")
	}
	for _, sc := range scopes {
		if _, ok := Scopes[sc]; !ok {
			return "", nil, fmt.Errorf("SQS-A021 unknown scope: %s", sc)
		}
	}
	if len(scopes) == 0 {
		return "", nil, errors.New("SQS-A022 choose at least one scope")
	}
	if ttl <= 0 || ttl > 365*24*time.Hour {
		ttl = 365 * 24 * time.Hour
	}
	raw := "sqs_" + secrets.RandomToken(32)
	id := ulid.New()
	sc, _ := json.Marshal(scopes)
	exp := s.Now().Add(ttl)
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO api_tokens(id, user_id, name, token_hash, scopes, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, p.User.ID, name, hashToken(raw), string(sc), store.NowT(), store.T(exp)); err != nil {
			return err
		}
		return s.audit.RecordTx(ctx, tx, p.Actor(), "token.create", "token", id, audit.OK, map[string]any{"name": name, "scopes": scopes})
	})
	if err != nil {
		return "", nil, err
	}
	return raw, &Token{ID: id, UserID: p.User.ID, Name: name, Scopes: scopes, CreatedAt: s.Now(), ExpiresAt: &exp}, nil
}

// Tokens lists tokens of a user (all users if userID is empty).
func (s *Service) Tokens(ctx context.Context, userID string) ([]*Token, error) {
	q := `SELECT id, user_id, name, scopes, created_at, expires_at, last_used_at, revoked_at FROM api_tokens`
	var args []any
	if userID != "" {
		q += ` WHERE user_id = ?`
		args = append(args, userID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Token
	for rows.Next() {
		var t Token
		var scopes, created string
		var exp, used, rev sql.NullString
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &scopes, &created, &exp, &used, &rev); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(scopes), &t.Scopes)
		t.CreatedAt = store.ParseT(created)
		t.ExpiresAt, t.LastUsedAt, t.RevokedAt = store.NullT(exp), store.NullT(used), store.NullT(rev)
		out = append(out, &t)
	}
	return out, rows.Err()
}

// RevokeToken revokes a token owned by the principal (or any, for admins).
func (s *Service) RevokeToken(ctx context.Context, p *Principal, id string) error {
	q := `UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`
	args := []any{store.NowT(), id}
	if !p.Can(ActUsersManage) {
		q += ` AND user_id = ?`
		args = append(args, p.User.ID)
	}
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("SQS-A023 token not found")
		}
		return s.audit.RecordTx(ctx, tx, p.Actor(), "token.revoke", "token", id, audit.OK, nil)
	})
}

// ErrBadToken is returned for invalid API tokens.
var ErrBadToken = errors.New("SQS-A024 invalid, expired or revoked token")

// TokenPrincipal resolves a bearer token.
func (s *Service) TokenPrincipal(ctx context.Context, raw, ip string) (*Principal, error) {
	if !strings.HasPrefix(raw, "sqs_") {
		return nil, ErrBadToken
	}
	var id, userID, scopes string
	var exp, rev sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, scopes, expires_at, revoked_at FROM api_tokens WHERE token_hash = ?`, hashToken(raw)).
		Scan(&id, &userID, &scopes, &exp, &rev)
	if err != nil {
		return nil, ErrBadToken
	}
	if rev.Valid || (exp.Valid && s.Now().After(store.ParseT(exp.String))) {
		return nil, ErrBadToken
	}
	u, err := s.UserByID(ctx, userID)
	if err != nil || u.DisabledAt != nil {
		return nil, ErrBadToken
	}
	var sc []string
	json.Unmarshal([]byte(scopes), &sc)
	s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, store.NowT(), id)
	return &Principal{User: u, TokenID: id, Scopes: sc, IP: ip}, nil
}

// PurgeSessions deletes expired sessions.
func (s *Service) PurgeSessions(ctx context.Context) {
	now := s.Now()
	s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ? OR last_seen_at < ?`, store.T(now), store.T(now.Add(-SessionIdle)))
}

// limiter tracks failed logins per IP and per username.
type limiter struct {
	mu sync.Mutex
	m  map[string]*attempts
}

type attempts struct {
	failures int
	last     time.Time
	until    time.Time
}

func newLimiter() *limiter { return &limiter{m: map[string]*attempts{}} }

func (l *limiter) keys(ip, user string) []string {
	return []string{"ip:" + ip, "user:" + strings.ToLower(user)}
}

// blocked reports until when the caller must wait (zero if not limited),
// without counting anything. reserve repeats the check when it counts.
func (l *limiter) blocked(ip, user string, now time.Time) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.waitLocked(ip, user, now)
}

// waitLocked is blocked with l.mu held.
func (l *limiter) waitLocked(ip, user string, now time.Time) (until time.Time) {
	for _, k := range l.keys(ip, user) {
		if a := l.m[k]; a != nil && a.until.After(now) && a.until.After(until) {
			until = a.until
		}
	}
	return until
}

// reserve checks the limit and, when the attempt may go ahead, counts it as a
// failure before the caller checks the password or the code, in the same
// critical section, so parallel attempts are counted as they arrive (audit
// M2); success clears the count. It returns when the caller may try again
// (zero if it may go ahead now) and whether this attempt reached the burst
// threshold.
func (l *limiter) reserve(ip, user string, now time.Time) (until time.Time, burst bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until = l.waitLocked(ip, user, now); !until.IsZero() {
		return until, false
	}
	if len(l.m) > 10000 {
		// Random usernames must not grow the map without bound.
		for k, a := range l.m {
			if now.Sub(a.last) > time.Hour && !a.until.After(now) {
				delete(l.m, k)
			}
		}
	}
	for _, k := range l.keys(ip, user) {
		a := l.m[k]
		if a == nil || now.Sub(a.last) > time.Hour {
			a = &attempts{}
			l.m[k] = a
		}
		a.failures++
		a.last = now
		if a.failures >= 5 {
			d := time.Duration(1<<uint(min(a.failures-5, 10))) * time.Second
			if d > 15*time.Minute {
				d = 15 * time.Minute
			}
			a.until = now.Add(d)
			if a.failures == 5 {
				burst = true
			}
		}
	}
	return time.Time{}, burst
}

func (l *limiter) success(ip, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range l.keys(ip, user) {
		delete(l.m, k)
	}
}
