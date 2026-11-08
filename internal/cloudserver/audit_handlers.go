package cloudserver

import (
	"math"
	"net/http"
	"net/url"
	"strconv"

	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/UcGeorge/keel/internal/web"
)

var auditActionLabels = map[string]string{
	"organization.create":           "Create organization",
	"organization.rename":           "Rename organization",
	"organization.delete":           "Delete organization",
	"organization.settings_view":    "View organization settings",
	"organization.audit_view":       "View audit log",
	"membership.list":               "View members",
	"membership.invite":             "Invite member",
	"membership.invite_view":        "View invitation",
	"membership.accept":             "Accept invitation",
	"membership.update":             "Change member permissions",
	"membership.remove":             "Remove member",
	"membership.revoke":             "Revoke invitation",
	"repository.list":               "View repositories",
	"repository.choices":            "View repository connection options",
	"repository.connect":            "Connect repository",
	"repository.view":               "View repository",
	"repository.sync":               "Sync repository",
	"repository.settings_view":      "View repository settings",
	"repository.update":             "Update repository settings",
	"repository.disconnect":         "Disconnect repository",
	"deployment.view":               "View deployment",
	"deployment.manifest_view":      "View variable manifest",
	"deployment.manifest_export":    "Export variable manifest",
	"deployment.target_create":      "Create target",
	"deployment.target_view":        "View target",
	"deployment.target_update":      "Update target",
	"deployment.target_delete":      "Delete target",
	"deployment.values_save":        "Save target values",
	"run.list":                      "View run history",
	"run.view":                      "View run",
	"run.logs_view":                 "Read run logs",
	"run.deploy":                    "Start deployment",
	"run.cancel":                    "Request run cancellation",
	"run.insight":                   "Generate run insight",
	"notification.view":             "View notifications",
	"notification.smtp_save":        "Save mail server settings",
	"notification.smtp_test":        "Send test email",
	"notification.recipient_create": "Add notification recipient",
	"notification.recipient_update": "Update notification recipient",
	"notification.recipient_delete": "Remove notification recipient",
	"ai.view":                       "View AI settings",
	"ai.save":                       "Save AI settings",
	"ai.models":                     "List AI provider models",
	"ai.test":                       "Test AI provider",
	"ai.delete":                     "Remove AI settings",
}

var (
	auditCategories = []web.AuditOption{
		{Key: "organization", Label: "Organization"}, {Key: "membership", Label: "Members and invitations"},
		{Key: "repository", Label: "Repositories"}, {Key: "deployment", Label: "Deployments and targets"},
		{Key: "run", Label: "Runs"}, {Key: "notification", Label: "Notifications"}, {Key: "ai", Label: "AI settings"},
	}
	auditSources = []web.AuditOption{
		{Key: "web", Label: "Browser"}, {Key: "mcp", Label: "MCP"}, {Key: "github", Label: "GitHub"},
	}
	auditOutcomes = []web.AuditOption{
		{Key: auditSucceeded, Label: "Succeeded"}, {Key: auditFailed, Label: "Failed"}, {Key: auditDenied, Label: "Denied"},
		{Key: auditPartial, Label: "Partially applied"}, {Key: auditNoChange, Label: "No change"},
	}
)

func auditLabel(options []web.AuditOption, key string) string {
	for _, o := range options {
		if o.Key == key {
			return o.Label
		}
	}
	return ""
}

func auditFilter(options []web.AuditOption, key string) string {
	if auditLabel(options, key) != "" {
		return key
	}
	return ""
}

func (s *Server) handleAuditLog(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	w.Header().Set("Cache-Control", "no-store")
	if !oc.isAdmin() {
		s.errorPage(w, r, oc.Sess, http.StatusForbidden, "Only owners and admins can view the audit log")
		return
	}
	ctx := r.Context()
	query := r.URL.Query()
	before, _ := strconv.ParseInt(query.Get("before"), 10, 64)
	if before <= 0 {
		before = math.MaxInt64
	}
	const pageSize = 50
	page := web.PageAuditLog{
		URL:        oc.urlBase() + "/audit-log",
		Search:     auditText(query.Get("q"), 120),
		Category:   auditFilter(auditCategories, query.Get("category")),
		Source:     auditFilter(auditSources, query.Get("source")),
		Outcome:    auditFilter(auditOutcomes, query.Get("outcome")),
		Categories: auditCategories, Sources: auditSources, Outcomes: auditOutcomes,
	}
	page.Filtered = page.Search != "" || page.Category != "" || page.Source != "" || page.Outcome != "" || before < math.MaxInt64
	rows, err := s.Q.ListOrgAuditEvents(ctx, clouddb.ListOrgAuditEventsParams{
		OrgID: oc.Org.ID, BeforeID: before, Search: page.Search, Category: page.Category,
		Source: page.Source, Outcome: page.Outcome, PageLimit: pageSize + 1,
	})
	if err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not load the audit log")
		return
	}
	// Record access before exposing the audit data. This page reflects the
	// rows as of the query above, so opening it doesn't add to its own results.
	if err := s.recordAudit(ctx, requestAudit(ctx)); err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusServiceUnavailable, "Could not record access to the audit log")
		return
	}
	if len(rows) > pageSize {
		rows = rows[:pageSize]
		next := url.Values{"before": {strconv.FormatInt(rows[len(rows)-1].ID, 10)}}
		for key, value := range map[string]string{"q": page.Search, "category": page.Category, "source": page.Source, "outcome": page.Outcome} {
			if value != "" {
				next.Set(key, value)
			}
		}
		page.OlderURL = page.URL + "?" + next.Encode()
	}
	for _, row := range rows {
		item := web.AuditRowVM{
			ID: row.ID, OccurredAt: row.OccurredAt.UTC(),
			ActorID: row.ActorID, ActorName: row.ActorName, ActorEmail: row.ActorEmail,
			Action: auditActionLabels[row.Action], Source: auditLabel(auditSources, row.Source),
			Outcome: row.Outcome, OutcomeLabel: auditLabel(auditOutcomes, row.Outcome),
			ResourceType: row.ResourceType, ResourceName: row.ResourceName, Detail: row.Detail,
		}
		if item.Action == "" {
			item.Action = row.Action
		}
		if row.ResourceID.Valid {
			item.ResourceID = row.ResourceID.UUID.String()
		}
		page.Rows = append(page.Rows, item)
	}
	page.Base = s.base(w, r, oc.Sess, oc, "Audit log")
	s.Renderer.Render(w, http.StatusOK, "cloud/audit_log.html", page)
}
