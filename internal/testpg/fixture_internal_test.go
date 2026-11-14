// SPDX-License-Identifier: AGPL-3.0-only

package testpg

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A port that another process takes between freePort and the start of
// postgres makes postgres exit with "Address already in use"; Start then
// starts it on another port, and the cluster looks like any other.
func TestStartTakesAnotherPortWhenItsPortIsTaken(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	taken := l.Addr().(*net.TCPAddr).Port
	orig := freePort
	var given []int
	freePort = func() (int, error) {
		p, err := taken, error(nil)
		if len(given) > 0 {
			p, err = orig()
		}
		given = append(given, p)
		return p, err
	}
	t.Cleanup(func() { freePort = orig })
	c := Start(t)
	if len(given) != 2 || c.Port != given[1] || c.Port == taken {
		t.Fatalf("ports handed out %v, the cluster has %d, the taken one is %d", given, c.Port, taken)
	}
	conn, err := c.ConnectTCP(t.Context(), "postgres", c.SuperPass, "postgres")
	if err == nil {
		conn.Close(t.Context())
	}
	if err == nil || !strings.Contains(err.Error(), "pg_hba.conf rejects connection") {
		t.Fatalf("the server does not answer on its port: %v", err)
	}
	log := c.Log()
	if strings.Count(log, "database system is ready to accept connections") != 1 || strings.Contains(log, "Address already in use") {
		t.Fatalf("the log keeps the failed start:\n%s", log)
	}
}

// TestHelperCluster is the helper process of TestKilledTestLeavesNoCluster:
// with SQSOLVE_TESTPG_HELPER set it starts a cluster, writes the pid of its
// postmaster and its folders to that file, and waits to be killed.
func TestHelperCluster(t *testing.T) {
	report := os.Getenv("SQSOLVE_TESTPG_HELPER")
	if report == "" {
		t.Skip("runs only as the helper process of TestKilledTestLeavesNoCluster")
	}
	c := Start(t)
	line := fmt.Sprintf("%d\n%s\n%s\n", c.cmd.Process.Pid, c.Base, c.SocketDir)
	if err := os.WriteFile(report+".partial", []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(report+".partial", report); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Minute)
}

// procStart returns the start time of pid (field 22 of /proc/<pid>/stat),
// or "" when it is gone or has exited (Z or X). A later process that reuses
// the pid has another start time.
func procStart(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return ""
	}
	f := strings.Fields(s[i+1:]) // f[0] is field 3 (state)
	if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
		return ""
	}
	return f[19]
}

