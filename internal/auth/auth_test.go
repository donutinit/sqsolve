// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
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
	h := HashPassword("correct horse battery")
	if !CheckPassword(h, "correct horse battery") || CheckPassword(h, "wrong") || CheckPassword("garbage", "x") {
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
	s.Now = time.Now
	// Lockout after 5 failures, for the user from any IP.
	for i := 0; i < 5; i++ {
		s.Login(ctx, "diego", "wrong", "10.0.0.9", "")
	}
	_, _, _, err = s.Login(ctx, "diego", "tungsten light 3200K", "10.0.0.3", "")
	var locked ErrLocked
	if !errors.As(err, &locked) {
		t.Fatalf("expected lockout, got %v", err)
	}
	// Ten wrong passwords in a row lock the account for good, even with the
	// right password afterwards; only Unlock lifts it and resets the count.
	base := time.Now()
	for i := 0; i < 20; i++ {
		base = base.Add(20 * time.Minute)
		s.Now = func() time.Time { return base }
		s.Login(ctx, "diego", "wrong", "10.0.0.9", "")
	}
	base = base.Add(20 * time.Minute)
	s.Now = func() time.Time { return base }
	if _, _, _, err := s.Login(ctx, "diego", "tungsten light 3200K", "10.0.0.3", ""); !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("expected account lock, got %v", err)
	}
	if u2, _ := s.UserByName(ctx, "diego"); u2.LockedAt == nil || u2.FailedLogins < LockoutAttempts {
		t.Fatal("lock not recorded")
	}
	if _, err := s.Session(ctx, raw, "", false); !errors.Is(err, ErrNoSession) {
		t.Fatal("session of a locked account still valid")
	}
	if err := s.Unlock(ctx, audit.CLI, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Login(ctx, "diego", "tungsten light 3200K", "10.0.0.3", ""); err != nil {
		t.Fatal("login after unlock:", err)
	}
	if u3, _ := s.UserByName(ctx, "diego"); u3.FailedLogins != 0 || u3.LockedAt != nil {
		t.Fatal("failure count not reset")
	}
	s.Now = time.Now
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
