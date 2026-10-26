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
	"runtime/debug"
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
	retries  []time.Duration

	// window groups repeated events (docs 11 §13); sweepEvery is how often
	// expired windows are closed with their summary.
	window     time.Duration
	sweepEvery time.Duration
	lookupIP   func(ctx context.Context, host string) ([]net.IPAddr, error)

	mu      sync.Mutex
	windows map[string]*window
	runDone chan struct{} // closed when Run returns

	// Deliveries run on base; Flush cancels it when its time is up.
	base     context.Context
	stopBase context.CancelFunc
	inflight sync.WaitGroup
	// flushing is closed when Flush starts: deliveries waiting for a
	// retry make one last attempt at once instead of the backoff.
	flushing  chan struct{}
	flushOnce sync.Once
}

// window is the grouping state of one channel, event type, library and
// discriminator.
type window struct {
	channel      string
	opened       time.Time
	severity     events.Severity // highest severity delivered in the window
	held         int
	heldSeverity events.Severity // highest severity among the held events
	last         events.Event    // latest held event, for the summary
}

// New creates a notifier.
func New(db *store.DB, box *secrets.Box, a *audit.Log, log *slog.Logger) *Notifier {
	base, stop := context.WithCancel(context.Background())
	return &Notifier{
		db: db, box: box, audit: a, log: log,
		client:     &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		queue:      make(chan events.Event, 256),
		retries:    []time.Duration{10 * time.Second, time.Minute, 10 * time.Minute, time.Hour},
		window:     30 * time.Minute,
		sweepEvery: time.Minute,
		lookupIP:   net.DefaultResolver.LookupIPAddr,
		windows:    map[string]*window{},
		base:       base,
		stopBase:   stop,
		flushing:   make(chan struct{}),
		Instance:   func() string { return "sqsolve" },
		BaseURL:    func() string { return "" },
	}
}

// Handle queues an event without blocking.
func (n *Notifier) Handle(e events.Event) {
	select {
	case n.queue <- e:
	default:
		n.log.Warn("notification queue full; dropping an event", "type", e.Type)
	}
}

// Run delivers queued events and closes expired grouping windows until ctx
// ends. A panic while handling one event is logged and the loop goes on.
func (n *Notifier) Run(ctx context.Context) {
	done := make(chan struct{})
	n.mu.Lock()
	n.runDone = done
	n.mu.Unlock()
	defer close(done)
	t := time.NewTicker(n.sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-n.queue:
			n.safely("event", func() { n.dispatch(ctx, e) })
		case <-t.C:
			n.safely("summary", func() { n.sweep(ctx, false) })
		}
	}
}

// Flush is the last step of a graceful shutdown, after the jobs stopped and
// after the context given to Run was cancelled. It delivers the events still
// queued (what the jobs emitted while stopping) and the summaries of open
// grouping windows, then waits for the deliveries in flight until ctx ends.
// A delivery waiting for a retry (up to 1 h) makes one last attempt at once
// and gives up, so a failing channel does not hold the stop.
func (n *Notifier) Flush(ctx context.Context) {
	n.flushOnce.Do(func() { close(n.flushing) })
	n.mu.Lock()
	done := n.runDone
	n.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			n.stopBase()
			return
		}
	}
	for drained := false; !drained; {
		select {
		case e := <-n.queue:
			n.safely("event", func() { n.dispatch(ctx, e) })
		default:
			drained = true
		}
	}
	n.safely("summary", func() { n.sweep(ctx, true) })
	idle := make(chan struct{})
	go func() { n.inflight.Wait(); close(idle) }()
	select {
	case <-idle:
	case <-ctx.Done():
		n.log.Warn("stopping before every notification was delivered")
	}
	n.stopBase()
}

