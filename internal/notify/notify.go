// SPDX-License-Identifier: AGPL-3.0-only

// Package notify sends events to webhooks (HMAC signed), ntfy and e-mail.
// Nothing leaves the server unless an administrator configures a channel.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// Kinds of channels.
const (
	Webhook = "webhook"
	Ntfy    = "ntfy"
	SMTP    = "smtp"
)

// Channel is a destination for events.
type Channel struct {
	ID          string
	Kind        string
	Name        string
	Enabled     bool
	MinSeverity events.Severity
	Pattern     string
	Config      map[string]string
	LastOK      *time.Time
	LastError   string
	CreatedAt   time.Time
}

// SecretFields are never shown back in the UI.
var SecretFields = map[string]bool{"secret": true, "token": true, "password": true}

// Notifier delivers events.
type Notifier struct {
	db       *store.DB
	box      *secrets.Box
	audit    *audit.Log
	log      *slog.Logger
	client   *http.Client
	Instance func() string
	BaseURL  func() string
	queue    chan events.Event
	mu       sync.Mutex
	last     map[string]time.Time
	held     map[string]int
	retries  []time.Duration
}

// New creates a notifier.
func New(db *store.DB, box *secrets.Box, a *audit.Log, log *slog.Logger) *Notifier {
	return &Notifier{
		db: db, box: box, audit: a, log: log,
		client:   &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		queue:    make(chan events.Event, 256),
		last:     map[string]time.Time{},
		held:     map[string]int{},
		retries:  []time.Duration{10 * time.Second, time.Minute, 10 * time.Minute, time.Hour},
		Instance: func() string { return "sqsolve" },
		BaseURL:  func() string { return "" },
	}
}

// Handle queues an event without blocking.
func (n *Notifier) Handle(e events.Event) {
	select {
	case n.queue <- e:
	default:
		n.log.Warn("cola de notificaciones llena; se descarta un evento", "type", e.Type)
	}
}

// Run delivers queued events until ctx ends.
func (n *Notifier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-n.queue:
			n.dispatch(ctx, e)
		}
	}
}

func matches(pattern, typ string) bool {
	for _, p := range strings.Split(pattern, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if ok, _ := path.Match(p, typ); ok || p == "*" {
			return true
		}
	}
	return false
}

func (n *Notifier) dispatch(ctx context.Context, e events.Event) {
	chs, err := n.Channels(ctx)
	if err != nil {
		return
	}
	for _, ch := range chs {
		if !ch.Enabled || e.Severity.Rank() < ch.MinSeverity.Rank() || !matches(ch.Pattern, e.Type) {
			continue
		}
		key := ch.ID + "|" + e.Type + "|" + e.LibraryID
		n.mu.Lock()
		if t, ok := n.last[key]; ok && time.Since(t) < 30*time.Minute {
			n.held[key]++
			n.mu.Unlock()
			continue
		}
		extra := n.held[key]
		n.held[key] = 0
		n.last[key] = time.Now()
		n.mu.Unlock()
		go n.deliver(context.WithoutCancel(ctx), ch, e, extra)
	}
}

func (n *Notifier) deliver(ctx context.Context, ch *Channel, e events.Event, extra int) {
	var err error
	for attempt := 0; ; attempt++ {
		err = n.send(ctx, ch, e, extra)
		if err == nil {
			n.db.ExecContext(ctx, `UPDATE notification_channels SET last_ok_at = ?, last_error = '' WHERE id = ?`, store.NowT(), ch.ID)
			return
		}
		if attempt >= len(n.retries) {
			break
		}
		time.Sleep(n.retries[attempt])
	}
	n.log.Warn("no se pudo notificar", "channel", ch.Name, "err", err)
	n.db.ExecContext(ctx, `UPDATE notification_channels SET last_error = ? WHERE id = ?`, store.NowT()+" "+err.Error(), ch.ID)
}

// Payload is the JSON body of webhooks (docs 10 §5).
type Payload struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Severity   string            `json:"severity"`
	OccurredAt time.Time         `json:"occurred_at"`
	Instance   string            `json:"instance"`
	LibraryID  string            `json:"library_id,omitempty"`
	JobID      string            `json:"job_id,omitempty"`
	Message    string            `json:"message"`
	Suppressed int               `json:"suppressed_since_last,omitempty"`
	Data       map[string]any    `json:"data,omitempty"`
	Links      map[string]string `json:"links,omitempty"`
}

