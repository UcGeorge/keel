package cloudserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"encoding/json"

	"github.com/UcGeorge/keel/internal/config"
	"github.com/UcGeorge/keel/internal/githubapp"
	"github.com/UcGeorge/keel/internal/gitutil"
	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/UcGeorge/keel/internal/web"
	"github.com/jackc/pgx/v5/pgtype"
)

// repoVM converts a stored repo to its view model.
func (s *Server) repoVM(oc *orgCtx, repo *clouddb.Repo) web.RepoVM {
	vm := web.RepoVM{
		ID:             repo.ID.String(),
		Name:           repo.Name,
		Provider:       repo.Provider,
		GitURL:         repo.GitUrl,
		Branch:         repo.Branch,
		GithubFullName: repo.GithubFullName,
		Status:         repo.Status,
		ConfigError:    repo.ConfigError,
		LastCommitSHA:  repo.LastCommitSha,
		LastSyncedAt:   repo.LastSyncedAt,
		URL:            oc.urlBase() + "/repos/" + repo.Name,
	}
	if cfg := repoConfig(repo); cfg != nil {
		vm.Deployments = len(cfg.Deployments)
	}
	return vm
}

// handleRepos is the organization home: connected repositories.
func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	repos, err := s.Q.ListReposForOrg(r.Context(), oc.Org.ID)
	if err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not load repositories")
		return
	}
	page := web.PageRepos{
		Base:         s.base(w, r, oc.Sess, oc, "Repositories"),
		CanConfigure: oc.isAdmin(),
		ConnectURL:   oc.urlBase() + "/repos/new",
	}
	for _, repo := range repos {
		page.Repos = append(page.Repos, s.repoVM(oc, repo))
	}
	s.Renderer.Render(w, http.StatusOK, "cloud/repos.html", page)
}

// handleRepoNewPage renders the connect-repository form.
func (s *Server) handleRepoNewPage(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !oc.isAdmin() {
		s.errorPage(w, r, oc.Sess, http.StatusForbidden, "Only owners and admins can connect repositories")
		return
	}
	s.renderRepoNew(w, r, oc, &web.PageRepoNew{Branch: "main"}, http.StatusOK)
}

func (s *Server) renderRepoNew(w http.ResponseWriter, r *http.Request, oc *orgCtx, page *web.PageRepoNew, code int) {
	page.Base = s.base(w, r, oc.Sess, oc, "Connect a repository")
	page.FormAction = oc.urlBase() + "/repos"
	if page.Errors == nil {
		page.Errors = map[string]string{}
	}
	if s.GitHub != nil {
		page.GithubEnabled = true
		page.InstallURL = s.GitHub.InstallURL()
		installs, err := s.Q.ListGithubInstallations(r.Context())
		if err == nil {
			for _, inst := range installs {
				repos, err := s.GitHub.ListInstallationRepos(inst.InstallationID)
				if err != nil {
					page.GithubError = fmt.Sprintf("Could not list repositories for the %s installation: %v", inst.AccountLogin, err)
					continue
				}
				for _, gr := range repos {
					page.GithubRepos = append(page.GithubRepos, web.GithubPickVM{
						FullName: gr.FullName, InstallationID: inst.InstallationID,
					})
				}
			}
		}
	}
	s.Renderer.Render(w, code, "cloud/repo_new.html", page)
}

