package cloudserver

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/UcGeorge/keel/internal/config"
	"github.com/UcGeorge/keel/internal/insight"
	"github.com/UcGeorge/keel/internal/llm"
	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/UcGeorge/keel/internal/web"
	"github.com/google/uuid"
)

func (oc *orgCtx) aiURL() string { return oc.urlBase() + "/ai" }

// errInsightUnavailable is returned when an organization has no verified
// model configuration.
var errInsightUnavailable = fmt.Errorf("AI insights are not set up for this organization")

// aiClient loads the organization's verified model configuration, or
// returns false when AI insights are not set up.
func (s *Server) aiClient(ctx context.Context, orgID uuid.UUID) (*llm.Client, string, bool) {
	row, err := s.Q.GetOrgAI(ctx, orgID)
	if err != nil {
		return nil, "", false
	}
	c := &llm.Client{BaseURL: row.BaseUrl}
	if len(row.ApiKeyEnc) > 0 {
		key, err := s.Box.OpenString(row.ApiKeyEnc)
		if err != nil {
			return nil, "", false
		}
		c.APIKey = key
	}
	return c, row.Model, true
}

func (s *Server) handleAI(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !s.requireAdmin(w, r, oc, "AI insights") {
		return
	}
	s.renderAI(w, r, oc, "", http.StatusOK)
}

func (s *Server) renderAI(w http.ResponseWriter, r *http.Request, oc *orgCtx, errMsg string, code int) {
	page := web.PageAI{Base: s.base(w, r, oc.Sess, oc, "AI insights"), URLBase: oc.urlBase(), Error: errMsg}
	for _, p := range llm.Presets {
		page.Presets = append(page.Presets, web.AIPresetVM{Name: p.Name, BaseURL: p.BaseURL, Hint: p.Hint})
	}
	if row, err := s.Q.GetOrgAI(r.Context(), oc.Org.ID); err == nil {
		page.Configured = true
		page.BaseURL = row.BaseUrl
		page.Model = row.Model
		page.HasKey = len(row.ApiKeyEnc) > 0
		page.VerifiedAt = row.VerifiedAt
		page.ModelPicker = web.AIModelsVM{Current: row.Model}
	}
	s.Renderer.Render(w, code, "cloud/ai.html", page)
}

// aiFormClient builds a client from the form, falling back to the saved
// key when the field is left blank.
func (s *Server) aiFormClient(r *http.Request, oc *orgCtx) (*llm.Client, string, error) {
	base, err := llm.NormalizeBaseURL(r.PostFormValue("base_url"))
	if err != nil {
		return nil, "", err
	}
	key := r.PostFormValue("api_key")
	if key == "" {
		if row, err := s.Q.GetOrgAI(r.Context(), oc.Org.ID); err == nil && len(row.ApiKeyEnc) > 0 {
			if saved, err := s.Box.OpenString(row.ApiKeyEnc); err == nil {
				key = saved
			}
		}
	}
	model := strings.TrimSpace(r.PostFormValue("model_custom"))
	if model == "" {
		model = strings.TrimSpace(r.PostFormValue("model"))
	}
	return &llm.Client{BaseURL: base, APIKey: key, HTTP: &http.Client{Timeout: 45 * time.Second}}, model, nil
}

// handleAIModels lists the provider's models (htmx fragment).
func (s *Server) handleAIModels(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !oc.isAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	vm := web.AIModelsVM{Current: strings.TrimSpace(r.PostFormValue("model"))}
	client, _, err := s.aiFormClient(r, oc)
	if err != nil {
		vm.Error = err.Error()
	} else if ids, err := client.ListModels(r.Context()); err != nil {
		vm.Error = err.Error() + " — you can still type a model id below and test it."
	} else {
		chat := llm.ChatModels(ids)
		if len(chat) == 0 {
			chat = ids
		}
		vm.Models = chat
		vm.Hidden = len(ids) - len(chat)
		if vm.Current == "" {
			if row, err := s.Q.GetOrgAI(r.Context(), oc.Org.ID); err == nil {
				vm.Current = row.Model
			}
		}
	}
	s.Renderer.RenderFragment(w, "cloud/ai.html", "ai_models", vm)
}

// handleAITest asks the model for OK (htmx fragment).
func (s *Server) handleAITest(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !oc.isAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	vm := s.aiPing(r, oc)
	s.Renderer.RenderFragment(w, "cloud/ai.html", "ai_test", vm)
}

func (s *Server) aiPing(r *http.Request, oc *orgCtx) web.AITestVM {
	client, model, err := s.aiFormClient(r, oc)
	if err != nil {
		return web.AITestVM{Error: err.Error()}
	}
	if model == "" {
		return web.AITestVM{Error: "Pick a model (or type its id) first."}
	}
	reply, err := client.Ping(r.Context(), model)
	if err != nil {
		return web.AITestVM{Model: model, Reply: reply, Error: err.Error()}
	}
	return web.AITestVM{OK: true, Model: model, Reply: strings.TrimSpace(reply)}
}

