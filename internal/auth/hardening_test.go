// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
)

// fakeArgon replaces Argon2id with a cheap deterministic function that holds
// for a while and records how many run at once and in total.
func fakeArgon(t *testing.T, hold time.Duration) (calls, peak *atomic.Int32) {
	t.Helper()
	var cur atomic.Int32
	calls, peak = new(atomic.Int32), new(atomic.Int32)
	orig := argonKey
	argonKey = func(pw, salt []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
		n := cur.Add(1)
		calls.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(hold)
		cur.Add(-1)
		sum := sha256.Sum256(append(append([]byte{}, pw...), salt...))
		return sum[:keyLen]
	}
	t.Cleanup(func() { argonKey = orig })
	return calls, peak
}

// Audit M2: every login ran a 64 MiB Argon2id with no cap, so a burst of
// anonymous logins could exhaust the NAS memory.
func TestArgonConcurrencyIsBounded(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	calls, peak := fakeArgon(t, 40*time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Login(ctx, fmt.Sprintf("nobody%d", i), "whatever", fmt.Sprintf("10.0.1.%d", i), "")
		}()
	}
	wg.Wait()
	if calls.Load() != 16 {
		t.Fatalf("%d hashes for 16 logins", calls.Load())
	}
	if p := peak.Load(); p > 4 || p < 2 {
		t.Fatalf("%d Argon2id computations ran at once, want at most 4 (and some concurrency)", p)
	}
}

