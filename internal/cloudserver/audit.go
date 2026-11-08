package cloudserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	auditSucceeded = "succeeded"
	auditFailed    = "failed"
	auditDenied    = "denied"
	auditPartial   = "partial"
	auditNoChange  = "no_change"
)

type auditKey struct{}

// An audit event belongs to one authenticated action. Resource IDs and display
// labels come from resolved records, never from raw URLs or request payloads.
type auditEvent struct {
	clouddb.InsertOrgAuditEventParams
	recorded bool
}

func userAudit(ctx context.Context, user *clouddb.User, org *clouddb.Org, action string) *auditEvent {
	e := &auditEvent{InsertOrgAuditEventParams: clouddb.InsertOrgAuditEventParams{
		Action: action, ActorID: user.ID.String(), ActorName: user.Name,
		ActorEmail: user.Email, Source: "web",
	}}
	if ctx.Value(mcpPrincipalKey{}) != nil {
		e.Source = "mcp"
	}
	if org != nil {
		e.organization(org)
	}
	return e
}

func (e *auditEvent) organization(org *clouddb.Org) {
	e.OrgID = org.ID
	e.subject("Organization", org.ID, org.Name)
}

func (e *auditEvent) subject(kind string, id uuid.UUID, name string) {
	e.ResourceType = kind
	e.ResourceID = uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
	e.ResourceName = name
}

func (e *auditEvent) write(ctx context.Context, q *clouddb.Queries) error {
	if e == nil || e.OrgID == uuid.Nil || e.Action == "" {
		return errors.New("missing organization audit scope")
	}
	p := e.InsertOrgAuditEventParams
	p.Category, _, _ = strings.Cut(p.Action, ".")
	if p.Outcome == "" {
		p.Outcome = auditSucceeded
	}
	p.ActorName = auditText(p.ActorName, 160)
	p.ActorEmail = auditText(p.ActorEmail, 254)
	p.ResourceName = auditText(p.ResourceName, 256)
	p.Detail = auditText(p.Detail, 400)
	return q.InsertOrgAuditEvent(ctx, p)
}

func auditText(value string, limit int) string {
	value = strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	if chars := []rune(value); len(chars) > limit {
		return string(chars[:limit-1]) + "…"
	}
	return value
}

func requestAudit(ctx context.Context) *auditEvent {
	e, _ := ctx.Value(auditKey{}).(*auditEvent)
	return e
}

func auditSubject(ctx context.Context, kind string, id uuid.UUID, name string) {
	if e := requestAudit(ctx); e != nil {
		e.subject(kind, id, name)
	}
}

func auditResult(ctx context.Context, outcome, detail string) {
	if e := requestAudit(ctx); e != nil {
		e.Outcome, e.Detail = outcome, detail
	}
}

// auditedChange makes it impossible to commit an org mutation without its
// audit entry. The callback only performs database work and describes the
// committed change; outbound work and rendering happen outside the transaction.
func (s *Server) auditedChange(ctx context.Context, e *auditEvent, change func(*clouddb.Queries) error) error {
	if e == nil {
		return errors.New("missing organization audit event")
	}
	original := e.InsertOrgAuditEventParams
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := s.Q.WithTx(tx)
		if err := change(q); err != nil {
			return err
		}
		if err := e.write(ctx, q); err != nil {
			return fmt.Errorf("record audit event: %w", err)
		}
		return nil
	})
	if err == nil {
		e.recorded = true
	} else {
		// No change was committed. Let the handler report the failure without
		// carrying details that describe the intended change as completed.
		e.InsertOrgAuditEventParams = original
		e.Outcome, e.Detail = auditFailed, ""
	}
	return err
}

func (s *Server) recordAudit(ctx context.Context, e *auditEvent) error {
	if err := e.write(ctx, s.Q); err != nil {
		return err
	}
	e.recorded = true
	return nil
}

func (s *Server) recordObservedAudit(e *auditEvent) {
	// A client disconnect does not erase an action that Keel already handled.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.recordAudit(ctx, e); err != nil {
		slog.Error("write organization audit event", "org", e.OrgID, "action", e.Action, "err", err)
	}
}

// observeAudit covers reads, denied/invalid attempts, and other actions that
// do not commit a change. Record the outcome before releasing the response
// when possible; MCP responses stay in memory until the handler returns.
func (s *Server) observeAudit(w http.ResponseWriter, r *http.Request, e *auditEvent, handle http.HandlerFunc) {
	attempted := false
	observe := func(status int, fragmentFailed bool) {
		if e.recorded || attempted {
			return
		}
		attempted = true
		if e.Outcome == "" {
			e.Outcome = auditHTTPOutcome(status, w.Header())
			if fragmentFailed && e.Outcome == auditSucceeded {
				e.Outcome = auditFailed
			}
		}
		if e.Action == "deployment.manifest_view" && w.Header().Get("Content-Disposition") != "" {
			e.Action = "deployment.manifest_export"
		}
		s.recordObservedAudit(e)
	}
	mcp, isMCP := w.(*mcpResponse)
	if !isMCP {
		w = &auditResponse{ResponseWriter: w, beforeWrite: func(status int) { observe(status, false) }}
	}
	returned := false
	defer func() {
		switch {
		case !returned:
			e.Outcome = auditFailed
			observe(http.StatusInternalServerError, false)
		case isMCP:
			observe(mcp.status, mcp.failed)
		default:
			observe(http.StatusOK, false)
		}
	}()
	handle(w, r.WithContext(context.WithValue(r.Context(), auditKey{}, e)))
	returned = true
}

func auditHTTPOutcome(status int, header http.Header) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return auditDenied
	case status >= 400:
		return auditFailed
	case status >= 300:
		// Some forms use redirects and flash messages for validation errors.
		// Inspect the error flag only; messages may include submitted data.
		for _, c := range (&http.Response{Header: header}).Cookies() {
			if c.Name == "keel_flash" && strings.HasPrefix(c.Value, "error|") {
				return auditFailed
			}
		}
	}
	return auditSucceeded
}

type auditResponse struct {
	http.ResponseWriter
	beforeWrite func(int)
}

func (w *auditResponse) WriteHeader(status int) {
	if status >= 200 {
		w.observe(status)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponse) Write(p []byte) (int, error) {
	w.observe(http.StatusOK)
	return w.ResponseWriter.Write(p)
}

func (w *auditResponse) observe(status int) {
	if w.beforeWrite != nil {
		w.beforeWrite(status)
		w.beforeWrite = nil
	}
}

func (w *auditResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *auditResponse) Flush() {
	w.observe(http.StatusOK)
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func requireAuditRow(rows int64, err error) error {
	if err == nil && rows == 0 {
		return pgx.ErrNoRows
	}
	return err
}

func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
