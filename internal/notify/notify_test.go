package notify

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"
)

func TestCatalog(t *testing.T) {
	seen := map[Kind]bool{}
	for _, e := range Catalog {
		if seen[e.Kind] {
			t.Errorf("duplicate kind %s", e.Kind)
		}
		seen[e.Kind] = true
		if e.Label == "" || e.Description == "" || e.Category == "" {
			t.Errorf("%s: incomplete catalog entry", e.Kind)
		}
	}
	cats := Categories()
	if len(cats) != 4 || cats[0].Name != "Deployments" {
		t.Errorf("categories = %+v", cats)
	}
	if !Valid("run.failed") || Valid("nope") {
		t.Error("Valid is wrong")
	}
	if Label("run.failed") != "Deployment failed" {
		t.Error("Label is wrong")
	}
}

func sampleEvent() Event {
	return Event{
		Kind: RunFailed, OrgName: "Acme", Title: "prod → client-a failed (api)",
		Summary: "The run stopped with an error.",
		Error:   "step \"Deploy\" failed with exit code 1",
		Facts:   []Fact{{"Repository", "api"}, {"Failed step", "2 of 3 — Deploy"}},
		Inputs:  []Fact{{"ACTION", "destroy"}},
		LogTail: []string{"=> Step 2/3: Deploy", "error: <boom> & done"},
		Link:    "https://keel.test/orgs/acme/repos/api/runs/1", LinkLabel: "Open the run",
		SettingsLink: "https://keel.test/orgs/acme/notifications",
	}
}

func TestRender(t *testing.T) {
	ev := sampleEvent()
	if ev.Subject() != "[Keel] prod → client-a failed (api)" {
		t.Errorf("subject = %q", ev.Subject())
	}
	text := ev.Text()
	for _, want := range []string{"prod → client-a failed", "Error: step", "Failed step:", "ACTION = destroy", "error: <boom> & done", "Open the run: https://keel.test", "subscribed to \u201cDeployment failed\u201d events for Acme"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q:\n%s", want, text)
		}
	}
	html := ev.HTML()
	if strings.Contains(html, "<boom>") || !strings.Contains(html, "&lt;boom&gt; &amp; done") {
		t.Error("log line not HTML-escaped")
	}
	if !strings.Contains(html, ">Failed<") || !strings.Contains(html, "#b91c1c") {
		t.Error("failed badge missing")
	}
	for _, want := range []string{"<h1 ", "<h2 ", `<th scope="row"`, `role="presentation"`, "exit code 1", "Deployment failed", `<title>prod → client-a failed (api)</title>`} {
		if !strings.Contains(html, want) {
			t.Errorf("html missing %q", want)
		}
	}
	if !strings.Contains(html, `href="https://keel.test/orgs/acme/repos/api/runs/1"`) {
		t.Error("link missing")
	}
	if !strings.Contains(html, "Deploy-time inputs") || !strings.Contains(html, "ACTION=<b>destroy</b>") {
		t.Error("inputs missing from HTML")
	}
	if strings.Contains(html, "AI insight") {
		t.Error("insight section rendered without an insight")
	}

	ev.Insight = "## What happened\nThe **Deploy** step failed.\n\n## What to do next\n1. Check `GCP_PROJECT`.\n<script>alert(1)</script>"
	html = ev.HTML()
	for _, want := range []string{"AI insight", "What happened", "<strong>Deploy</strong>", "<ol", "<code", "Check it against the log"} {
		if !strings.Contains(html, want) {
			t.Errorf("insight HTML missing %q", want)
		}
	}
	if strings.Contains(html, "<script>") {
		t.Error("raw HTML from the model must not be inlined")
	}
	if text := ev.Text(); !strings.Contains(text, "AI insight\n----------\n## What happened") {
		t.Errorf("insight missing from text:\n%s", text)
	}
	ev.Insight, ev.InsightNote = "", "could not be generated: timeout"
	if html = ev.HTML(); !strings.Contains(html, "could not be generated: timeout") || !strings.Contains(html, "AI insight") {
		t.Error("insight note missing")
	}
}

