package cloudserver

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/UcGeorge/keel/internal/notify"
	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/UcGeorge/keel/internal/web"
	"github.com/google/uuid"
)

// logTailLines is how many log lines a failed-run email carries.
const logTailLines = 30

// deliveriesKept bounds the per-organization delivery log.
const deliveriesKept = 200

// insightFunc produces the AI insight for a failed run on demand: the
// stored one, or a freshly generated (and stored) one. It returns
// errInsightUnavailable when AI insights are not set up.
type insightFunc func(ctx context.Context) (string, error)

// publish announces an event: recipients subscribed to its kind get an
// email, sent in the background so the caller never waits on SMTP.
func (s *Server) publish(orgID uuid.UUID, ev notify.Event) {
	go s.deliver(orgID, ev, nil)
}

// publishWithInsight is publish for failed runs: recipients who asked for
// the AI insight get it generated and embedded in their email.
func (s *Server) publishWithInsight(orgID uuid.UUID, ev notify.Event, insight insightFunc) {
	go s.deliver(orgID, ev, insight)
}

func (s *Server) deliver(orgID uuid.UUID, ev notify.Event, insight insightFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	recipients, err := s.Q.ListRecipientsForEvent(ctx, clouddb.ListRecipientsForEventParams{OrgID: orgID, Column2: string(ev.Kind)})
	if err != nil || len(recipients) == 0 {
		return
	}
	var plain, withInsight []string
	for _, r := range recipients {
		if insight != nil && r.IncludeInsight {
			withInsight = append(withInsight, r.Email)
		} else {
			plain = append(plain, r.Email)
		}
	}
	cfg, err := s.smtpConfig(ctx, orgID)
	if err != nil {
		s.logDelivery(ctx, orgID, ev, append(plain, withInsight...), err)
		return
	}
	if len(plain) > 0 {
		s.logDelivery(ctx, orgID, ev, plain, cfg.Send(ctx, plain, ev.Subject(), ev.Text(), ev.HTML()))
	}
	if len(withInsight) == 0 {
		return
	}
	// Generation can take a minute; those who did not ask for the insight
	// already have their email.
	rich := ev
	content, err := insight(ctx)
	switch {
	case err == nil:
		rich.Insight = content
	case err == errInsightUnavailable:
		rich.InsightNote = "not included — AI insights are not set up for this organization."
	default:
		rich.InsightNote = "could not be generated: " + err.Error()
		slog.Warn("auto insight failed", "org", orgID, "err", err)
	}
	s.logDelivery(ctx, orgID, rich, withInsight, cfg.Send(ctx, withInsight, rich.Subject(), rich.Text(), rich.HTML()))
}

func (s *Server) logDelivery(ctx context.Context, orgID uuid.UUID, ev notify.Event, to []string, sendErr error) {
	status, msg := "sent", ""
	if sendErr != nil {
		status, msg = "failed", sendErr.Error()
		slog.Warn("notification not sent", "org", orgID, "event", ev.Kind, "err", sendErr)
	}
	if err := s.Q.InsertDelivery(ctx, clouddb.InsertDeliveryParams{
		OrgID: orgID, Event: string(ev.Kind), Subject: ev.Subject(), Recipients: to, Status: status, Error: msg,
	}); err != nil {
		slog.Error("log notification delivery", "err", err)
	}
	_ = s.Q.PruneDeliveries(ctx, clouddb.PruneDeliveriesParams{OrgID: orgID, Limit: deliveriesKept})
}

// errSMTPUnconfigured is recorded when recipients exist but no server does.
var errSMTPUnconfigured = fmt.Errorf("no SMTP server configured — set one up under Notifications")

// smtpConfig loads and decrypts the organization's SMTP settings.
func (s *Server) smtpConfig(ctx context.Context, orgID uuid.UUID) (*notify.SMTP, error) {
	row, err := s.Q.GetOrgSMTP(ctx, orgID)
	if err != nil {
		return nil, errSMTPUnconfigured
	}
	cfg := &notify.SMTP{
		Host: row.Host, Port: int(row.Port), Username: row.Username, Encryption: row.Encryption,
		From: row.FromAddress, FromName: row.FromName,
	}
	if len(row.PasswordEnc) > 0 {
		pw, err := s.Box.OpenString(row.PasswordEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt SMTP password: %w", err)
		}
		cfg.Password = pw
	}
	return cfg, nil
}

// --- event builders ----------------------------------------------------------