func (n *Notifier) safely(what string, fn func()) {
	defer func() {
		if p := recover(); p != nil {
			n.log.Error("panic in the notifier", "while", what, "panic", p, "stack", string(debug.Stack()))
		}
	}()
	fn()
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

// discriminators name the event data that tell apart events of one type
// and library, mainly system events (no library): the job type of a failed
// job, the volume of a space warning, the account and address of a lock or
// a burst of failed logins, the certificate about to expire.
var discriminators = []string{"job_type", "volume", "kind", "loop", "username", "ip", "cert"}

// closes maps an event type to the type whose window it ends: once
// PostgreSQL answers again, a new outage is news, not a repeat.
var closes = map[string]string{events.PostgresUp: events.PostgresDown}

func groupKey(channel string, e events.Event) string {
	key := channel + "|" + e.Type + "|" + e.LibraryID
	for _, d := range discriminators {
		if v, ok := e.Data[d]; ok {
			key += "|" + d + "=" + fmt.Sprint(v)
		}
	}
	return key
}

// dispatch sends e to every matching channel. Within a window, events with
// the same key are held, except one more severe than any delivered so far
// in the window, which always goes out.
func (n *Notifier) dispatch(ctx context.Context, e events.Event) {
	chs, err := n.Channels(ctx)
	if err != nil {
		n.log.Error("could not read the notification channels", "err", err)
		return
	}
	now := time.Now()
	for _, ch := range chs {
		if !ch.Enabled {
			continue
		}
		// Deliveries of this event on ch, sent one after the other in
		// this order: a summary always goes out before the event that
		// follows it.
		var out []delivery
		// The window an event ends is closed even on a channel that does
		// not send that event (postgres.up is info): its held events, if
		// any, go out as the summary, before the event.
		if typ, ok := closes[e.Type]; ok {
			if d, ok := n.takeWindow(groupKey(ch.ID, events.Event{Type: typ, LibraryID: e.LibraryID, Data: e.Data})); ok {
				out = append(out, d)
			}
		}
		if e.Severity.Rank() < ch.MinSeverity.Rank() || !matches(ch.Pattern, e.Type) {
			n.startSeq(ch, out...)
			continue
		}
		key := groupKey(ch.ID, e)
		send := false
		n.mu.Lock()
		w := n.windows[key]
		if w != nil && now.Sub(w.opened) >= n.window {
			// Expired before the sweep saw it: close it first.
			delete(n.windows, key)
			if w.held > 0 {
				out = append(out, delivery{summaryOf(w), w.held})
			}
			w = nil
		}
		switch {
		case w == nil:
			n.windows[key] = &window{channel: ch.ID, opened: now, severity: e.Severity}
			send = true
		case e.Severity.Rank() > w.severity.Rank():
			w.severity = e.Severity
			send = true
		default:
			w.held++
			w.last = e
			if w.held == 1 || e.Severity.Rank() > w.heldSeverity.Rank() {
				w.heldSeverity = e.Severity
			}
		}
		n.mu.Unlock()
		if send {
			out = append(out, delivery{e, 0})
		}
		n.startSeq(ch, out...)
	}
}

// takeWindow ends the window of key and returns its summary when it held
// events.
func (n *Notifier) takeWindow(key string) (delivery, bool) {
	n.mu.Lock()
	w := n.windows[key]
	delete(n.windows, key)
	n.mu.Unlock()
	if w == nil || w.held == 0 {
		return delivery{}, false
	}
	return delivery{summaryOf(w), w.held}, true
}

// summaryOf is the event sent when a window closes with held events: the
// latest of them, with the highest severity held, saying how many were held.
func summaryOf(w *window) events.Event {
	e := w.last
	e.Severity = w.heldSeverity
	e.Message = fmt.Sprintf("%d more like this. Latest: %s", w.held, w.last.Message)
	return e
}

// sweep closes expired windows (all of them with all) and sends their
// summaries.
func (n *Notifier) sweep(ctx context.Context, all bool) {
	type due struct {
		channel string
		e       events.Event
		held    int
	}
	var out []due
	now := time.Now()
	n.mu.Lock()
	for key, w := range n.windows {
		if !all && now.Sub(w.opened) < n.window {
			continue
		}
		delete(n.windows, key)
		if w.held > 0 {
			out = append(out, due{w.channel, summaryOf(w), w.held})
		}
	}
	n.mu.Unlock()
	if len(out) == 0 {
		return
	}
	chs, err := n.Channels(ctx)
	if err != nil {
		n.log.Error("could not read the notification channels", "err", err)
		return
	}
	byID := map[string]*Channel{}
	for _, ch := range chs {
		byID[ch.ID] = ch
	}
	for _, d := range out {
		if ch := byID[d.channel]; ch != nil && ch.Enabled {
			n.start(ch, d.e, d.held)
		}
	}
}

// start delivers in the background; a panic there is logged, never fatal.
func (n *Notifier) start(ch *Channel, e events.Event, extra int) {
	n.startSeq(ch, delivery{e, extra})
}

// delivery is one notification to send: an event, or a summary with the
// number of events it stands for.
type delivery struct {
	e     events.Event
	extra int
}

// startSeq sends ds on ch in the background, one after the other, so they
// arrive in that order.
func (n *Notifier) startSeq(ch *Channel, ds ...delivery) {
	if len(ds) == 0 {
		return
	}
	n.inflight.Add(1)
	go func() {
		defer n.inflight.Done()
		for _, d := range ds {
			n.safely("delivery", func() { n.deliver(n.base, ch, d.e, d.extra) })
		}
	}()
}

func (n *Notifier) deliver(ctx context.Context, ch *Channel, e events.Event, extra int) {
	var err error
	last := false
	for attempt := 0; ; attempt++ {
		err = n.send(ctx, ch, e, extra)
		if err == nil {
			n.db.ExecContext(context.WithoutCancel(ctx), `UPDATE notification_channels SET last_ok_at = ?, last_error = '' WHERE id = ?`, store.NowT(), ch.ID)
			return
		}
		if last || attempt >= len(n.retries) {
			break
		}
		select {
		case <-ctx.Done():
		case <-n.flushing:
			// Stopping: one last attempt now instead of the backoff, so a
			// failing channel does not hold the stop.
			last = true
			continue
		case <-time.After(n.retries[attempt]):
			continue
		}
		break
	}
	msg := shortError(ch, err)
	n.log.Warn("could not notify", "channel", ch.Name, "err", msg)
	n.db.ExecContext(context.WithoutCancel(ctx), `UPDATE notification_channels SET last_error = ? WHERE id = ?`, store.NowT()+" "+msg, ch.ID)
}

// shortError is what is stored and shown about a failed delivery: the cause
// and the HTTP status, never the URL, which carries the tokens of Slack,
// Discord or ntfy, nor a secret field.
func shortError(ch *Channel, err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := err.Error()
	hide := []string{ch.Config["url"]}
	if u, perr := url.Parse(ch.Config["url"]); perr == nil {
		hide = append(hide, u.RawQuery)
		if len(u.Path) > 1 {
			hide = append(hide, u.Path)
		}
	}
	for k := range SecretFields {
		hide = append(hide, ch.Config[k])
	}
	for _, v := range hide {
		if len(v) >= 4 {
			msg = strings.ReplaceAll(msg, v, "[redacted]")
		}
	}
	return truncate(msg, 300)
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
			p.Links["job"] = base + "/jobs/" + e.JobID
		}
		if e.LibraryID != "" {
			p.Links["library"] = base + "/libraries/" + e.LibraryID
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
	switch ch.Kind {
	case Webhook:
		body, _ := json.Marshal(n.payload(e, extra))
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.Config["url"], bytes.NewReader(body))
		if err != nil {
			return errBadURL
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
			return errBadURL
		}
		req.Header.Set("Title", "["+n.Instance()+"] "+e.Severity.Label())
		req.Header.Set("Tags", strings.ReplaceAll(e.Type, ".", "_"))
		prio := map[events.Severity]string{events.Info: "3", events.Warning: "4", events.Error: "5", events.Critical: "5"}[e.Severity]
		req.Header.Set("Priority", prio)
		if t := ch.Config["token"]; t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
		if base := n.BaseURL(); base != "" && e.JobID != "" {
			req.Header.Set("Click", base+"/jobs/"+e.JobID)
		}
		return n.do(req)
	case SMTP:
		return sendMail(ctx, ch.Config, fmt.Sprintf("[%s] %s: %s", n.Instance(), e.Severity.Label(), truncate(e.Message, 120)), msg+"\n\n"+e.TS.Format(time.RFC1123Z)+"\n")
	}
	return fmt.Errorf("SQS-N001 unknown channel kind: %s", ch.Kind)
}