func TestValidate(t *testing.T) {
	errs := SMTP{Port: 0, Encryption: "x", From: "nope"}.Validate()
	for _, k := range []string{"host", "port", "encryption", "from_address"} {
		if errs[k] == "" {
			t.Errorf("expected error for %s", k)
		}
	}
	if errs := (SMTP{Host: "smtp.x", Port: 587, Encryption: EncryptionStartTLS, From: "a@b.co"}).Validate(); len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

// fakeSMTP is the smallest server that satisfies the client: EHLO, AUTH
// PLAIN, MAIL, RCPT, DATA, QUIT. It records the message it received.
type fakeSMTP struct {
	ln      net.Listener
	auth    string
	from    string
	rcpts   []string
	message string
	done    chan struct{}
}

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, done: make(chan struct{})}
	go func() {
		defer close(f.done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		rd := bufio.NewReader(conn)
		w := func(s string) { conn.Write([]byte(s + "\r\n")) }
		w("220 fake ESMTP")
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			cmd := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				w("250-fake")
				w("250-AUTH PLAIN LOGIN")
				w("250 8BITMIME")
			case strings.HasPrefix(cmd, "AUTH PLAIN"):
				f.auth = strings.TrimPrefix(line, "AUTH PLAIN ")
				w("235 ok")
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				f.from = line[len("MAIL FROM:"):]
				w("250 ok")
			case strings.HasPrefix(cmd, "RCPT TO:"):
				f.rcpts = append(f.rcpts, line[len("RCPT TO:"):])
				w("250 ok")
			case cmd == "DATA":
				w("354 go")
				var b strings.Builder
				for {
					l, err := rd.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				f.message = b.String()
				w("250 queued")
			case cmd == "QUIT":
				w("221 bye")
				return
			default:
				w("250 ok")
			}
		}
	}()
	return f
}

func TestSend(t *testing.T) {
	f := startFakeSMTP(t)
	defer f.ln.Close()
	_, portStr, _ := net.SplitHostPort(f.ln.Addr().String())
	port := 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}
	cfg := SMTP{Host: "127.0.0.1", Port: port, Username: "u", Password: "p", Encryption: EncryptionNone, From: "keel@x.test", FromName: "Keel Cloud"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ev := sampleEvent()
	if err := cfg.Send(ctx, []string{"a@x.test", "b@x.test"}, ev.Subject(), ev.Text(), ev.HTML()); err != nil {
		t.Fatal(err)
	}
	<-f.done
	if creds, _ := base64.StdEncoding.DecodeString(f.auth); string(creds) != "\x00u\x00p" {
		t.Errorf("auth = %q", f.auth)
	}
	// net/smtp appends BODY=8BITMIME when the server advertises it.
	if !strings.HasPrefix(f.from, "<keel@x.test>") || len(f.rcpts) != 2 {
		t.Errorf("from=%q rcpts=%v", f.from, f.rcpts)
	}
	for _, want := range []string{"From: \"Keel Cloud\" <keel@x.test>", "To: a@x.test, b@x.test", "Subject: ", "multipart/alternative", "Content-Type: text/plain", "Content-Type: text/html", "base64"} {
		if !strings.Contains(f.message, want) {
			t.Errorf("message missing %q:\n%s", want, f.message)
		}
	}
	// STARTTLS must be refused when the server does not offer it.
	f2 := startFakeSMTP(t)
	defer f2.ln.Close()
	_, portStr, _ = net.SplitHostPort(f2.ln.Addr().String())
	port = 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}
	cfg.Port, cfg.Encryption = port, EncryptionStartTLS
	if err := cfg.Send(ctx, []string{"a@x.test"}, "s", "t", "h"); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("expected a STARTTLS error, got %v", err)
	}
}
