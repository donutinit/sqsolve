// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
)

func newService(t *testing.T) *Service {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "a.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	key := secrets.RandomBytes(32)
	box, _ := secrets.NewBox(key)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	return New(db, box, audit.New(db), events.NewRecorder(db, bus, log), key)
}

func TestPasswordHash(t *testing.T) {
	ctx := context.Background()
	h, err := HashPassword(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	check := func(hash, pw string) bool {
		ok, err := CheckPassword(ctx, hash, pw)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !check(h, "correct horse battery") || check(h, "wrong") || check("garbage", "x") {
		t.Fatal("hash check")
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatal(h)
	}
}

func TestValidatePassword(t *testing.T) {
	for _, bad := range []string{"short", "tung32K", "password1234", "aaaaaaaaaaaaaaaa", "diego-is-great-123"} {
		if ValidatePassword(bad, "diego") == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"tungsten light 3200K", "tung 32K"} {
		if err := ValidatePassword(ok, "diego"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTOTPVectors(t *testing.T) {
	// RFC 6238 appendix B, SHA-1 seed "12345678901234567890", 8 digits
	// truncated to 6: the last 6 digits must match.
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	cases := map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"}
	for ts, want := range cases {
		got, err := TOTPCode(secret, Counter(time.Unix(ts, 0)))
		if err != nil || got != want {
			t.Errorf("t=%d got %s want %s", ts, got, want)
		}
	}
	if _, ok := VerifyTOTP(secret, "287082", time.Unix(59+30, 0)); !ok {
		t.Error("previous step not accepted")
	}
	if _, ok := VerifyTOTP(secret, "287082", time.Unix(59+90, 0)); ok {
		t.Error("old code accepted")
	}
}

func TestRoleLabels(t *testing.T) {
	want := map[Role]string{RoleAdmin: "Administrator", RoleOperator: "Operator", RoleViewer: "Viewer", Role("other"): "other"}
	for r, label := range want {
		if got := r.Label(); got != label {
			t.Errorf("%s: got %q want %q", r, got, label)
		}
	}
}

func TestPolicyMatrix(t *testing.T) {
	for _, a := range AllActions {
		if !RoleAllows(RoleAdmin, a) {
			t.Errorf("admin cannot %s", a)
		}
		if RoleAllows(RoleViewer, a) && a != ActView && a != ActTokensOwn {
			t.Errorf("viewer can %s", a)
		}
		if RoleAllows(Role("other"), a) {
			t.Errorf("unknown role can %s", a)
		}
	}
	for _, a := range []Action{ActLibraryDelete, ActRestoreReplace, ActLibraryReveal, ActUsersManage, ActSettingsEdit} {
		if RoleAllows(RoleOperator, a) {
			t.Errorf("operator can %s", a)
		}
	}
	for _, a := range []Action{ActBackupCreate, ActRestoreNew, ActBackupDownload, ActLibraryExport} {
		if !RoleAllows(RoleOperator, a) {
			t.Errorf("operator cannot %s", a)
		}
	}
	// Token scope narrows the role.
	p := &Principal{User: &User{Role: RoleAdmin}, TokenID: "t", Scopes: []string{"read"}}
	if p.Can(ActBackupCreate) || !p.Can(ActView) {
		t.Error("scope not applied")
	}
	op := &Principal{User: &User{Role: RoleOperator}, TokenID: "t", Scopes: []string{"admin"}}
	if op.Can(ActLibraryDelete) {
		t.Error("scope widened the role")
	}
	ex := &Principal{User: &User{Role: RoleOperator}, TokenID: "t", Scopes: []string{"export"}}
	if !ex.Can(ActLibraryExport) || ex.Can(ActBackupDownload) || !ex.Can(ActView) {
		t.Error("export scope wrong")
	}
	if v := (&Principal{User: &User{Role: RoleViewer}}); v.Can(ActLibraryExport) {
		t.Error("viewer can export")
	}
}

func TestLoginSessionCSRFAndLockout(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	if _, err := s.CreateUser(ctx, audit.System, "diego", "", "password1234", RoleAdmin); err == nil {
		t.Fatal("weak password accepted")
	}
	u, err := s.CreateUser(ctx, audit.System, "diego", "Diego", "tungsten light 3200K", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, audit.System, "DIEGO", "", "tungsten light 3200K", RoleViewer); err == nil {
		t.Fatal("duplicate username accepted")
	}
	raw, p, mfa, err := s.Login(ctx, "Diego", "tungsten light 3200K", "10.0.0.2", "test")
	if err != nil || mfa || p.User.ID != u.ID {
		t.Fatalf("login: %v %v", err, mfa)
	}
	p2, err := s.Session(ctx, raw, "10.0.0.2", false)
	if err != nil || p2.User.Username != "diego" {
		t.Fatal(err)
	}
	if !s.CheckCSRF(raw, p2.CSRF) || s.CheckCSRF(raw, "x") || s.CheckCSRF("other", p2.CSRF) {
		t.Fatal("csrf")
	}
	// A session survives a long weekend and expires after SessionIdle without use.
	s.Now = func() time.Time { return time.Now().Add(4 * 24 * time.Hour) }
	if _, err := s.Session(ctx, raw, "", false); err != nil {
		t.Fatal("session dropped before SessionIdle:", err)
	}
	s.Now = func() time.Time { return time.Now().Add(4*24*time.Hour + SessionIdle + time.Hour) }
	if _, err := s.Session(ctx, raw, "", false); !errors.Is(err, ErrNoSession) {
		t.Fatal("idle session accepted")
	}
	// Lockout after 5 failures, for the user from any IP. The clock stands still so the
	// limiter's wait cannot run out while slow hashes (under -race and load) are computed.
	frozen := time.Now()
	s.Now = func() time.Time { return frozen }
	for i := 0; i < 5; i++ {
		s.Login(ctx, "diego", "wrong", "10.0.0.9", "")
	}
	_, _, _, err = s.Login(ctx, "diego", "tungsten light 3200K", "10.0.0.3", "")
	var locked ErrLocked
	if !errors.As(err, &locked) {
		t.Fatalf("expected lockout, got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "SQS-A003 too many attempts; wait ") {
		t.Fatalf("rate limit message: %v", err)
	}
	// The account lock after LockoutAttempts is in TestLockoutIsTemporary.
}

func TestTOTPFlowAndTokens(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	s.CreateUser(ctx, audit.System, "ana", "", "reference monitor 709", RoleOperator)
	raw, p, _, _ := s.Login(ctx, "ana", "reference monitor 709", "1.1.1.1", "")
	secret, uri := s.BeginTOTP(p.User)
	if !strings.HasPrefix(uri, "otpauth://totp/sqsolve:ana?") {
		t.Fatal(uri)
	}
	code, _ := TOTPCode(secret, Counter(time.Now()))
	codes, err := s.EnableTOTP(ctx, p, secret, code)
	if err != nil || len(codes) != 10 {
		t.Fatal(err)
	}
	s.Logout(ctx, p)
	if _, err := s.Session(ctx, raw, "", false); err == nil {
		t.Fatal("logout did not end session")
	}
	raw, p, mfa, _ := s.Login(ctx, "ana", "reference monitor 709", "1.1.1.1", "")
	if !mfa {
		t.Fatal("mfa not required")
	}
	if _, err := s.Session(ctx, raw, "", false); !errors.Is(err, ErrMFAPending) {
		t.Fatal("pending session accepted")
	}
	// The code used at enrolment cannot be replayed; a recovery code works once.
	if err := s.VerifySecondFactor(ctx, p, code); err == nil {
		t.Fatal("replayed code accepted")
	}
	if err := s.VerifySecondFactor(ctx, p, codes[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, raw, "", false); err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.CreateToken(ctx, p, "ci", []string{"backup"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := s.TokenPrincipal(ctx, tok, "")
	if err != nil || !tp.Can(ActBackupCreate) || tp.Can(ActLibraryDelete) {
		t.Fatal("token principal")
	}
	ts, _ := s.Tokens(ctx, p.User.ID)
	s.RevokeToken(ctx, p, ts[0].ID)
	if _, err := s.TokenPrincipal(ctx, tok, ""); err == nil {
		t.Fatal("revoked token accepted")
	}
}

// enrolledTOTP creates an operator with a second factor enabled at now and
// returns its secret.
func enrolledTOTP(t *testing.T, s *Service, now time.Time) string {
	t.Helper()
	ctx := context.Background()
	s.Now = func() time.Time { return now }
	if _, err := s.CreateUser(ctx, audit.System, "ana", "", "reference monitor 709", RoleOperator); err != nil {
		t.Fatal(err)
	}
	_, p, _, err := s.Login(ctx, "ana", "reference monitor 709", "1.1.1.1", "")
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := s.BeginTOTP(p.User)
	code, _ := TOTPCode(secret, Counter(now))
	if _, err := s.EnableTOTP(ctx, p, secret, code); err != nil {
		t.Fatal(err)
	}
	return secret
}

// Audit AUD-005: the replay check compared the counter loaded at login, so
// a second pending session that loaded the same counter accepted a code the
// first one had already used.
func TestTOTPCodeAcceptedOnceAcrossSessions(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	start := time.Now()
	secret := enrolledTOTP(t, s, start)
	s.Now = func() time.Time { return start.Add(time.Minute) }
	var pending []*Principal
	for i := 0; i < 2; i++ {
		_, p, mfa, err := s.Login(ctx, "ana", "reference monitor 709", "1.1.1.1", "")
		if err != nil || !mfa {
			t.Fatalf("login %d: mfa %v, %v", i, mfa, err)
		}
		pending = append(pending, p)
	}
	code, _ := TOTPCode(secret, Counter(s.Now()))
	if err := s.VerifySecondFactor(ctx, pending[0], code); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifySecondFactor(ctx, pending[1], code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("the code was accepted twice: %v", err)
	}
}

func TestTOTPCodeAcceptedOnceUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	start := time.Now()
	secret := enrolledTOTP(t, s, start)
	s.Now = func() time.Time { return start.Add(time.Minute) }
	const n = 4
	pending := make([]*Principal, n)
	for i := range pending {
		_, p, _, err := s.Login(ctx, "ana", "reference monitor 709", "10.0.0."+string(rune('1'+i)), "")
		if err != nil {
			t.Fatal(err)
		}
		pending[i] = p
	}
	code, _ := TOTPCode(secret, Counter(s.Now()))
	var wg sync.WaitGroup
	gate := make(chan struct{})
	errs := make([]error, n)
	for i := range pending {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			errs[i] = s.VerifySecondFactor(ctx, pending[i], code)
		}()
	}
	close(gate)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrBadCode):
			t.Fatalf("want SQS-A006, got %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("the same code was accepted %d times", ok)
	}
}

// A failed write of the used counter must not accept the code: the replay
// protection would be gone for it.
func TestTOTPCounterWriteFailureRefusesTheCode(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	start := time.Now()
	secret := enrolledTOTP(t, s, start)
	s.Now = func() time.Time { return start.Add(time.Minute) }
	raw, p, _, err := s.Login(ctx, "ana", "reference monitor 709", "1.1.1.1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER no_counter BEFORE UPDATE OF totp_last_counter ON admin_users BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	code, _ := TOTPCode(secret, Counter(s.Now()))
	err = s.VerifySecondFactor(ctx, p, code)
	if err == nil || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("want the write error, got %v", err)
	}
	if _, err := s.Session(ctx, raw, "", false); !errors.Is(err, ErrMFAPending) {
		t.Fatalf("the session left the second factor step: %v", err)
	}
}

func TestLastAdminProtected(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, _ := s.CreateUser(ctx, audit.System, "root1", "", "a long and odd password", RoleAdmin)
	if err := s.UpdateUser(ctx, audit.System, u.ID, "", RoleViewer, false); err == nil {
		t.Fatal("last admin demoted")
	}
	if err := s.UpdateUser(ctx, audit.System, u.ID, "", RoleAdmin, true); err == nil {
		t.Fatal("last admin disabled")
	}
}

// Audit AUD-006: with two administrators, two requests that each demote or
// disable one of them both counted the other as remaining and left none.
func TestLastAdminProtectedUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	a, _ := s.CreateUser(ctx, audit.System, "root1", "", "a long and odd password", RoleAdmin)
	b, _ := s.CreateUser(ctx, audit.System, "root2", "", "another long odd password", RoleAdmin)
	// Widen the window between the check and the write.
	hookAdminGuard = func() { time.Sleep(100 * time.Millisecond) }
	defer func() { hookAdminGuard = nil }()
	var wg sync.WaitGroup
	gate := make(chan struct{})
	errs := make([]error, 2)
	for i, change := range []func() error{
		func() error { return s.UpdateUser(ctx, audit.System, a.ID, "", RoleViewer, false) },
		func() error { return s.UpdateUser(ctx, audit.System, b.ID, "", RoleAdmin, true) },
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			errs[i] = change()
		}()
	}
	close(gate)
	wg.Wait()
	refused := 0
	for _, err := range errs {
		if err != nil {
			if err.Error() != "SQS-A014 at least one active administrator must remain" {
				t.Fatalf("want SQS-A014, got %v", err)
			}
			refused++
		}
	}
	var n int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM admin_users WHERE role = 'admin' AND disabled_at IS NULL`).Scan(&n)
	if refused != 1 || n != 1 {
		t.Fatalf("%d refused, %d active administrators left", refused, n)
	}
}

// Audit AUD-004: user and token changes are written with their audit entry
// in one transaction; without the entry the change does not happen.
func TestUserAndTokenChangesNeedTheirAuditEntry(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	admin, err := s.CreateUser(ctx, audit.System, "root1", "", "a long and odd password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.CreateUser(ctx, audit.System, "ana", "", "reference monitor 709", RoleOperator)
	_, p, _, _ := s.Login(ctx, "root1", "a long and odd password", "1.1.1.1", "")
	_, tok, err := s.CreateToken(ctx, p, "ci", []string{"backup"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	fails := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "disk I/O error") {
			t.Errorf("%s without its audit entry: %v", what, err)
		}
	}
	_, err = s.CreateUser(ctx, audit.System, "luis", "", "tungsten light 3200K", RoleViewer)
	fails("user.create", err)
	if _, err := s.UserByName(ctx, "luis"); err == nil {
		t.Error("the user was created")
	}
	fails("user.update", s.UpdateUser(ctx, audit.System, other.ID, "", RoleOperator, true))
	if u, _ := s.UserByID(ctx, other.ID); u.DisabledAt != nil {
		t.Error("the user was disabled")
	}
	fails("user.password", s.SetPassword(ctx, audit.System, other.ID, "tungsten light 3200K", ""))
	if _, _, _, err := s.Login(ctx, "ana", "reference monitor 709", "1.1.1.2", ""); err != nil {
		t.Errorf("the password was changed: %v", err)
	}
	_, _, err = s.CreateToken(ctx, p, "ci2", []string{"backup"}, 0)
	fails("token.create", err)
	if ts, _ := s.Tokens(ctx, admin.ID); len(ts) != 1 {
		t.Errorf("%d tokens", len(ts))
	}
	fails("token.revoke", s.RevokeToken(ctx, p, tok.ID))
	if ts, _ := s.Tokens(ctx, admin.ID); ts[0].RevokedAt != nil {
		t.Error("the token was revoked")
	}
}

// PLAN P0.3: unlocking a user and turning the second factor on or off are
// stored with their audit entry or not at all (SQS-X040).
func TestUnlockAndTOTPNeedTheirAuditEntry(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, err := s.CreateUser(ctx, audit.System, "root1", "", "a long and odd password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.CreateUser(ctx, audit.System, "ana", "", "reference monitor 709", RoleOperator)
	_, p, _, _ := s.Login(ctx, "root1", "a long and odd password", "1.1.1.1", "")
	secret := NewTOTPSecret()
	code, _ := TOTPCode(secret, Counter(s.Now()))
	s.db.ExecContext(ctx, `UPDATE admin_users SET failed_logins = 5, locked_at = ? WHERE id = ?`, store.NowT(), other.ID)
	enabled, _ := s.CreateUser(ctx, audit.System, "luis", "", "tungsten light 3200K", RoleAdmin)
	s.db.ExecContext(ctx, `UPDATE admin_users SET totp_enabled = 1, totp_secret_enc = 'x' WHERE id = ?`, enabled.ID)
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	unavailable := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, audit.ErrUnavailable) || !strings.HasPrefix(err.Error(), "SQS-X040 ") {
			t.Errorf("%s: want SQS-X040, got %v", what, err)
		}
	}
	unavailable("auth.unlock", s.Unlock(ctx, audit.CLI, other.ID))
	if got, _ := s.UserByID(ctx, other.ID); got.LockedAt == nil {
		t.Error("the user was unlocked")
	}
	_, err = s.EnableTOTP(ctx, p, secret, code)
	unavailable("auth.totp_enable", err)
	if got, _ := s.UserByID(ctx, u.ID); got.TOTPEnabled {
		t.Error("the second factor was enabled")
	}
	unavailable("auth.totp_disable", s.DisableTOTP(ctx, audit.System, enabled.ID))
	if got, _ := s.UserByID(ctx, enabled.ID); !got.TOTPEnabled {
		t.Error("the second factor was disabled")
	}
}

// ADR 0026 and PLAN P0.3: a login is best effort, also its second step. An
// audit log that cannot be written does not turn a valid code into an error
// after the session was already completed.
func TestSecondFactorLoginSurvivesAnAuditOutage(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	start := time.Now()
	secret := enrolledTOTP(t, s, start)
	s.Now = func() time.Time { return start.Add(time.Minute) }
	_, p, mfa, err := s.Login(ctx, "ana", "reference monitor 709", "1.1.1.1", "")
	if err != nil || !mfa {
		t.Fatalf("login: mfa %v, %v", mfa, err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	code, _ := TOTPCode(secret, Counter(s.Now()))
	if err := s.VerifySecondFactor(ctx, p, code); err != nil {
		t.Fatalf("a valid code failed because of the audit log: %v", err)
	}
}
