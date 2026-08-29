package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Encryption modes for the SMTP connection.
const (
	EncryptionStartTLS = "starttls" // plain connection upgraded with STARTTLS (port 587)
	EncryptionTLS      = "tls"      // implicit TLS from the first byte (port 465)
	EncryptionNone     = "none"     // no encryption (local relays only)
)

// SMTP is a mail server configuration.
type SMTP struct {
	Host       string
	Port       int
	Username   string
	Password   string
	Encryption string
	From       string
	FromName   string
}

// Validate checks the configuration for obvious problems and returns a
// per-field message map (empty when valid).
func (c SMTP) Validate() map[string]string {
	errs := map[string]string{}
	if strings.TrimSpace(c.Host) == "" {
		errs["host"] = "The server host is required."
	}
	if c.Port <= 0 || c.Port > 65535 {
		errs["port"] = "Enter a port between 1 and 65535 (587 for STARTTLS, 465 for TLS)."
	}
	switch c.Encryption {
	case EncryptionStartTLS, EncryptionTLS, EncryptionNone:
	default:
		errs["encryption"] = "Pick an encryption mode."
	}
	if _, err := mail.ParseAddress(c.From); err != nil || strings.TrimSpace(c.From) == "" {
		errs["from_address"] = "Enter the address emails are sent from, e.g. keel@company.com."
	}
	return errs
}

// Send delivers one message to the recipients. The context bounds the
// whole exchange.
func (c SMTP) Send(ctx context.Context, to []string, subject, text, htmlBody string) error {
	if len(to) == 0 {
		return errors.New("no recipients")
	}
	deadline := 30 * time.Second
	if d, ok := ctx.Deadline(); ok {
		deadline = time.Until(d)
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	dialer := &net.Dialer{Timeout: deadline}

	var conn net.Conn
	var err error
	tlsCfg := &tls.Config{ServerName: c.Host}
	if c.Encryption == EncryptionTLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(deadline))

	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp handshake with %s: %w", addr, err)
	}
	defer client.Close()
	if err := client.Hello("keel"); err != nil {
		return fmt.Errorf("smtp EHLO: %w", err)
	}
	if c.Encryption == EncryptionStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%s does not offer STARTTLS — pick TLS (port 465) or none", addr)
		}
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if c.Username != "" {
		if err := client.Auth(c.auth(client)); err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		}
	}
	from, _ := mail.ParseAddress(c.From)
	fromAddr := c.From
	if from != nil {
		fromAddr = from.Address
	}
	if err := client.Mail(fromAddr); err != nil {
		return fmt.Errorf("MAIL FROM rejected: %w", err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("recipient %s rejected: %w", rcpt, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(c.message(to, subject, text, htmlBody)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("message rejected: %w", err)
	}
	return client.Quit()
}

// auth picks a mechanism the server advertises. PLAIN is preferred; LOGIN
// covers Microsoft 365 and older relays; CRAM-MD5 is the last resort.
func (c SMTP) auth(client *smtp.Client) smtp.Auth {
	_, mechs := client.Extension("AUTH")
	switch {
	case strings.Contains(mechs, "PLAIN"):
		return plainAuth{username: c.Username, password: c.Password}
	case strings.Contains(mechs, "LOGIN"):
		return loginAuth{username: c.Username, password: c.Password}
	case strings.Contains(mechs, "CRAM-MD5"):
		return smtp.CRAMMD5Auth(c.Username, c.Password)
	default:
		return plainAuth{username: c.Username, password: c.Password}
	}
}

// plainAuth is PLAIN without net/smtp's refusal to run over an
// unencrypted connection: the encryption mode is the administrator's
// explicit choice, and "none" exists for local relays.
type plainAuth struct{ username, password string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.username + "\x00" + a.password), nil
}

func (a plainAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge")
	}
	return nil, nil
}

// loginAuth implements the LOGIN mechanism.
type loginAuth struct{ username, password string }

func (a loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) { return "LOGIN", nil, nil }

func (a loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:":
		return []byte(a.username), nil
	case "password:":
		return []byte(a.password), nil
	default:
		return nil, fmt.Errorf("unexpected LOGIN challenge %q", fromServer)
	}
}

// message assembles a multipart/alternative MIME message.
func (c SMTP) message(to []string, subject, text, htmlBody string) []byte {
	boundary := "keel-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	from := (&mail.Address{Name: c.FromName, Address: c.From}).String()
	if parsed, err := mail.ParseAddress(c.From); err == nil {
		from = (&mail.Address{Name: c.FromName, Address: parsed.Address}).String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@keel>\r\n", boundary)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("X-Mailer: Keel Cloud\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	part := func(ctype, body string) {
		fmt.Fprintf(&b, "--%s\r\n", boundary)
		fmt.Fprintf(&b, "Content-Type: %s; charset=utf-8\r\n", ctype)
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		enc := base64.StdEncoding.EncodeToString([]byte(body))
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n")
	}
	part("text/plain", text)
	part("text/html", htmlBody)
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}
