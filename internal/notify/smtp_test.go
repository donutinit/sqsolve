// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"encoding/base64"
	"mime"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
)

// letter is one message a test SMTP server accepted.
type letter struct {
	auth string // decoded AUTH PLAIN response
	from string
	to   []string
	data string
}

// smtpServer is a minimal plain-text SMTP server: it advertises AUTH PLAIN
// when auth is set, rejects the verbs listed in reject with the given reply
// and keeps every message it accepts.
type smtpServer struct {
	l      net.Listener
	auth   bool
	reject map[string]string
	mu     sync.Mutex
	mails  []letter
}

func newSMTPServer(t *testing.T, auth bool, reject map[string]string) *smtpServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &smtpServer{l: l, auth: auth, reject: reject}
	var wg sync.WaitGroup
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); s.serve(c) }()
		}
	}()
	t.Cleanup(func() { l.Close(); wg.Wait() })
	return s
}

func (s *smtpServer) port() string { return strconv.Itoa(s.l.Addr().(*net.TCPAddr).Port) }

func (s *smtpServer) serve(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	tp := textproto.NewConn(c)
	tp.PrintfLine("220 test ESMTP")
	var m letter
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		if r, ok := s.reject[verb]; ok {
			tp.PrintfLine("%s", r)
			continue
		}
		switch verb {
		case "EHLO", "HELO":
			if s.auth {
				tp.PrintfLine("250-test")
				tp.PrintfLine("250 AUTH PLAIN")
			} else {
				tp.PrintfLine("250 test")
			}
		case "AUTH":
			b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			m.auth = string(b)
			tp.PrintfLine("235 2.7.0 accepted")
		case "MAIL":
			m.from = line
			tp.PrintfLine("250 OK")
		case "RCPT":
			m.to = append(m.to, line)
			tp.PrintfLine("250 OK")
		case "DATA":
			tp.PrintfLine("354 go ahead")
			b, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			m.data = string(b)
			s.mu.Lock()
			s.mails = append(s.mails, m)
			s.mu.Unlock()
			tp.PrintfLine("250 OK queued")
		case "QUIT":
			tp.PrintfLine("221 bye")
			return
		default:
			tp.PrintfLine("502 not implemented")
		}
	}
}

func (s *smtpServer) received() []letter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]letter(nil), s.mails...)
}

// smtpChannel saves an e-mail channel to 127.0.0.1 with cfg over the
// defaults (security none, one sender and two recipients).
func smtpChannel(t *testing.T, n *Notifier, port string, cfg map[string]string) *Channel {
	t.Helper()
	c := map[string]string{"host": "127.0.0.1", "port": port, "security": "none", "from": "sqsolve <sqsolve@example.com>", "to": "ana@example.com, bea@example.com"}
	for k, v := range cfg {
		c[k] = v
	}
	ch := &Channel{Kind: SMTP, Name: "mail", Enabled: true, MinSeverity: events.Warning, Pattern: "*", Config: c}
	if err := n.Save(context.Background(), audit.System, ch); err != nil {
		t.Fatal(err)
	}
	return ch
}

// A test e-mail goes from the sender to every recipient, with the
// credentials when a user is set, and its headers name the instance and the
// severity; the body ends with the time of the event.
func TestSMTPSendsTheMessage(t *testing.T) {
	ctx := context.Background()
	n, _ := newNotifier(t)
	n.Instance = func() string { return "nas-01" }
	srv := newSMTPServer(t, true, nil)
	ch := smtpChannel(t, n, srv.port(), map[string]string{"username": "mailer", "password": "pw-123456"})
	if err := n.Test(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	ms := srv.received()
	if len(ms) != 1 {
		t.Fatalf("%d messages", len(ms))
	}
	m := ms[0]
	if m.auth != "\x00mailer\x00pw-123456" || m.from != "MAIL FROM:<sqsolve@example.com>" || strings.Join(m.to, "|") != "RCPT TO:<ana@example.com>|RCPT TO:<bea@example.com>" {
		t.Fatalf("envelope %+v", m)
	}
	head, body, _ := strings.Cut(m.data, "\n\n")
	for _, want := range []string{
		`From: "sqsolve" <sqsolve@example.com>`,
		"To: ana@example.com, bea@example.com",
		"Subject: [nas-01] Info: sqsolve test notification",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
	} {
		if !strings.Contains(head, want+"\n") {
			t.Errorf("headers lack %q:\n%s", want, head)
		}
	}
	date, err := time.Parse(time.RFC1123Z, strings.TrimSpace(strings.TrimPrefix(body, "sqsolve test notification\n\n")))
	if !strings.HasPrefix(body, "sqsolve test notification\n\n") || err != nil || time.Since(date) > time.Minute {
		t.Fatalf("body %q: %v", body, err)
	}
	if got, _ := n.Channel(ctx, ch.ID); got.LastOK == nil || got.LastError != "" {
		t.Fatalf("channel %+v", got)
	}
}

// Events delivered by e-mail get a subject of at most 120 characters of the
// message, encoded when it is not ASCII, and no AUTH without a user.
func TestSMTPSubjectOfALongMessage(t *testing.T) {
	n, _ := newNotifier(t)
	srv := newSMTPServer(t, false, nil)
	smtpChannel(t, n, srv.port(), nil)
	run(t, n)
	msg := "Backup of Café failed: " + strings.Repeat("x", 150)
	n.Handle(events.Event{ID: "1", Type: events.BackupFailed, Severity: events.Error, LibraryID: "LIB1", Message: msg, TS: time.Now()})
	deadline := time.Now().Add(5 * time.Second)
	for len(srv.received()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no message")
		}
		time.Sleep(10 * time.Millisecond)
	}
	m := srv.received()[0]
	var subject string
	for _, line := range strings.Split(m.data, "\n") {
		if strings.HasPrefix(line, "Subject: ") {
			subject = strings.TrimPrefix(line, "Subject: ")
		}
	}
	dec, err := new(mime.WordDecoder).DecodeHeader(subject)
	want := "[sqsolve] Error: " + string([]rune(msg)[:120]) + "..."
	if err != nil || dec != want || !strings.HasPrefix(subject, "=?utf-8?q?") {
		t.Fatalf("subject %q decodes to %q: %v", subject, dec, err)
	}
	if m.auth != "" || !strings.Contains(m.data, msg+"\n") {
		t.Fatalf("message %+v", m)
	}
}

