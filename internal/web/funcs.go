// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
)

var months = []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

func funcs(a *app.App) template.FuncMap {
	loc := func() *time.Location { return a.Cfg.Location }
	toTime := func(v any) (time.Time, bool) {
		switch t := v.(type) {
		case time.Time:
			return t, !t.IsZero()
		case *time.Time:
			if t == nil {
				return time.Time{}, false
			}
			return *t, !t.IsZero()
		}
		return time.Time{}, false
	}
	return template.FuncMap{
		"dt": func(v any) string {
			t, ok := toTime(v)
			if !ok {
				return ""
			}
			t = t.In(loc())
			return fmt.Sprintf("%d %s %d, %02d:%02d", t.Day(), months[t.Month()-1], t.Year(), t.Hour(), t.Minute())
		},
		"iso": func(v any) string {
			t, ok := toTime(v)
			if !ok {
				return ""
			}
			return t.UTC().Format(time.RFC3339)
		},
		"ago": func(v any) string {
			t, ok := toTime(v)
			if !ok {
				return "never"
			}
			return humanAgo(time.Since(t))
		},
		"until": func(v any) string {
			t, ok := toTime(v)
			if !ok {
				return ""
			}
			return humanUntil(time.Until(t))
		},
		"dur":   func(d time.Duration) string { return humanDuration(d) },
		"bytes": func(n any) string { return core.HumanBytes(toInt64(n)) },
		"pct":   func(f float64) string { return fmt.Sprintf("%.0f %%", f) },
		"pctw":  func(f float64) string { return fmt.Sprintf("%.0f", f) },
		"kind":  core.KindLabel,
		"sev":   func(s events.Severity) string { return s.Label() },
		"sevtone": func(s events.Severity) string {
			switch s {
			case events.Warning:
				return "warn"
			case events.Error, events.Critical:
				return "bad"
			}
			return "info"
		},
		"jobtone": func(s jobs.State) string {
			switch s {
			case jobs.Succeeded:
				return "ok"
			case jobs.Failed, jobs.Interrupted:
				return "bad"
			case jobs.Running, jobs.Queued:
				return "run"
			}
			return "off"
		},
		"outcometone": func(o string) string {
			if o == audit.OK {
				return "ok"
			}
			return "bad"
		},
		"outcomelabel": func(o string) string {
			switch o {
			case audit.OK:
				return "OK"
			case audit.Denied:
				return "Denied"
			case audit.Failed:
				return "Failed"
			}
			return o
		},
		"checklabel": func(st string) string {
			switch st {
			case "ok":
				return "OK"
			case "warn":
				return "Warning"
			case "fail":
				return "Failed"
			}
			return st
		},
		"steptone": func(st string) string {
			switch st {
			case "succeeded":
				return "ok"
			case "running":
				return "run"
			case "failed":
				return "bad"
			}
			return "off"
		},
		"verifytone": func(st string) string {
			switch st {
			case core.VerifyL2OK, core.VerifyL1OK:
				return "ok"
			case core.VerifyPartial:
				return "warn"
			case core.VerifyFailed:
				return "bad"
			}
			return "off"
		},
		"verifylabel": func(st string) string {
			switch st {
			case core.VerifyL2OK:
				return "Test restore OK"
			case core.VerifyL1OK:
				return "Checksum and index OK"
			case core.VerifyPartial:
				return "Partial verification"
			case core.VerifyFailed:
				return "Verification failed"
			}
			return "Unverified"
		},
		"backuptone": func(st string) string {
			switch st {
			case core.BackupOK:
				return "ok"
			case core.BackupRunning:
				return "run"
			case core.BackupFailed, core.BackupInterrupted:
				return "bad"
			}
			return "off"
		},
		"backuplabel": func(st string) string {
			switch st {
			case core.BackupOK:
				return "Available"
			case core.BackupRunning:
				return "Running"
			case core.BackupFailed:
				return "Failed"
			case core.BackupInterrupted:
				return "Interrupted"
			case core.BackupDeleted:
				return "Deleted"
			}
			return st
		},
		"steplabel": stepLabel,
		"json": func(v any) string {
			b, _ := json.MarshalIndent(v, "", "  ")
			return string(b)
		},
		"add":  func(a, b int) int { return a + b },
		"addf": func(a, b float64) float64 { return a + b },
		"subf": func(a, b float64) float64 { return a - b },
		"short": func(s string) string {
			if len(s) > 12 {
				return s[:12]
			}
			return s
		},
		"has": func(list []string, v string) bool {
			for _, x := range list {
				if x == v {
					return true
				}
			}
			return false
		},
		"join": strings.Join,
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[fmt.Sprint(kv[i])] = kv[i+1]
			}
			return m
		},
		"jpeg": func(b []byte) template.URL {
			return template.URL("data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b))
		},
		"scopelabel": scopeLabel,
		"firstline": func(s string) string {
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				return s[:i]
			}
			return s
		},
		"tz": func() string { return loc().String() },
	}
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func humanAgo(d time.Duration) string {
	switch {
	case d < 0:
		return "in the future"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

func humanUntil(d time.Duration) string {
	switch {
	case d < 0:
		return "expired"
	case d < time.Minute:
		return "in less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("in %d min", int(d.Minutes())+1)
	case d < 48*time.Hour:
		return fmt.Sprintf("in %d h", int(d.Hours()))
	default:
		return fmt.Sprintf("in %d days", int(d.Hours()/24))
	}
}

func humanDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1f s", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%d min %d s", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}

// stepLabels names the job steps (core and jobs packages) for the UI.
var stepLabels = map[string]string{
	"validate": "Validate", "create_role": "Create role", "create_database": "Create database", "open_window": "Open initialization window",
	"network_access": "Apply network access", "prepare": "Prepare", "dump": "Dump with pg_dump", "seal": "Seal and write manifest",
	"verify_l1": "Verify checksum and index", "retention": "Apply retention", "space": "Check space",
	"test_restore": "Test restore", "compare": "Compare with the manifest", "verify_backup": "Verify backup",
	"restore": "Restore", "activate": "Activate", "check": "Check", "pre_restore_backup": "Pre-restore backup",
	"wait_for_sessions": "Wait for sessions to close", "lock": "Lock connections", "swap": "Swap databases",
	"reopen": "Reopen", "final_backup": "Final backup", "move": "Move", "reactivate": "Reactivate",
	"check_backup": "Check final backup", "drop_database": "Drop database", "drop_previous_state": "Drop previous state",
	"drop_roles": "Drop roles", "roles": "Recreate roles", "read": "Read file", "copy": "Copy to the catalog",
	"temporary_restore": "Temporary restore", "broken": "Broken", "one": "One", "two": "Two",
}

// stepLabel returns the UI name of a job step. Unknown names are prettified
// (underscores to spaces, first letter upper case) so a renamed step never
// shows up broken.
func stepLabel(name string) string {
	if l, ok := stepLabels[name]; ok {
		return l
	}
	if strings.HasPrefix(name, "backup_") {
		return "Back up " + strings.ReplaceAll(strings.TrimPrefix(name, "backup_"), "_", " ")
	}
	pretty := strings.ReplaceAll(name, "_", " ")
	if pretty == "" {
		return pretty
	}
	return strings.ToUpper(pretty[:1]) + pretty[1:]
}

// scopeLabel names an API token scope the way the panel describes roles.
func scopeLabel(s string) string {
	switch s {
	case "read":
		return "Read only"
	case "backup":
		return "Back up and verify"
	case "restore":
		return "Restore and download backups"
	case "export":
		return "Export projects and grades (experimental)"
	case "admin":
		return "Everything your role allows"
	}
	return s
}