func (s *Server) orgLink(org *clouddb.Org, path string) string {
	return s.Cfg.BaseURL + "/orgs/" + org.Slug + path
}

// orgEvent builds a non-run event in an organization.
func (s *Server) orgEvent(org *clouddb.Org, kind notify.Kind, title, summary string, link, linkLabel string, facts ...notify.Fact) notify.Event {
	return notify.Event{
		Kind: kind, OrgName: org.Name, Title: title, Summary: summary, Facts: facts,
		Link: link, LinkLabel: linkLabel, SettingsLink: s.orgLink(org, "/notifications"),
	}
}

// publishRunEvent announces a run transition. It reloads the run so the
// email reflects the persisted final state, and attaches the deploy-time
// inputs and, for failures, the log tail.
func (s *Server) publishRunEvent(ctx context.Context, kind notify.Kind, runID uuid.UUID, repo *clouddb.Repo) {
	run, err := s.Q.GetRun(ctx, runID)
	if err != nil {
		return
	}
	org, err := s.Q.GetOrg(ctx, repo.OrgID)
	if err != nil {
		return
	}
	var verb, summary string
	switch kind {
	case notify.RunStarted:
		verb = "started"
		summary = "A deployment run has started. You will get another email when it finishes if you subscribed to those events."
	case notify.RunSucceeded:
		verb = "succeeded"
		summary = "Every step finished successfully."
	case notify.RunFailed:
		verb = "failed"
		summary = "The run stopped with an error. The last log lines are below; open the run for the full log and an AI explanation."
	case notify.RunCanceled:
		verb = "was canceled"
		summary = "Someone canceled the run before it finished. Whatever the steps already did in external systems remains."
	}
	ev := s.orgEvent(org, kind,
		fmt.Sprintf("%s → %s %s (%s)", run.Deployment, run.TargetName, verb, repo.Name),
		summary,
		s.orgLink(org, "/repos/"+repo.Name+"/runs/"+run.ID.String()), "Open the run",
	)
	ev.Facts = append(ev.Facts,
		notify.Fact{Label: "Repository", Value: repo.Name},
		notify.Fact{Label: "Deployment", Value: run.Deployment},
		notify.Fact{Label: "Target", Value: run.TargetName},
	)
	trigger := "manual"
	if run.TriggerKind == "push" {
		trigger = "push"
		if run.CommitSha != "" {
			trigger += fmt.Sprintf(" (commit %.7s)", run.CommitSha)
		}
	} else if run.StartedBy.Valid {
		if u, err := s.Q.GetUser(ctx, run.StartedBy.UUID); err == nil {
			trigger = "manual, by " + u.Name
		}
	}
	ev.Facts = append(ev.Facts, notify.Fact{Label: "Trigger", Value: trigger})
	ev.Facts = append(ev.Facts, notify.Fact{Label: "Started", Value: run.CreatedAt.Local().Format("Jan 2, 2006 15:04 MST")})
	if run.StartedAt != nil && run.FinishedAt != nil {
		ev.Facts = append(ev.Facts, notify.Fact{Label: "Duration", Value: web.HumanDuration(run.FinishedAt.Sub(*run.StartedAt))})
	}
	if run.FailedStep.Valid {
		if steps, err := s.Q.ListRunSteps(ctx, run.ID); err == nil {
			for _, st := range steps {
				if st.Idx == run.FailedStep.Int32 {
					ev.Facts = append(ev.Facts, notify.Fact{Label: "Failed step", Value: fmt.Sprintf("%d of %d — %s", st.Idx+1, len(steps), st.Name)})
				}
			}
		}
	}
	if run.Error != "" && kind != notify.RunCanceled {
		ev.Error = run.Error
	}
	for _, in := range s.runInputVMs(ctx, run.ID) {
		if in.DeployTime && !in.Secret && in.Set() {
			ev.Inputs = append(ev.Inputs, notify.Fact{Label: in.Name, Value: in.Value})
		}
	}
	if kind != notify.RunFailed {
		s.publish(org.ID, ev)
		return
	}
	if logs, err := s.Q.ListRunLogs(ctx, run.ID); err == nil {
		start := len(logs) - logTailLines
		if start < 0 {
			start = 0
		}
		for _, l := range logs[start:] {
			ev.LogTail = append(ev.LogTail, l.Line)
		}
	}
	s.publishWithInsight(org.ID, ev, func(ctx context.Context) (string, error) {
		return s.autoInsight(ctx, org, repo, run)
	})
}