// handleRepoCreate connects a repository (plain git or GitHub App).
func (s *Server) handleRepoCreate(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !oc.isAdmin() {
		s.errorPage(w, r, oc.Sess, http.StatusForbidden, "Only owners and admins can connect repositories")
		return
	}
	provider := r.PostFormValue("provider")
	name := strings.TrimSpace(r.PostFormValue("name"))
	branch := strings.TrimSpace(r.PostFormValue("branch"))
	gitURL := strings.TrimSpace(r.PostFormValue("git_url"))
	token := r.PostFormValue("token")
	githubPick := r.PostFormValue("github_repo") // "<installation_id>|<full_name>"

	if branch == "" {
		branch = "main"
	}
	page := &web.PageRepoNew{Name: name, GitURL: gitURL, Branch: branch, Errors: map[string]string{}}

	var params clouddb.CreateRepoParams
	switch provider {
	case "github_app":
		if s.GitHub == nil {
			s.errorPage(w, r, oc.Sess, http.StatusBadRequest, "The GitHub App integration is not configured")
			return
		}
		instStr, fullName, ok := strings.Cut(githubPick, "|")
		instID, err := strconv.ParseInt(instStr, 10, 64)
		if !ok || err != nil || fullName == "" {
			page.Errors["github"] = "Pick a repository from the list."
			s.renderRepoNew(w, r, oc, page, http.StatusUnprocessableEntity)
			return
		}
		if name == "" {
			if _, short, ok := strings.Cut(fullName, "/"); ok {
				name = short
			}
		}
		params = clouddb.CreateRepoParams{
			Provider:             "github_app",
			GitUrl:               "https://github.com/" + fullName + ".git",
			GithubInstallationID: pgInt8(instID),
			GithubFullName:       fullName,
		}
	case "git":
		if !gitutil.ValidHTTPURL(gitURL) {
			page.Errors["git_url"] = "Enter an http(s) git URL, e.g. https://github.com/acme/api.git"
			s.renderRepoNew(w, r, oc, page, http.StatusUnprocessableEntity)
			return
		}
		if name == "" {
			name = repoNameFromURL(gitURL)
		}
		params = clouddb.CreateRepoParams{Provider: "git", GitUrl: gitURL}
		if token != "" {
			enc, err := s.Box.SealString(token)
			if err != nil {
				s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not store the access token")
				return
			}
			params.AuthTokenEnc = enc
		}
	default:
		s.errorPage(w, r, oc.Sess, http.StatusBadRequest, "Unknown provider")
		return
	}

	if !config.ValidDeploymentName(name) {
		page.Errors["name"] = "Repository names use lowercase letters, digits, and hyphens."
		page.Name = name
		s.renderRepoNew(w, r, oc, page, http.StatusUnprocessableEntity)
		return
	}

	params.OrgID = oc.Org.ID
	params.Name = name
	params.Branch = branch
	params.CreatedBy = nullUUID(oc.Sess.UserID)
	repo, err := s.Q.CreateRepo(r.Context(), params)
	if err != nil {
		page.Errors["name"] = fmt.Sprintf("A repository named %q is already connected to this organization.", name)
		s.renderRepoNew(w, r, oc, page, http.StatusUnprocessableEntity)
		return
	}

	// First sync happens synchronously so the user lands on a meaningful page.
	s.syncRepo(r.Context(), repo)
	web.SetFlash(w, "success", "Repository connected.")
	http.Redirect(w, r, oc.urlBase()+"/repos/"+repo.Name, http.StatusSeeOther)
}

func repoNameFromURL(url string) string {
	trimmed := strings.TrimSuffix(strings.TrimRight(url, "/"), ".git")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	return strings.ToLower(trimmed)
}

// repoAuth resolves clone credentials for a repository.
func (s *Server) repoAuth(repo *clouddb.Repo) (*gitutil.Auth, error) {
	switch repo.Provider {
	case "github_app":
		if s.GitHub == nil {
			return nil, fmt.Errorf("the GitHub App integration is not configured")
		}
		if !repo.GithubInstallationID.Valid {
			return nil, fmt.Errorf("repository has no GitHub installation")
		}
		token, err := s.GitHub.InstallationToken(repo.GithubInstallationID.Int64)
		if err != nil {
			return nil, err
		}
		return &gitutil.Auth{Username: "x-access-token", Password: token}, nil
	default:
		if len(repo.AuthTokenEnc) == 0 {
			return nil, nil // public repository
		}
		token, err := s.Box.OpenString(repo.AuthTokenEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt repository token: %w", err)
		}
		return &gitutil.Auth{Username: "token", Password: token}, nil
	}
}

// syncRepo clones the repository and refreshes its stored configuration.
func (s *Server) syncRepo(ctx context.Context, repo *clouddb.Repo) {
	status, cfgYAML, cfgErr, sha := s.fetchConfig(ctx, repo)
	if err := s.Q.UpdateRepoSync(ctx, clouddb.UpdateRepoSyncParams{
		ID: repo.ID, Status: status, ConfigYaml: cfgYAML, ConfigError: cfgErr, LastCommitSha: sha,
	}); err != nil {
		slog.Error("update repo sync", "repo", repo.ID, "err", err)
	}
}

func (s *Server) fetchConfig(ctx context.Context, repo *clouddb.Repo) (status, cfgYAML, cfgErr, sha string) {
	auth, err := s.repoAuth(repo)
	if err != nil {
		return "error", "", err.Error(), ""
	}
	dir := filepath.Join(s.Cfg.DataDir, "sync", repo.ID.String())
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)

	sha, err = gitutil.CloneShallow(ctx, repo.GitUrl, repo.Branch, dir, auth)
	if err != nil {
		return "error", "", err.Error(), ""
	}
	path, err := config.Find(dir)
	if err != nil {
		return "config_missing", "", fmt.Sprintf("no keel.yaml on branch %q", repo.Branch), sha
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "error", "", err.Error(), sha
	}
	if _, err := config.Parse(data); err != nil {
		var verrs *config.ValidationErrors
		if errors.As(err, &verrs) {
			return "config_invalid", string(data), verrs.Error(), sha
		}
		return "config_invalid", string(data), err.Error(), sha
	}
	return "ok", string(data), "", sha
}