// handleAISave re-verifies the configuration and stores it.
func (s *Server) handleAISave(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !s.requireAdmin(w, r, oc, "AI insights") {
		return
	}
	test := s.aiPing(r, oc)
	if !test.OK {
		s.renderAIWithForm(w, r, oc, test)
		return
	}
	client, model, _ := s.aiFormClient(r, oc)
	var keyEnc []byte
	if client.APIKey != "" {
		enc, err := s.Box.SealString(client.APIKey)
		if err != nil {
			s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not encrypt the API key")
			return
		}
		keyEnc = enc
	}
	if err := s.Q.UpsertOrgAI(r.Context(), clouddb.UpsertOrgAIParams{
		OrgID: oc.Org.ID, BaseUrl: client.BaseURL, ApiKeyEnc: keyEnc, Model: model, UpdatedBy: nullUUID(oc.Sess.UserID),
	}); err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not save the AI settings")
		return
	}
	web.SetFlash(w, "success", "AI insights are on — failed runs now offer an explanation, using "+model+".")
	http.Redirect(w, r, oc.aiURL(), http.StatusSeeOther)
}

// renderAIWithForm re-renders the page with the submitted values and the
// failed test, so nothing typed is lost.
func (s *Server) renderAIWithForm(w http.ResponseWriter, r *http.Request, oc *orgCtx, test web.AITestVM) {
	page := web.PageAI{Base: s.base(w, r, oc.Sess, oc, "AI insights"), URLBase: oc.urlBase()}
	for _, p := range llm.Presets {
		page.Presets = append(page.Presets, web.AIPresetVM{Name: p.Name, BaseURL: p.BaseURL, Hint: p.Hint})
	}
	if row, err := s.Q.GetOrgAI(r.Context(), oc.Org.ID); err == nil {
		page.Configured = true
		page.Model = row.Model
		page.HasKey = len(row.ApiKeyEnc) > 0
		page.VerifiedAt = row.VerifiedAt
	}
	page.BaseURL = strings.TrimSpace(r.PostFormValue("base_url"))
	page.ModelPicker = web.AIModelsVM{Current: test.Model}
	page.TestResult = test
	page.Error = "The configuration was not saved because the test failed."
	s.Renderer.Render(w, http.StatusUnprocessableEntity, "cloud/ai.html", page)
}

func (s *Server) handleAIDelete(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !s.requireAdmin(w, r, oc, "AI insights") {
		return
	}
	if err := s.Q.DeleteOrgAI(r.Context(), oc.Org.ID); err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not turn off AI insights")
		return
	}
	web.SetFlash(w, "success", "AI insights turned off.")
	http.Redirect(w, r, oc.aiURL(), http.StatusSeeOther)
}

// --- run insights ------------------------------------------------------------

// insightCard builds the card for a run page: nil for runs that did not
// fail, otherwise the stored insight (if any) and the generate control.
func (s *Server) insightCard(ctx context.Context, rc *repoCtx, run *clouddb.Run) *web.InsightCardVM {
	if run.Status != "failed" {
		return nil
	}
	_, _, configured := s.aiClient(ctx, rc.Org.ID)
	if !configured && !rc.isAdmin() {
		return nil // nothing a member could act on
	}
	card := &web.InsightCardVM{CSRF: rc.Sess.CsrfToken}
	if configured {
		card.URL = rc.repoURL() + "/runs/" + run.ID.String() + "/insight"
	} else {
		card.SetupURL = rc.aiURL()
	}
	if row, err := s.Q.GetRunInsight(ctx, run.ID); err == nil {
		card.Insight = s.insightVM(ctx, row)
	}
	return card
}

func (s *Server) insightVM(ctx context.Context, row *clouddb.RunInsight) *web.InsightVM {
	vm := &web.InsightVM{Content: web.Funcs["markdown"].(func(string) template.HTML)(row.Content), Model: row.Model, CreatedAt: row.CreatedAt, Auto: !row.CreatedBy.Valid}
	if row.CreatedBy.Valid {
		if u, err := s.Q.GetUser(ctx, row.CreatedBy.UUID); err == nil {
			vm.CreatedBy = u.Name
		}
	}
	return vm
}