// runKindForStatus maps a final run status to its event kind.
func runKindForStatus(status string) (notify.Kind, bool) {
	switch status {
	case "succeeded":
		return notify.RunSucceeded, true
	case "failed":
		return notify.RunFailed, true
	case "canceled":
		return notify.RunCanceled, true
	}
	return "", false
}

// publishRepoEvent announces a repository event.
func (s *Server) publishRepoEvent(oc *orgCtx, kind notify.Kind, repo *clouddb.Repo, summary string) {
	title := ""
	switch kind {
	case notify.RepoConnected:
		title = "Repository " + repo.Name + " connected"
	case notify.RepoSynced:
		title = "Repository " + repo.Name + " synced"
	case notify.RepoDisconnected:
		title = "Repository " + repo.Name + " disconnected"
	}
	link, label := s.orgLink(oc.Org, "/repos/"+repo.Name), "Open the repository"
	if kind == notify.RepoDisconnected {
		link, label = s.orgLink(oc.Org, ""), "Open the organization"
	}
	facts := []notify.Fact{
		{Label: "Repository", Value: repo.Name},
		{Label: "Source", Value: repo.GitUrl + " (" + repo.Branch + ")"},
	}
	if kind != notify.RepoDisconnected {
		facts = append(facts, notify.Fact{Label: "Configuration", Value: configStatusText(repo)})
	}
	facts = append(facts, notify.Fact{Label: "By", Value: oc.Sess.User.Name})
	s.publish(oc.Org.ID, s.orgEvent(oc.Org, kind, title, summary, link, label, facts...))
}

func configStatusText(repo *clouddb.Repo) string {
	switch repo.Status {
	case "ok":
		n := 0
		if cfg := repoConfig(repo); cfg != nil {
			n = len(cfg.Deployments)
		}
		return fmt.Sprintf("valid — %d deployment(s) at %.7s", n, repo.LastCommitSha)
	case "config_missing":
		return "no keel.yaml on branch " + repo.Branch
	case "config_invalid":
		return "invalid: " + firstLine(repo.ConfigError)
	case "error":
		return "sync error: " + firstLine(repo.ConfigError)
	}
	return repo.Status
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// publishTargetEvent announces a target event.
func (s *Server) publishTargetEvent(rc *repoCtx, kind notify.Kind, dep, target, summary string, extra ...notify.Fact) {
	title := ""
	switch kind {
	case notify.TargetCreated:
		title = fmt.Sprintf("Target %s / %s created (%s)", dep, target, rc.Repo.Name)
	case notify.TargetValuesChanged:
		title = fmt.Sprintf("Variables changed on %s / %s (%s)", dep, target, rc.Repo.Name)
	case notify.TargetDeleted:
		title = fmt.Sprintf("Target %s / %s deleted (%s)", dep, target, rc.Repo.Name)
	}
	link, label := s.orgLink(rc.Org, "/repos/"+rc.Repo.Name+"/deployments/"+dep+"/targets/"+target), "Open the target"
	if kind == notify.TargetDeleted {
		link, label = s.orgLink(rc.Org, "/repos/"+rc.Repo.Name+"/deployments/"+dep), "Open the deployment"
	}
	facts := append([]notify.Fact{
		{Label: "Repository", Value: rc.Repo.Name},
		{Label: "Deployment", Value: dep},
		{Label: "Target", Value: target},
	}, extra...)
	facts = append(facts, notify.Fact{Label: "By", Value: rc.Sess.User.Name})
	s.publish(rc.Org.ID, s.orgEvent(rc.Org, kind, title, summary, link, label, facts...))
}

// publishMemberEvent announces a membership event.
func (s *Server) publishMemberEvent(org *clouddb.Org, kind notify.Kind, who, role, by string) {
	var title, summary string
	switch kind {
	case notify.MemberInvited:
		title, summary = who+" invited to "+org.Name, "An invite link was created; it is valid for 14 days."
	case notify.MemberJoined:
		title, summary = who+" joined "+org.Name, "The invite was accepted."
	case notify.MemberRemoved:
		title, summary = who+" removed from "+org.Name, "They no longer have access to the organization."
	}
	facts := []notify.Fact{{Label: "Member", Value: who}}
	if role != "" {
		facts = append(facts, notify.Fact{Label: "Role", Value: role})
	}
	if by != "" {
		facts = append(facts, notify.Fact{Label: "By", Value: by})
	}
	s.publish(org.ID, s.orgEvent(org, kind, title, summary, s.orgLink(org, "/members"), "Open members", facts...))
}