// errBadURL replaces the parse error, which quotes the whole URL.
var errBadURL = errors.New("SQS-N041 the destination URL is not valid")

// do posts the request. Transport errors keep their cause but not the URL
// that *url.Error quotes.
func (n *Notifier) do(req *http.Request) error {
	resp, err := n.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("SQS-N040 could not reach the destination: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("SQS-N002 the destination responded %s", resp.Status)
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
		return fmt.Errorf("SQS-N010 invalid sender: %w", err)
	}
	var to []string
	for _, a := range strings.Split(c["to"], ",") {
		if a = strings.TrimSpace(a); a != "" {
			addr, err := mail.ParseAddress(a)
			if err != nil {
				return fmt.Errorf("SQS-N011 invalid recipient: %s", a)
			}
			to = append(to, addr.Address)
		}
	}
	if len(to) == 0 {
		return errors.New("SQS-N011 no recipients")
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
	return nil, errors.New("SQS-N020 channel not found")
}

// Validate checks a channel's configuration.
func Validate(c *Channel) error {
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 80 {
		return errors.New("SQS-N021 the channel needs a name")
	}
	switch c.MinSeverity {
	case events.Info, events.Warning, events.Error, events.Critical:
	default:
		return errors.New("SQS-N022 invalid severity")
	}
	if c.Pattern == "" {
		c.Pattern = "*"
	}
	switch c.Kind {
	case Webhook, Ntfy:
		u, err := url.Parse(c.Config["url"])
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("SQS-N023 invalid URL (use http:// or https://)")
		}
	case SMTP:
		if c.Config["host"] == "" || c.Config["from"] == "" || c.Config["to"] == "" {
			return errors.New("SQS-N024 email needs a server, sender and recipients")
		}
		if p := c.Config["port"]; p != "" {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				return errors.New("SQS-N025 invalid port")
			}
		}
		switch c.Config["security"] {
		case "", "starttls", "tls", "none":
		default:
			return errors.New("SQS-N026 invalid security setting")
		}
	default:
		return errors.New("SQS-N001 unknown channel kind")
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
	err = n.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		if isNew {
			_, err = tx.ExecContext(ctx, `INSERT INTO notification_channels(id, kind, name, config_enc, enabled, min_severity, event_pattern, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				c.ID, c.Kind, strings.TrimSpace(c.Name), enc, c.Enabled, string(c.MinSeverity), c.Pattern, store.NowT())
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE notification_channels SET name = ?, config_enc = ?, enabled = ?, min_severity = ?, event_pattern = ? WHERE id = ?`,
				strings.TrimSpace(c.Name), enc, c.Enabled, string(c.MinSeverity), c.Pattern, c.ID)
		}
		if err != nil {
			return err
		}
		return n.audit.RecordTx(ctx, tx, by, "notify.save", "channel", c.ID, audit.OK, map[string]any{"kind": c.Kind, "name": c.Name})
	})
	if err != nil && isNew {
		c.ID = ""
	}
	return err
}