func (n *Notifier) payload(e events.Event, extra int) Payload {
	p := Payload{ID: e.ID, Type: e.Type, Severity: string(e.Severity), OccurredAt: e.TS.UTC(), Instance: n.Instance(),
		LibraryID: e.LibraryID, JobID: e.JobID, Message: e.Message, Suppressed: extra, Data: e.Data}
	if base := n.BaseURL(); base != "" {
		p.Links = map[string]string{}
		if e.JobID != "" {
			p.Links["job"] = base + "/tareas/" + e.JobID
		}
		if e.LibraryID != "" {
			p.Links["library"] = base + "/bibliotecas/" + e.LibraryID
		}
	}
	return p
}

// Sign computes the webhook signature header value.
func Sign(secret string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strconv.FormatInt(ts, 10)))
	m.Write([]byte("."))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func (n *Notifier) send(ctx context.Context, ch *Channel, e events.Event, extra int) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	msg := e.Message
	if extra > 0 {
		msg += fmt.Sprintf(" (y %d avisos iguales más)", extra)
	}
	switch ch.Kind {
	case Webhook:
		body, _ := json.Marshal(n.payload(e, extra))
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.Config["url"], bytes.NewReader(body))
		if err != nil {
			return err
		}
		ts := time.Now().Unix()
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "sqsolve")
		req.Header.Set("X-Sqsolve-Event", e.Type)
		req.Header.Set("X-Sqsolve-Delivery", ulid.New())
		req.Header.Set("X-Sqsolve-Timestamp", strconv.FormatInt(ts, 10))
		if s := ch.Config["secret"]; s != "" {
			req.Header.Set("X-Sqsolve-Signature", Sign(s, ts, body))
		}
		return n.do(req)
	case Ntfy:
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.Config["url"], strings.NewReader(msg))
		if err != nil {
			return err
		}
		req.Header.Set("Title", "["+n.Instance()+"] "+e.Severity.Label())
		req.Header.Set("Tags", strings.ReplaceAll(e.Type, ".", "_"))
		prio := map[events.Severity]string{events.Info: "3", events.Warning: "4", events.Error: "5", events.Critical: "5"}[e.Severity]
		req.Header.Set("Priority", prio)
		if t := ch.Config["token"]; t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
		if base := n.BaseURL(); base != "" && e.JobID != "" {
			req.Header.Set("Click", base+"/tareas/"+e.JobID)
		}
		return n.do(req)
	case SMTP:
		return sendMail(ctx, ch.Config, fmt.Sprintf("[%s] %s: %s", n.Instance(), e.Severity.Label(), truncate(e.Message, 120)), msg+"\n\n"+e.TS.Format(time.RFC1123Z)+"\n")
	}
	return fmt.Errorf("SQS-N001 tipo de canal desconocido: %s", ch.Kind)
}

func (n *Notifier) do(req *http.Request) error {
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("SQS-N002 el destino respondió %s", resp.Status)
	}
	return nil
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}