// handleRepo renders the repository overview.
func (s *Server) handleRepo(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	page := web.PageRepo{
		Base:         s.base(w, r, rc.Sess, rc.orgCtx, rc.Repo.Name),
		Repo:         s.repoVM(rc.orgCtx, rc.Repo),
		CanConfigure: rc.canConfigure(),
		SyncURL:      rc.repoURL() + "/sync",
	}
	page.Config = repoConfigStatus(rc.Repo)
	if cfg := repoConfig(rc.Repo); cfg != nil {
		targets, _ := s.Q.ListTargetsForRepo(r.Context(), rc.Repo.ID)
		for _, d := range cfg.Deployments {
			vm := web.NewDeploymentVM(d, rc.repoURL()+"/deployments/"+d.Name)
			for _, t := range targets {
				if t.Deployment == d.Name {
					vm.Targets = append(vm.Targets, s.targetVM(r.Context(), rc, d, t, false))
				}
			}
			page.Deployments = append(page.Deployments, vm)
		}
	}
	s.Renderer.Render(w, http.StatusOK, "cloud/repo.html", page)
}

// repoConfigStatus summarizes stored config validity for banners.
func repoConfigStatus(repo *clouddb.Repo) web.ConfigStatusVM {
	source := repo.Branch
	if repo.LastCommitSha != "" {
		source = fmt.Sprintf("%s @ %.7s", repo.Branch, repo.LastCommitSha)
	}
	switch repo.Status {
	case "ok":
		return web.ConfigStatusVM{OK: true, Source: source}
	case "config_missing":
		return web.ConfigStatusVM{Missing: true, Source: source}
	case "pending":
		return web.ConfigStatusVM{Missing: true, Source: "not synced yet"}
	default:
		return web.ConfigStatusVM{
			Source: source,
			Errors: []config.ValidationError{{Message: repo.ConfigError}},
		}
	}
}

// handleRepoSync re-reads the configuration from the repository.
func (s *Server) handleRepoSync(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	if !rc.canConfigure() {
		s.errorPage(w, r, rc.Sess, http.StatusForbidden, "You don't have permission to sync this repository")
		return
	}
	s.syncRepo(r.Context(), rc.Repo)
	web.SetFlash(w, "success", "Repository synced.")
	http.Redirect(w, r, rc.repoURL(), http.StatusSeeOther)
}

// handleRepoSettings renders repository settings.
func (s *Server) handleRepoSettings(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	if !rc.isAdmin() {
		s.errorPage(w, r, rc.Sess, http.StatusForbidden, "Only owners and admins can manage repository settings")
		return
	}
	page := web.PageRepoSettings{
		Base:      s.base(w, r, rc.Sess, rc.orgCtx, rc.Repo.Name+" settings"),
		Repo:      s.repoVM(rc.orgCtx, rc.Repo),
		Errors:    map[string]string{},
		HasToken:  len(rc.Repo.AuthTokenEnc) > 0,
		DeleteURL: rc.repoURL() + "/delete",
	}
	s.Renderer.Render(w, http.StatusOK, "cloud/repo_settings.html", page)
}