// A test process that is killed takes its cluster with it, and the next
// Start removes the folder it left.
func TestKilledTestLeavesNoCluster(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Pdeathsig and /proc are Linux only")
	}
	if BinDir() == "" && os.Getenv("SQSOLVE_REQUIRE_PG") != "1" {
		t.Skip("PostgreSQL is not installed; set SQSOLVE_TEST_PG_BIN")
	}
	cache := t.TempDir()
	report := filepath.Join(t.TempDir(), "cluster")
	helper := exec.Command(os.Args[0], "-test.run=^TestHelperCluster$", "-test.v")
	helper.Env = append(os.Environ(), "SQSOLVE_TESTPG_HELPER="+report, "XDG_CACHE_HOME="+cache)
	var out bytes.Buffer
	helper.Stdout, helper.Stderr = &out, &out
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	owner := helper.Process.Pid
	exited := make(chan error, 1)
	go func() { exited <- helper.Wait() }()
	killed := false
	kill := func() {
		if !killed {
			helper.Process.Kill()
			<-exited
			killed = true
		}
	}
	t.Cleanup(kill)
	var b []byte
	for deadline := time.Now().Add(time.Minute); ; {
		var err error
		if b, err = os.ReadFile(report); err == nil {
			break
		}
		select {
		case err := <-exited:
			killed = true
			t.Fatalf("the helper ended before its cluster started: %v\n%s", err, out.String())
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			kill()
			t.Fatalf("the helper did not start its cluster\n%s", out.String())
		}
	}
	f := strings.Split(strings.TrimSpace(string(b)), "\n")
	pid, _ := strconv.Atoi(f[0])
	start := procStart(pid)
	if len(f) != 3 || start == "" {
		t.Fatalf("the helper reported %q and its postmaster has no start time", b)
	}
	base, sock := f[1], f[2]
	// Only the postmaster the helper started is signalled, and only while
	// it is still that process; the socket folder in /tmp is its own.
	t.Cleanup(func() {
		if procStart(pid) == start {
			syscall.Kill(pid, syscall.SIGQUIT)
			for i := 0; i < 100 && procStart(pid) == start; i++ {
				time.Sleep(100 * time.Millisecond)
			}
		}
		os.RemoveAll(sock)
	})
	if want := fmt.Sprintf("pg-%d-", owner); filepath.Dir(base) != filepath.Join(cache, "sqsolve-test") || !strings.HasPrefix(filepath.Base(base), want) {
		t.Fatalf("the cluster folder %s is not %s* in the cache", base, want)
	}
	kill()
	for i := 0; i < 100 && procStart(pid) == start; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if procStart(pid) == start {
		t.Fatal("the cluster outlived its test process")
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("the folder of the killed test is already gone: %v", err)
	}
	t.Setenv("XDG_CACHE_HOME", cache)
	c := Start(t)
	if filepath.Dir(c.Base) != filepath.Dir(base) {
		t.Fatalf("the next cluster is in %s, not beside %s", c.Base, base)
	}
	if _, err := os.Lstat(base); !os.IsNotExist(err) {
		t.Fatalf("the next start left the folder of the killed test: %v", err)
	}
}

// removeStale removes only the cluster folders whose test process and
// postmaster are both gone; anything else in the cache folder stays, and a
// symbolic link is never followed.
func TestRemoveStaleOnlyRemovesFoldersOfDeadTests(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("tells a dead process only from /proc")
	}
	// No process can have a pid above pid_max.
	b, err := os.ReadFile("/proc/sys/kernel/pid_max")
	if err != nil {
		t.Fatal(err)
	}
	pidMax, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	dead, self := pidMax+1, os.Getpid()
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, postmaster int) string {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Join(p, "data"), 0o700); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(p, "postgres.log"), []byte("log"), 0o600)
		if postmaster != 0 {
			os.WriteFile(filepath.Join(p, "data", "postmaster.pid"), []byte(fmt.Sprintf("%d\n%s\n", postmaster, p)), 0o600)
		}
		return p
	}
	withLink := mk(fmt.Sprintf("pg-%d-2", dead), 0)
	if err := os.Symlink(outside, filepath.Join(withLink, "link")); err != nil {
		t.Fatal(err)
	}
	removed := []string{
		mk(fmt.Sprintf("pg-%d-1", dead), 0),
		withLink,
		mk(fmt.Sprintf("pg-%d-3", dead), dead), // a postmaster that died too
	}
	kept := []string{
		mk(fmt.Sprintf("pg-%d-4", self), 0),    // its test still runs
		mk(fmt.Sprintf("pg-%d-5", dead), self), // its postmaster still runs
		mk("pg-6", 0),                          // an older fixture, no owner in the name
		mk(fmt.Sprintf("pg-%d-x", dead), 0),    // not a name the fixture makes
		mk(fmt.Sprintf("pg-%d-", dead), 0),
		mk(fmt.Sprintf("other-%d-7", dead), 0),
	}
	file := filepath.Join(dir, fmt.Sprintf("pg-%d-8", dead))
	os.WriteFile(file, []byte("x"), 0o600)
	link := filepath.Join(dir, fmt.Sprintf("pg-%d-9", dead))
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	kept = append(kept, file, link)
	removeStale(dir)
	for _, p := range removed {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s was not removed: %v", filepath.Base(p), err)
		}
	}
	for _, p := range kept {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Errorf("a file outside the cache folder was removed: %v", err)
	}
}