func sendMail(ctx context.Context, c map[string]string, subject, body string) error {
	host := c["host"]
	port := c["port"]
	if port == "" {
		port = "587"
	}
	from, err := mail.ParseAddress(c["from"])
	if err != nil {
		return fmt.Errorf("SQS-N010 remitente inválido: %w", err)
	}
	var to []string
	for _, a := range strings.Split(c["to"], ",") {
		if a = strings.TrimSpace(a); a != "" {
			addr, err := mail.ParseAddress(a)
			if err != nil {
				return fmt.Errorf("SQS-N011 destinatario inválido: %s", a)
			}
			to = append(to, addr.Address)
		}
	}
	if len(to) == 0 {
		return errors.New("SQS-N011 sin destinatarios")
	}
	addr := net.JoinHostPort(host, port)
	d := net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if c["security"] == "tls" {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, tlsCfg)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	cl, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer cl.Close()
	if c["security"] != "tls" && c["security"] != "none" {
		if err := cl.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("SQS-N012 STARTTLS: %w", err)
		}
	}
	if u := c["username"]; u != "" {
		if err := cl.Auth(smtp.PlainAuth("", u, c["password"], host)); err != nil {
			return err
		}
	}
	if err := cl.Mail(from.Address); err != nil {
		return err
	}
	for _, t := range to {
		if err := cl.Rcpt(t); err != nil {
			return err
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@sqsolve>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n",
		from.String(), strings.Join(to, ", "), mime.QEncoding.Encode("utf-8", subject), time.Now().Format(time.RFC1123Z), ulid.New())
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	if _, err := w.Write([]byte(b.String())); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

func aad(id string) string { return "notification_channels.config_enc." + id }

// Channels lists channels with decrypted configuration.
func (n *Notifier) Channels(ctx context.Context) ([]*Channel, error) {
	rows, err := n.db.QueryContext(ctx, `SELECT id, kind, name, config_enc, enabled, min_severity, event_pattern, last_ok_at, last_error, created_at FROM notification_channels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Channel
	for rows.Next() {
		var c Channel
		var enc, sev, created string
		var lastOK sql.NullString
		if err := rows.Scan(&c.ID, &c.Kind, &c.Name, &enc, &c.Enabled, &sev, &c.Pattern, &lastOK, &c.LastError, &created); err != nil {
			return nil, err
		}
		c.MinSeverity = events.Severity(sev)
		c.LastOK = store.NullT(lastOK)
		c.CreatedAt = store.ParseT(created)
		c.Config = map[string]string{}
		if plain, err := n.box.Open(enc, aad(c.ID)); err == nil {
			json.Unmarshal([]byte(plain.Reveal()), &c.Config)
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// Channel returns one channel.
func (n *Notifier) Channel(ctx context.Context, id string) (*Channel, error) {
	all, err := n.Channels(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range all {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, errors.New("SQS-N020 canal no encontrado")
}

// Validate checks a channel's configuration.
func Validate(c *Channel) error {
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 80 {
		return errors.New("SQS-N021 el canal necesita un nombre")
	}
	switch c.MinSeverity {
	case events.Info, events.Warning, events.Error, events.Critical:
	default:
		return errors.New("SQS-N022 severidad inválida")
	}
	if c.Pattern == "" {
		c.Pattern = "*"
	}
	switch c.Kind {
	case Webhook, Ntfy:
		u, err := url.Parse(c.Config["url"])
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("SQS-N023 URL inválida (usa http:// o https://)")
		}
	case SMTP:
		if c.Config["host"] == "" || c.Config["from"] == "" || c.Config["to"] == "" {
			return errors.New("SQS-N024 el correo necesita servidor, remitente y destinatarios")
		}
		if p := c.Config["port"]; p != "" {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				return errors.New("SQS-N025 puerto inválido")
			}
		}
		switch c.Config["security"] {
		case "", "starttls", "tls", "none":
		default:
			return errors.New("SQS-N026 seguridad inválida")
		}
	default:
		return errors.New("SQS-N001 tipo de canal desconocido")
	}
	return nil
}

// Save creates or updates a channel. Empty secret fields keep their value.
func (n *Notifier) Save(ctx context.Context, by audit.Actor, c *Channel) error {
	if c.ID != "" {
		if old, err := n.Channel(ctx, c.ID); err == nil {
			for k := range SecretFields {
				if c.Config[k] == "" && old.Config[k] != "" {
					c.Config[k] = old.Config[k]
				}
			}
		}
	}
	if err := Validate(c); err != nil {
		return err
	}
	isNew := c.ID == ""
	if isNew {
		c.ID = ulid.New()
	}
	cfg, _ := json.Marshal(c.Config)
	enc, err := n.box.Seal(secrets.New(string(cfg)), aad(c.ID))
	if err != nil {
		return err
	}
	if isNew {
		_, err = n.db.ExecContext(ctx, `INSERT INTO notification_channels(id, kind, name, config_enc, enabled, min_severity, event_pattern, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.Kind, strings.TrimSpace(c.Name), enc, c.Enabled, string(c.MinSeverity), c.Pattern, store.NowT())
	} else {
		_, err = n.db.ExecContext(ctx, `UPDATE notification_channels SET name = ?, config_enc = ?, enabled = ?, min_severity = ?, event_pattern = ? WHERE id = ?`,
			strings.TrimSpace(c.Name), enc, c.Enabled, string(c.MinSeverity), c.Pattern, c.ID)
	}
	if err != nil {
		return err
	}
	return n.audit.Record(ctx, by, "notify.save", "channel", c.ID, audit.OK, map[string]any{"kind": c.Kind, "name": c.Name})
}

// Delete removes a channel.
func (n *Notifier) Delete(ctx context.Context, by audit.Actor, id string) error {
	if _, err := n.db.ExecContext(ctx, `DELETE FROM notification_channels WHERE id = ?`, id); err != nil {
		return err
	}
	return n.audit.Record(ctx, by, "notify.delete", "channel", id, audit.OK, nil)
}

// Test sends a test event synchronously.
func (n *Notifier) Test(ctx context.Context, id string) error {
	c, err := n.Channel(ctx, id)
	if err != nil {
		return err
	}
	e := events.Event{ID: ulid.New(), TS: time.Now(), Type: "system.test", Severity: events.Info, Message: "Prueba de notificación de sqsolve"}
	err = n.send(ctx, c, e, 0)
	if err == nil {
		n.db.ExecContext(ctx, `UPDATE notification_channels SET last_ok_at = ?, last_error = '' WHERE id = ?`, store.NowT(), id)
	} else {
		n.db.ExecContext(ctx, `UPDATE notification_channels SET last_error = ? WHERE id = ?`, store.NowT()+" "+err.Error(), id)
	}
	return err
}