// handleRunInsight generates (or regenerates) the explanation of a failed
// run and returns the refreshed card.
func (s *Server) handleRunInsight(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	run := s.pathRun(w, r, rc)
	if run == nil {
		return
	}
	ctx := r.Context()
	card := s.insightCard(ctx, rc, run)
	if card == nil {
		http.Error(w, "insights are only available for failed runs", http.StatusConflict)
		return
	}
	client, model, ok := s.aiClient(ctx, rc.Org.ID)
	if !ok {
		card.Error = "AI insights are not set up for this organization."
		s.Renderer.RenderFragment(w, "run.html", "insight_card", card)
		return
	}
	rctx := s.insightRun(ctx, rc.Org.Name, rc.Repo, run)
	genCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	content, err := insight.Explain(genCtx, client, model, rctx)
	if err != nil {
		card.Error = "The model could not be reached: " + err.Error()
		s.Renderer.RenderFragment(w, "run.html", "insight_card", card)
		return
	}
	if err := s.Q.UpsertRunInsight(ctx, clouddb.UpsertRunInsightParams{
		RunID: run.ID, Model: model, Content: content, CreatedBy: nullUUID(rc.Sess.UserID),
	}); err != nil {
		card.Error = "The explanation could not be stored: " + err.Error()
	}
	if row, err := s.Q.GetRunInsight(ctx, run.ID); err == nil {
		card.Insight = s.insightVM(ctx, row)
	} else {
		card.Insight = &web.InsightVM{Content: web.Funcs["markdown"].(func(string) template.HTML)(content), Model: model, CreatedAt: time.Now(), CreatedBy: rc.Sess.User.Name}
	}
	s.Renderer.RenderFragment(w, "run.html", "insight_card", card)
}

// autoInsight returns the stored insight of a failed run, generating and
// storing one when there is none yet. Used by failure emails.
func (s *Server) autoInsight(ctx context.Context, org *clouddb.Org, repo *clouddb.Repo, run *clouddb.Run) (string, error) {
	client, model, ok := s.aiClient(ctx, org.ID)
	if !ok {
		return "", errInsightUnavailable
	}
	if row, err := s.Q.GetRunInsight(ctx, run.ID); err == nil {
		return row.Content, nil
	}
	genCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	content, err := insight.Explain(genCtx, client, model, s.insightRun(ctx, org.Name, repo, run))
	if err != nil {
		return "", err
	}
	if err := s.Q.UpsertRunInsight(ctx, clouddb.UpsertRunInsightParams{RunID: run.ID, Model: model, Content: content}); err != nil {
		slog.Warn("store auto insight", "run", run.ID, "err", err)
	}
	return content, nil
}

// insightRun gathers everything the model gets to see about a run.
func (s *Server) insightRun(ctx context.Context, orgName string, repo *clouddb.Repo, run *clouddb.Run) insight.Run {
	rctx := insight.Run{
		Org: orgName, Repo: repo.Name, Deployment: run.Deployment, Target: run.TargetName,
		Trigger: run.TriggerKind, CommitSHA: run.CommitSha, Status: run.Status, Error: run.Error,
	}
	if run.StartedBy.Valid {
		if u, err := s.Q.GetUser(ctx, run.StartedBy.UUID); err == nil {
			rctx.StartedBy = u.Name
		}
	}
	if run.ExitCode.Valid {
		v := int(run.ExitCode.Int32)
		rctx.ExitCode = &v
	}
	if run.FailedStep.Valid {
		v := int(run.FailedStep.Int32)
		rctx.FailedStep = &v
	}
	if run.StartedAt != nil && run.FinishedAt != nil {
		rctx.Duration = web.HumanDuration(run.FinishedAt.Sub(*run.StartedAt))
	}
	var d *config.Deployment
	if cfg := repoConfig(repo); cfg != nil {
		d = cfg.Deployment(run.Deployment)
	}
	if d != nil {
		rctx.Dockerfile = d.Environment.Dockerfile
		rctx.Description = d.Description
	}
	if steps, err := s.Q.ListRunSteps(ctx, run.ID); err == nil {
		for _, st := range steps {
			step := insight.Step{Name: st.Name, Status: st.Status}
			if d != nil && int(st.Idx) < len(d.Steps) && d.Steps[st.Idx].Name == st.Name {
				step.Run = d.Steps[st.Idx].Run
			}
			rctx.Steps = append(rctx.Steps, step)
		}
	}
	for _, in := range s.runInputVMs(ctx, run.ID) {
		ic := insight.Input{Name: in.Name, Label: in.Label, Value: in.Value, Set: in.Set(), Secret: in.Secret, DeployTime: in.DeployTime, Source: in.Source}
		if d != nil {
			if v := d.Variable(in.Name); v != nil {
				ic.Type = string(v.Type)
				ic.Description = v.Description
			}
		}
		rctx.Inputs = append(rctx.Inputs, ic)
	}
	if logs, err := s.Q.ListRunLogs(ctx, run.ID); err == nil {
		for _, l := range logs {
			rctx.Log = append(rctx.Log, l.Line)
		}
	}
	return rctx
}
