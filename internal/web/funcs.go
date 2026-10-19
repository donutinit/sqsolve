// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
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

var months = []string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"}

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
				return "nunca"
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
				return "Correcto"
			case audit.Denied:
				return "Denegado"
			case audit.Failed:
				return "Fallido"
			}
			return o
		},
		"checklabel": func(st string) string {
			switch st {
			case "ok":
				return "Correcto"
			case "warn":
				return "Aviso"
			case "fail":
				return "Fallido"
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
				return "Restauración de prueba correcta"
			case core.VerifyL1OK:
				return "Suma e índice correctos"
			case core.VerifyPartial:
				return "Verificación parcial"
			case core.VerifyFailed:
				return "Verificación fallida"
			}
			return "Sin verificar"
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
				return "Disponible"
			case core.BackupRunning:
				return "En curso"
			case core.BackupFailed:
				return "Fallido"
			case core.BackupInterrupted:
				return "Interrumpido"
			case core.BackupDeleted:
				return "Borrado"
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
		return "en el futuro"
	case d < time.Minute:
		return "hace un momento"
	case d < time.Hour:
		return fmt.Sprintf("hace %d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("hace %d h", int(d.Hours()))
	default:
		return fmt.Sprintf("hace %d días", int(d.Hours()/24))
	}
}

func humanUntil(d time.Duration) string {
	switch {
	case d < 0:
		return "vencido"
	case d < time.Minute:
		return "en menos de un minuto"
	case d < time.Hour:
		return fmt.Sprintf("en %d min", int(d.Minutes())+1)
	case d < 48*time.Hour:
		return fmt.Sprintf("en %d h", int(d.Hours()))
	default:
		return fmt.Sprintf("en %d días", int(d.Hours()/24))
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

var stepLabels = map[string]string{
	"validar": "Validar", "crear_rol": "Crear rol", "crear_base": "Crear base", "abrir_ventana": "Abrir ventana de inicialización",
	"acceso_red": "Aplicar acceso de red", "preparar": "Preparar", "volcar": "Volcar con pg_dump", "sellar": "Sellar y escribir manifiesto",
	"verificar_l1": "Verificar suma e índice", "retencion": "Aplicar retención", "espacio": "Comprobar espacio",
	"restaurar_prueba": "Restauración de prueba", "comparar": "Comparar con el manifiesto", "verificar_respaldo": "Verificar respaldo",
	"restaurar": "Restaurar", "activar": "Activar", "comprobar": "Comprobar", "respaldo_previo": "Respaldo previo",
	"esperar_sesiones": "Esperar a que cierren las sesiones", "bloquear": "Bloquear conexiones", "intercambiar": "Intercambiar bases",
	"reabrir": "Reabrir", "respaldo_final": "Respaldo final", "mover": "Mover", "reactivar": "Reactivar",
	"comprobar_respaldo": "Comprobar respaldo final", "borrar_base": "Borrar base", "borrar_estado_previo": "Borrar estado previo",
	"borrar_roles": "Borrar roles", "roles": "Recrear roles", "leer": "Leer archivo", "copiar": "Copiar al catálogo",
}

func stepLabel(name string) string {
	if l, ok := stepLabels[name]; ok {
		return l
	}
	if strings.HasPrefix(name, "respaldo_") {
		return "Respaldar " + strings.TrimPrefix(name, "respaldo_")
	}
	return name
}