// handleRepoSettingsSave updates name, branch, URL, and token.
func (s *Server) handleRepoSettingsSave(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	if !rc.isAdmin() {
		s.errorPage(w, r, rc.Sess, http.StatusForbidden, "Only owners and admins can manage repository settings")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	branch := strings.TrimSpace(r.PostFormValue("branch"))
	gitURL := strings.TrimSpace(r.PostFormValue("git_url"))
	token := r.PostFormValue("token")

	if !config.ValidDeploymentName(name) {
		web.SetFlash(w, "error", "Repository names use lowercase letters, digits, and hyphens.")
		http.Redirect(w, r, rc.repoURL()+"/settings", http.StatusSeeOther)
		return
	}
	if branch == "" {
		branch = "main"
	}
	if rc.Repo.Provider == "github_app" {
		gitURL = rc.Repo.GitUrl // fixed for app-connected repos
	} else if !gitutil.ValidHTTPURL(gitURL) {
		web.SetFlash(w, "error", "Enter an http(s) git URL.")
		http.Redirect(w, r, rc.repoURL()+"/settings", http.StatusSeeOther)
		return
	}
	tokenEnc := rc.Repo.AuthTokenEnc
	if token != "" {
		enc, err := s.Box.SealString(token)
		if err != nil {
			s.errorPage(w, r, rc.Sess, http.StatusInternalServerError, "Could not store the access token")
			return
		}
		tokenEnc = enc
	}
	if r.PostFormValue("clear_token") == "true" {
		tokenEnc = nil
	}
	updated, err := s.Q.UpdateRepoSettings(r.Context(), clouddb.UpdateRepoSettingsParams{
		ID: rc.Repo.ID, Name: name, Branch: branch, GitUrl: gitURL, AuthTokenEnc: tokenEnc,
	})
	if err != nil {
		web.SetFlash(w, "error", fmt.Sprintf("A repository named %q already exists in this organization.", name))
		http.Redirect(w, r, rc.repoURL()+"/settings", http.StatusSeeOther)
		return
	}
	s.syncRepo(r.Context(), updated)
	web.SetFlash(w, "success", "Repository settings saved.")
	http.Redirect(w, r, rc.urlBase()+"/repos/"+updated.Name+"/settings", http.StatusSeeOther)
}

// handleRepoDelete disconnects a repository and all its targets and runs.
func (s *Server) handleRepoDelete(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	if !rc.isAdmin() {
		s.errorPage(w, r, rc.Sess, http.StatusForbidden, "Only owners and admins can disconnect repositories")
		return
	}
	if r.PostFormValue("confirm_name") != rc.Repo.Name {
		web.SetFlash(w, "error", "Type the repository name exactly to confirm.")
		http.Redirect(w, r, rc.repoURL()+"/settings", http.StatusSeeOther)
		return
	}
	if err := s.Q.DeleteRepo(r.Context(), rc.Repo.ID); err != nil {
		s.errorPage(w, r, rc.Sess, http.StatusInternalServerError, "Could not disconnect the repository")
		return
	}
	web.SetFlash(w, "success", "Repository disconnected.")
	http.Redirect(w, r, rc.urlBase(), http.StatusSeeOther)
}

// --- GitHub webhook ----------------------------------------------------------

// handleGithubWebhook ingests installation bookkeeping and push events.
func (s *Server) handleGithubWebhook(w http.ResponseWriter, r *http.Request) {
	if s.GitHub == nil {
		http.Error(w, "github app not configured", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if !s.GitHub.VerifyWebhook(r.Header.Get("X-Hub-Signature-256"), body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	switch event {
	case "installation":
		var ev githubapp.InstallationEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		switch ev.Action {
		case "deleted":
			_ = s.Q.DeleteGithubInstallation(r.Context(), ev.Installation.ID)
		default:
			_ = s.Q.UpsertGithubInstallation(r.Context(), clouddb.UpsertGithubInstallationParams{
				InstallationID: ev.Installation.ID,
				AccountLogin:   ev.Installation.Account.Login,
				AccountType:    ev.Installation.Account.Type,
			})
		}
	case "installation_repositories":
		var ev githubapp.InstallationEvent
		if err := json.Unmarshal(body, &ev); err == nil {
			_ = s.Q.UpsertGithubInstallation(r.Context(), clouddb.UpsertGithubInstallationParams{
				InstallationID: ev.Installation.ID,
				AccountLogin:   ev.Installation.Account.Login,
				AccountType:    ev.Installation.Account.Type,
			})
		}
	case "push":
		var ev githubapp.PushEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		s.handlePush(r.Context(), &ev)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePush syncs affected repositories and triggers auto-deploy targets.
func (s *Server) handlePush(ctx context.Context, ev *githubapp.PushEvent) {
	branch := ev.Branch()
	if branch == "" || ev.Repository.FullName == "" {
		return
	}
	repos, err := s.Q.ListReposByGithubRepo(ctx, clouddb.ListReposByGithubRepoParams{
		GithubInstallationID: pgInt8(ev.Installation.ID),
		Lower:                ev.Repository.FullName,
	})
	if err != nil {
		slog.Error("webhook: list repos", "err", err)
		return
	}
	for _, repo := range repos {
		if repo.Branch != branch {
			continue
		}
		s.syncRepo(ctx, repo)
		fresh, err := s.Q.GetRepo(ctx, repo.ID)
		if err != nil {
			continue
		}
		cfg := repoConfig(fresh)
		if cfg == nil {
			slog.Warn("webhook: config unavailable after sync, skipping auto-deploy", "repo", repo.Name)
			continue
		}
		targets, err := s.Q.ListAutoDeployTargets(ctx, repo.ID)
		if err != nil {
			continue
		}
		for _, t := range targets {
			d := cfg.Deployment(t.Deployment)
			if d == nil {
				continue
			}
			if err := s.autoDeploy(ctx, fresh, d, t, ev.After); err != nil {
				slog.Warn("webhook: auto-deploy failed to start", "repo", repo.Name, "target", t.Name, "err", err)
			}
		}
	}
}

func pgInt8(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: true}
}
