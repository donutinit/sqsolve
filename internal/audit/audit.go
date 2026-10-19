// SPDX-License-Identifier: AGPL-3.0-only

// Package audit keeps a hash-chained log of administrative actions.
package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/donutinit/sqsolve/internal/store"
)

// Actor is who performs an action.
type Actor struct {
	Type string `json:"type"` // user, token, cli, system, schedule
	ID   string `json:"id"`
	Name string `json:"name"`
	IP   string `json:"ip,omitempty"`
}

// Common actors.
var (
	System = Actor{Type: "system", ID: "sqsolve", Name: "sqsolve"}
	CLI    = Actor{Type: "cli", ID: "cli", Name: "consola"}
)

// Label is a human description of the actor.
func (a Actor) Label() string {
	switch a.Type {
	case "system":
		return "sistema"
	case "schedule":
		return "programación"
	case "cli":
		return "consola"
	}
	if a.Name != "" {
		return a.Name
	}
	return a.ID
}

// Outcomes.
const (
	OK     = "ok"
	Denied = "denied"
	Failed = "failed"
)

// Entry is one audit record.
type Entry struct {
	Seq        int64          `json:"seq"`
	TS         time.Time      `json:"ts"`
	Actor      Actor          `json:"actor"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Outcome    string         `json:"outcome"`
	Details    map[string]any `json:"details,omitempty"`
	PrevHash   string         `json:"prev_hash"`
	Hash       string         `json:"hash"`
}

// canonical is what the hash covers.
type canonical struct {
	TS         string         `json:"ts"`
	ActorType  string         `json:"actor_type"`
	ActorID    string         `json:"actor_id"`
	ActorName  string         `json:"actor_name"`
	ActorIP    string         `json:"actor_ip"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Outcome    string         `json:"outcome"`
	Details    map[string]any `json:"details"`
}

func hashOf(prev string, c canonical) string {
	b, _ := json.Marshal(c)
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write([]byte{'|'})
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// Log writes and verifies audit entries.
type Log struct {
	db *store.DB
	mu sync.Mutex
}

// New creates a Log.
func New(db *store.DB) *Log { return &Log{db: db} }

// Record appends an entry.
func (l *Log) Record(ctx context.Context, a Actor, action, targetType, targetID, outcome string, details map[string]any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	ctx = context.WithoutCancel(ctx)
	if details == nil {
		details = map[string]any{}
	}
	// Round-trip so the hash input equals what is read back later.
	db, _ := json.Marshal(details)
	var norm map[string]any
	json.Unmarshal(db, &norm)
	c := canonical{store.NowT(), a.Type, a.ID, a.Name, a.IP, action, targetType, targetID, outcome, norm}
	return l.db.Tx(ctx, func(tx *sql.Tx) error {
		var prev string
		err := tx.QueryRowContext(ctx, `SELECT hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&prev)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		h := hashOf(prev, c)
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_log(ts, actor_type, actor_id, actor_name, actor_ip, action, target_type, target_id, outcome, details, prev_hash, hash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.TS, c.ActorType, c.ActorID, c.ActorName, c.ActorIP, c.Action, c.TargetType, c.TargetID, c.Outcome, string(db), prev, h)
		return err
	})
}

func scanEntry(rows interface{ Scan(...any) error }) (Entry, canonical, error) {
	var e Entry
	var c canonical
	var details string
	err := rows.Scan(&e.Seq, &c.TS, &c.ActorType, &c.ActorID, &c.ActorName, &c.ActorIP, &c.Action, &c.TargetType, &c.TargetID, &c.Outcome, &details, &e.PrevHash, &e.Hash)
	if err != nil {
		return e, c, err
	}
	json.Unmarshal([]byte(details), &c.Details)
	if c.Details == nil {
		c.Details = map[string]any{}
	}
	e.TS = store.ParseT(c.TS)
	e.Actor = Actor{c.ActorType, c.ActorID, c.ActorName, c.ActorIP}
	e.Action, e.TargetType, e.TargetID, e.Outcome, e.Details = c.Action, c.TargetType, c.TargetID, c.Outcome, c.Details
	return e, c, nil
}

const cols = `seq, ts, actor_type, actor_id, actor_name, actor_ip, action, target_type, target_id, outcome, details, prev_hash, hash`

// Verify walks the whole chain. It returns the first broken seq, or 0.
func (l *Log) Verify(ctx context.Context) (brokenAt int64, count int64, err error) {
	rows, err := l.db.QueryContext(ctx, `SELECT `+cols+` FROM audit_log ORDER BY seq`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	prev := ""
	for rows.Next() {
		e, c, err := scanEntry(rows)
		if err != nil {
			return 0, count, err
		}
		count++
		if e.PrevHash != prev || hashOf(prev, c) != e.Hash {
			return e.Seq, count, nil
		}
		prev = e.Hash
	}
	return 0, count, rows.Err()
}

// List returns entries newest first. before is a seq cursor (0 for newest).
func (l *Log) List(ctx context.Context, before int64, limit int, targetID string) ([]Entry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + cols + ` FROM audit_log WHERE 1=1`
	var args []any
	if before > 0 {
		q += ` AND seq < ?`
		args = append(args, before)
	}
	if targetID != "" {
		q += ` AND target_id = ?`
		args = append(args, targetID)
	}
	q += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit)
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, _, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Export writes entries with ts in [from, to) as JSON Lines.
func (l *Log) Export(ctx context.Context, w io.Writer, from, to time.Time) (int, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT `+cols+` FROM audit_log WHERE ts >= ? AND ts < ? ORDER BY seq`, store.T(from), store.T(to))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	enc := json.NewEncoder(w)
	n := 0
	for rows.Next() {
		e, _, err := scanEntry(rows)
		if err != nil {
			return n, err
		}
		if err := enc.Encode(e); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}
