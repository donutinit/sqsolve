// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/donutinit/sqsolve/internal/store"
)

// insertAdmin stores an enabled administrator account.
func insertAdmin(t *testing.T, a *App) {
	t.Helper()
	now := store.NowT()
	if _, err := a.DB.ExecContext(context.Background(), `INSERT INTO admin_users(id, username, password_hash, role, created_at, password_changed_at)
		VALUES ('UADMIN', 'diego', 'x', 'admin', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
}

// The setup code exists only while there is no administrator. A wrong code
// never runs the setup, a setup that fails keeps the code for the next try
// and a successful one spends it, whatever case or spaces it was typed with.
func TestInstallCodeIsSpentOnlyBySuccessfulSetup(t *testing.T) {
	ctx := context.Background()
	a := openApp(t, ctx, testConfig(t))
	defer a.Close()
	if a.InstallCode() != "" {
		t.Fatal("a code before it was prepared")
	}
	a.PrepareInstallCode(ctx)
	code := a.InstallCode()
	if !regexp.MustCompile(`^[A-Z2-9]{5}-[A-Z2-9]{5}$`).MatchString(code) {
		t.Fatalf("code %q", code)
	}
	ran := false
	if ok, err := a.UseInstallCode("WRONG-CODE0", func() error { ran = true; return nil }); ok || err != nil || ran {
		t.Fatalf("a wrong code: matched %v, %v, setup ran %v", ok, err, ran)
	}
	failed := errors.New("SQS-A011 invalid username: 2 to 32 characters, letters, digits, dot, hyphen or underscore")
	if ok, err := a.UseInstallCode(code, func() error { return failed }); !ok || err != failed {
		t.Fatalf("a failed setup: matched %v, %v", ok, err)
	}
	if a.InstallCode() != code {
		t.Fatal("a failed setup spent the code")
	}
	if ok, err := a.UseInstallCode("  "+strings.ToLower(code)+" ", func() error { ran = true; return nil }); !ok || err != nil || !ran {
		t.Fatalf("the right code typed in lowercase: matched %v, %v, setup ran %v", ok, err, ran)
	}
	if a.InstallCode() != "" {
		t.Fatal("a successful setup did not spend the code")
	}
	ran = false
	if ok, _ := a.UseInstallCode(code, func() error { ran = true; return nil }); ok || ran {
		t.Fatal("a spent code ran the setup again")
	}

	// With an administrator there is no code; ClearInstallCode drops one
	// prepared before the administrator existed.
	a.PrepareInstallCode(ctx)
	if a.InstallCode() == "" {
		t.Fatal("no code without an administrator")
	}
	insertAdmin(t, a)
	a.ClearInstallCode()
	if a.InstallCode() != "" {
		t.Fatal("the code survived ClearInstallCode")
	}
	a.PrepareInstallCode(ctx)
	if a.InstallCode() != "" {
		t.Fatal("a code was prepared although an administrator exists")
	}
}

// Two setups with the right code run one at a time: the second waits for the
// first and, once the first spent the code, does not run.
func TestInstallCodeSetupsRunOneAtATime(t *testing.T) {
	ctx := context.Background()
	a := openApp(t, ctx, testConfig(t))
	defer a.Close()
	a.PrepareInstallCode(ctx)
	code := a.InstallCode()
	started, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	var firstOK, secondOK, secondRan bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		firstOK, _ = a.UseInstallCode(code, func() error { close(started); <-release; return nil })
	}()
	<-started
	wg.Add(1)
	go func() {
		defer wg.Done()
		secondOK, _ = a.UseInstallCode(code, func() error { secondRan = true; return nil })
	}()
	close(release)
	wg.Wait()
	if !firstOK || secondOK || secondRan {
		t.Fatalf("first matched %v; second matched %v and ran %v", firstOK, secondOK, secondRan)
	}
}