// Delete removes a channel, in one transaction with its audit entry.
func (n *Notifier) Delete(ctx context.Context, by audit.Actor, id string) error {
	return n.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM notification_channels WHERE id = ?`, id); err != nil {
			return err
		}
		return n.audit.RecordTx(ctx, tx, by, "notify.delete", "channel", id, audit.OK, nil)
	})
}

// Test sends a test event synchronously.
func (n *Notifier) Test(ctx context.Context, id string) error {
	c, err := n.Channel(ctx, id)
	if err != nil {
		return err
	}
	e := events.Event{ID: ulid.New(), TS: time.Now(), Type: "system.test", Severity: events.Info, Message: "sqsolve test notification"}
	if err := n.send(ctx, c, e, 0); err != nil {
		msg := shortError(c, err)
		n.db.ExecContext(ctx, `UPDATE notification_channels SET last_error = ? WHERE id = ?`, store.NowT()+" "+msg, id)
		return errors.New(msg)
	}
	n.db.ExecContext(ctx, `UPDATE notification_channels SET last_ok_at = ?, last_error = '' WHERE id = ?`, store.NowT(), id)
	return nil
}

// AddressWarning returns a warning, not an error (the channel is saved),
// when a webhook or ntfy URL points to a loopback, private, link-local or
// unspecified address (docs 08 T-16): sqsolve would then post events inside
// the network it runs in. A name that does not resolve gives no warning.
func (n *Notifier) AddressWarning(ctx context.Context, c *Channel) string {
	if c.Kind != Webhook && c.Kind != Ntfy {
		return ""
	}
	u, err := url.Parse(c.Config["url"])
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := u.Hostname()
	var ips []net.IP
	lower := strings.ToLower(host)
	switch {
	case net.ParseIP(host) != nil:
		ips = []net.IP{net.ParseIP(host)}
	case lower == "localhost" || strings.HasSuffix(lower, ".localhost"):
		ips = []net.IP{net.IPv4(127, 0, 0, 1)}
	default:
		lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		addrs, err := n.lookupIP(lctx, host)
		if err != nil {
			return ""
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Sprintf("SQS-N042 %s points to a private, loopback or link-local address (%s): sqsolve will send events inside its own network. Check that this is the destination you want.", host, ip)
		}
	}
	return ""
}