// Addresses are checked before connecting: an invalid sender is SQS-N010,
// an invalid recipient or none at all is SQS-N011.
func TestSMTPAddressErrors(t *testing.T) {
	ctx := context.Background()
	n, _ := newNotifier(t)
	srv := newSMTPServer(t, false, nil)
	for _, tc := range []struct {
		cfg  map[string]string
		want string
	}{
		{map[string]string{"from": "nobody"}, "SQS-N010 invalid sender: mail: missing '@' or angle-addr"},
		{map[string]string{"to": "ana@example.com, bea@"}, "SQS-N011 invalid recipient: bea@"},
		{map[string]string{"to": " , "}, "SQS-N011 no recipients"},
	} {
		ch := smtpChannel(t, n, srv.port(), tc.cfg)
		if err := n.Test(ctx, ch.ID); err == nil || err.Error() != tc.want {
			t.Errorf("%v: got %v, want %s", tc.cfg, err, tc.want)
		}
		if got, _ := n.Channel(ctx, ch.ID); !strings.HasSuffix(got.LastError, " "+tc.want) {
			t.Errorf("%v: stored %q", tc.cfg, got.LastError)
		}
	}
	if len(srv.received()) != 0 {
		t.Fatal("a message went out")
	}
}

// Each refusal of the server stops the delivery with the server's reply:
// STARTTLS not offered (SQS-N012), credentials, sender, recipient and data
// refused; a password quoted in a reply is redacted.
func TestSMTPServerRefusals(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		auth   bool
		reject map[string]string
		cfg    map[string]string
		want   string
	}{
		{"starttls", false, nil, map[string]string{"security": "starttls"}, `SQS-N012 STARTTLS: 502 "not implemented"`},
		{"default security is starttls", false, nil, map[string]string{"security": ""}, `SQS-N012 STARTTLS: 502 "not implemented"`},
		{"bad credentials", true, map[string]string{"AUTH": "535 5.7.8 pw-123456 is wrong"}, map[string]string{"username": "mailer", "password": "pw-123456"}, `535 "5.7.8 [redacted] is wrong"`},
		{"sender", false, map[string]string{"MAIL": "553 5.7.1 sender rejected"}, nil, `553 "5.7.1 sender rejected"`},
		{"recipient", false, map[string]string{"RCPT": "550 5.1.1 no such user"}, nil, `550 "5.1.1 no such user"`},
		{"data", false, map[string]string{"DATA": "554 5.3.4 no"}, nil, `554 "5.3.4 no"`},
	} {
		n, _ := newNotifier(t)
		srv := newSMTPServer(t, tc.auth, tc.reject)
		ch := smtpChannel(t, n, srv.port(), tc.cfg)
		if err := n.Test(ctx, ch.ID); err == nil || err.Error() != tc.want {
			t.Errorf("%s: got %v, want %s", tc.name, err, tc.want)
		}
		if len(srv.received()) != 0 {
			t.Errorf("%s: a message went out", tc.name)
		}
	}
}

// A server that cannot be reached, or one that does not speak TLS when TLS
// is asked for, is an error and nothing is sent.
func TestSMTPConnectionErrors(t *testing.T) {
	ctx := context.Background()
	n, _ := newNotifier(t)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	ch := smtpChannel(t, n, closed, nil)
	if err := n.Test(ctx, ch.ID); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("closed port: %v", err)
	}
	srv := newSMTPServer(t, false, nil)
	ch = smtpChannel(t, n, srv.port(), map[string]string{"security": "tls"})
	if err := n.Test(ctx, ch.ID); err == nil || !strings.HasPrefix(err.Error(), "tls: ") {
		t.Fatalf("plain server with tls: %v", err)
	}
	if len(srv.received()) != 0 {
		t.Fatal("a message went out")
	}
}