// Audit M2: failures were counted after hashing, so parallel attempts all
// passed the limiter before any of them was recorded.
func TestParallelBurstIsCounted(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	if _, err := s.CreateUser(ctx, audit.System, "diego", "", "tungsten light 3200K", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.Now = func() time.Time { return now }
	calls, _ := fakeArgon(t, 20*time.Millisecond)
	var wg sync.WaitGroup
	var limited atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var l ErrLocked
			if _, _, _, err := s.Login(ctx, "diego", "wrong", "10.0.0.9", ""); errors.As(err, &l) {
				limited.Add(1)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 5 || limited.Load() != 15 {
		t.Fatalf("20 parallel wrong passwords: %d hashed, %d rate limited; want 5 and 15", calls.Load(), limited.Load())
	}
	if u, _ := s.UserByName(ctx, "diego"); u.FailedLogins != 5 {
		t.Fatalf("failure counter %d, want 5", u.FailedLogins)
	}
}

// Audit M6: every entry of the common-password list had 12 or more
// characters, so with the 8-character minimum it rejected nothing.
func TestCommonPasswordsRejected(t *testing.T) {
	for _, p := range []string{"password", "12345678", "123456789", "qwerty123", "iloveyou", "sunshine", "princess", "football",
		"admin123", "PASSWORD", "Password1", "baseball", "trustno1", "1q2w3e4r", "davinci1", "resolve1", "blackmagic", "postgres1",
		"sqsolve1", "DaVinci1", "password1234", "davinciresolve"} {
		err := ValidatePassword(p, "diego")
		if !errors.Is(err, ErrWeakPassword) || err.Error() != "SQS-A010 weak password: it is in the list of common passwords" {
			t.Errorf("%q: %v", p, err)
		}
	}
	if err := ValidatePassword("tung 32K", "diego"); err != nil {
		t.Fatalf("uncommon password rejected: %v", err)
	}
}

func TestCommonPasswordList(t *testing.T) {
	short := 0
	for p := range common {
		if p != strings.ToLower(p) || strings.TrimSpace(p) != p || p == "" {
			t.Errorf("entry %q is not normalized", p)
		}
		if n := len([]rune(p)); n < MinPasswordLength {
			t.Errorf("entry %q is shorter than the minimum and can never match", p)
		} else if n <= 11 {
			short++
		}
	}
	if short < 200 {
		t.Fatalf("only %d entries of 8 to 11 characters", short)
	}
}

// Audit M3: ten anonymous wrong passwords locked the account for good, closed
// its sessions and broke its API tokens (ADR 0025).
func TestLockoutIsTemporary(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, err := s.CreateUser(ctx, audit.System, "diego", "", "tungsten light 3200K", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	s.Now = func() time.Time { return clock }
	raw, p, _, err := s.Login(ctx, "diego", "tungsten light 3200K", "10.0.0.2", "")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.CreateToken(ctx, p, "ci", []string{"read"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Each round comes from a new address and waits out the rate limiter's
	// back-off, which is not what is being tested here.
	attack := func(ip string) error {
		var err error
		for i := 0; i < LockoutAttempts; i++ {
			clock = clock.Add(time.Minute)
			_, _, _, err = s.Login(ctx, "diego", "wrong", ip, "")
		}
		return err
	}
	const lockedMsg = "SQS-A004 account locked for 15 minutes after 10 incorrect passwords in a row; try again later, or unlock it now in the container console: sqsolve admin unlock <username>"
	if err := attack("10.0.0.9"); !errors.Is(err, ErrAccountLocked) || err.Error() != lockedMsg {
		t.Fatalf("tenth wrong password: %v", err)
	}
	if u2, _ := s.UserByID(ctx, u.ID); !u2.IsLocked(clock) || u2.FailedLogins != LockoutAttempts {
		t.Fatalf("lock not recorded: %v %d", u2.LockedAt, u2.FailedLogins)
	}
	clock = clock.Add(time.Minute)
	if _, _, _, err := s.Login(ctx, "diego", "tungsten light 3200K", "10.0.0.3", ""); !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("right password during the lock: %v", err)
	}
	// The lock refuses password logins only.
	if sp, err := s.Session(ctx, raw, "10.0.0.2", false); err != nil || !sp.Can(ActUsersManage) {
		t.Fatalf("open session after the lock: %v", err)
	}
	if tp, err := s.TokenPrincipal(ctx, tok, ""); err != nil || !tp.Can(ActView) {
		t.Fatalf("API token after the lock: %v", err)
	}
	locked := 0
	entries, _ := s.audit.List(ctx, 0, 100, u.ID)
	for _, e := range entries {
		if e.Action == "auth.locked" {
			locked++
		}
	}
	if locked != 1 {
		t.Fatalf("%d auth.locked audit entries, want 1", locked)
	}
	// The owner retrying during the lock is refused without feeding the rate
	// limiter, so the console lifts the lock at once: the right password
	// works at the same moment, with no wait left over.
	for i := 0; i < 4; i++ {
		clock = clock.Add(2 * time.Minute)
		if _, _, _, err := s.Login(ctx, "diego", "tungsten light 3200K", "10.0.0.3", ""); !errors.Is(err, ErrAccountLocked) {
			t.Fatalf("retry %d during the lock: %v", i, err)
		}
	}
	if err := s.Unlock(ctx, audit.CLI, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Login(ctx, "diego", "tungsten light 3200K", "10.0.0.3", ""); err != nil {
		t.Fatalf("login right after unlock: %v", err)
	}
	// Without the console it expires after LockoutDuration, and the count
	// starts again from zero.
	if err := attack("10.0.0.10"); !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("second lock: %v", err)
	}
	clock = clock.Add(LockoutDuration)
	if _, _, _, err := s.Login(ctx, "diego", "tungsten light 3200K", "10.0.0.3", ""); err != nil {
		t.Fatalf("login after the lock expired: %v", err)
	}
	if u3, _ := s.UserByID(ctx, u.ID); u3.FailedLogins != 0 || u3.LockedAt != nil {
		t.Fatalf("lock not cleared: %v %d", u3.LockedAt, u3.FailedLogins)
	}
	if err := attack("10.0.0.11"); !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("third lock: %v", err)
	}
	clock = clock.Add(LockoutDuration + time.Minute)
	if _, _, _, err := s.Login(ctx, "diego", "wrong", "10.0.0.12", ""); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("wrong password after an expired lock: %v", err)
	}
	if u4, _ := s.UserByID(ctx, u.ID); u4.FailedLogins != 1 || u4.LockedAt != nil {
		t.Fatalf("count after an expired lock: %v %d", u4.LockedAt, u4.FailedLogins)
	}
}

func TestHashingBusyIsCoded(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, err := s.CreateUser(ctx, audit.System, "diego", "", "tungsten light 3200K", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	orig := hashWait
	hashWait = 20 * time.Millisecond
	for i := 0; i < HashSlots; i++ {
		hashSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < HashSlots; i++ {
			<-hashSlots
		}
		hashWait = orig
	})
	const busyMsg = "SQS-A050 the server is checking too many passwords at once; try again in a few seconds"
	if _, _, _, err := s.Login(ctx, "diego", "tungsten light 3200K", "10.0.0.2", ""); !errors.Is(err, ErrBusy) || err.Error() != busyMsg {
		t.Fatalf("login with every slot taken: %v", err)
	}
	if _, err := s.CreateUser(ctx, audit.System, "ana", "", "reference monitor 709", RoleOperator); !errors.Is(err, ErrBusy) {
		t.Fatalf("create user with every slot taken: %v", err)
	}
	if err := s.SetPassword(ctx, audit.System, u.ID, "reference monitor 709", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("set password with every slot taken: %v", err)
	}
	// A busy answer checked nothing, so it is not a wrong password.
	if u2, _ := s.UserByID(ctx, u.ID); u2.FailedLogins != 0 {
		t.Fatalf("a busy login counted as a wrong password: %d", u2.FailedLogins)
	}
	// A request that goes away stops waiting at once.
	hashWait = time.Hour
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	start := time.Now()
	if _, err := HashPassword(cctx, "x"); !errors.Is(err, ErrBusy) || time.Since(start) > time.Second {
		t.Fatalf("cancelled wait: %v after %s", err, time.Since(start))
	}
}

func TestMustEnrollTOTP(t *testing.T) {
	asked := 0
	on := func() bool { asked++; return true }
	off := func() bool { asked++; return false }
	admin := &Principal{User: &User{Role: RoleAdmin}}
	cases := []struct {
		p        *Principal
		required func() bool
		want     bool
	}{
		{admin, on, true},
		{admin, off, false},
		{&Principal{User: &User{Role: RoleAdmin, TOTPEnabled: true}}, on, false},
		{&Principal{User: &User{Role: RoleOperator}}, on, false},
		{&Principal{User: &User{Role: RoleAdmin}, TokenID: "t"}, on, false},
		{nil, on, false},
	}
	for i, c := range cases {
		if got := MustEnrollTOTP(c.p, c.required); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
	if asked != 2 {
		t.Errorf("the setting was read %d times; only administrators without a second factor need it", asked)
	}
	if ErrTOTPRequired.Error() != "SQS-A040 the second factor is required for administrators on this installation" ||
		ErrEnrollTOTP.Error() != "SQS-A051 the second factor is required for administrators on this installation: enable it in the panel before continuing" {
		t.Fatal("second factor messages changed")
	}
}

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.1/32")}
	cases := []struct {
		remote string
		xff    []string
		want   string
	}{
		{"203.0.113.5:4000", nil, "203.0.113.5"},
		{"203.0.113.5:4000", []string{"1.2.3.4"}, "203.0.113.5"},               // not a trusted proxy: ignored
		{"10.1.2.3:4000", []string{"198.51.100.7"}, "198.51.100.7"},            // trusted proxy
		{"10.1.2.3:4000", []string{"6.6.6.6, 198.51.100.7"}, "198.51.100.7"},   // the client cannot prepend
		{"10.1.2.3:4000", []string{"6.6.6.6", "198.51.100.7"}, "198.51.100.7"}, // several header lines
		{"10.1.2.3:4000", []string{"198.51.100.7, 10.9.9.9"}, "198.51.100.7"},  // chained trusted proxies
		{"10.1.2.3:4000", []string{"198.51.100.7, not-an-ip"}, "10.1.2.3"},     // garbage is not believed
		{"10.1.2.3:4000", []string{"10.4.4.4"}, "10.4.4.4"},                    // only proxies: the left-most
		{"192.168.1.1:4000", []string{" 198.51.100.7 "}, "198.51.100.7"},       // single-address prefix
		{"[::ffff:10.1.2.3]:4000", []string{"198.51.100.7"}, "198.51.100.7"},   // IPv4-mapped proxy
		{"[2001:db8::1]:4000", nil, "2001:db8::1"},
		{"10.1.2.3:4000", nil, "10.1.2.3"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		for _, v := range c.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if got := ClientIP(r, trusted); got != c.want {
			t.Errorf("%s %q: got %s want %s", c.remote, c.xff, got, c.want)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.1.2.3:4000"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got := ClientIP(r, nil); got != "10.1.2.3" {
		t.Errorf("without trusted proxies: %s", got)
	}
}
