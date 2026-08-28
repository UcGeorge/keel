package cloudserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/UcGeorge/keel/internal/auth"
	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/UcGeorge/keel/internal/web"
)

const sessionTTL = 30 * 24 * time.Hour

func authHash(token string) []byte { return auth.HashToken(token) }

func authNewToken() (string, []byte, error) { return auth.NewToken() }

// safeNext keeps redirects on-site.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

// handleHome sends the user to their first organization.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request, sess *sessionInfo) {
	orgs, err := s.Q.ListOrgsForUser(r.Context(), sess.UserID)
	if err != nil || len(orgs) == 0 {
		http.Redirect(w, r, "/orgs/new", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/orgs/"+orgs[0].Slug, http.StatusSeeOther)
}

// --- signup ------------------------------------------------------------------

func (s *Server) handleSignupPage(w http.ResponseWriter, r *http.Request) {
	if s.session(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	page := web.PageSignup{
		Base: s.base(w, r, nil, nil, "Create your account"),
		Next: r.URL.Query().Get("next"), Errors: map[string]string{},
	}
	s.Renderer.Render(w, http.StatusOK, "auth/signup.html", page)
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	email := auth.NormalizeEmail(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	next := r.PostFormValue("next")

	errs := map[string]string{}
	if name == "" {
		errs["name"] = "Your name is required."
	}
	if !auth.ValidEmail(email) {
		errs["email"] = "Enter a valid email address."
	}
	if msg := auth.CheckPasswordStrength(password); msg != "" {
		errs["password"] = msg
	}
	if len(errs) == 0 {
		if _, err := s.Q.GetUserByEmail(r.Context(), email); err == nil {
			errs["email"] = "An account with this email already exists — sign in instead."
		}
	}
	if len(errs) > 0 {
		page := web.PageSignup{Base: s.base(w, r, nil, nil, "Create your account"), Name: name, Email: email, Next: next, Errors: errs}
		s.Renderer.Render(w, http.StatusUnprocessableEntity, "auth/signup.html", page)
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		s.errorPage(w, r, nil, http.StatusInternalServerError, "Could not create the account")
		return
	}
	user, err := s.Q.CreateUser(r.Context(), clouddb.CreateUserParams{Email: email, Name: name, PasswordHash: hash})
	if err != nil {
		s.errorPage(w, r, nil, http.StatusInternalServerError, "Could not create the account")
		return
	}

	// Every user gets a personal organization.
	if err := s.createPersonalOrg(r.Context(), user); err != nil {
		slog.Error("create personal org", "user", user.ID, "err", err)
	}

	if err := s.startSession(w, r, user); err != nil {
		s.errorPage(w, r, nil, http.StatusInternalServerError, "Could not sign you in")
		return
	}
	web.SetFlash(w, "success", "Welcome to Keel Cloud!")
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

// createPersonalOrg provisions the user's personal organization with a
// unique slug derived from their email or name.
func (s *Server) createPersonalOrg(ctx context.Context, user *clouddb.User) error {
	local := user.Email[:strings.Index(user.Email, "@")]
	base := auth.Slugify(local)
	if !auth.ValidSlug(base) {
		base = auth.Slugify(user.Name)
	}
	if !auth.ValidSlug(base) {
		base = "user"
	}
	slug := base
	for i := 0; i < 50; i++ {
		if i > 0 {
			slug = fmt.Sprintf("%s-%d", base, i+1)
		}
		org, err := s.Q.CreateOrg(ctx, clouddb.CreateOrgParams{
			Slug: slug, Name: user.Name, Personal: true,
			CreatedBy: nullUUID(user.ID),
		})
		if err != nil {
			continue // slug collision — try the next suffix
		}
		_, err = s.Q.CreateOrgMember(ctx, clouddb.CreateOrgMemberParams{
			OrgID: org.ID, UserID: user.ID, Role: "owner", CanConfigure: true, CanDeploy: true,
		})
		return err
	}
	return fmt.Errorf("could not find a free slug for %q", base)
}

// startSession creates a session and sets the cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user *clouddb.User) error {
	token, tokenHash, err := auth.NewToken()
	if err != nil {
		return err
	}
	csrf, _, err := auth.NewToken()
	if err != nil {
		return err
	}
	expires := time.Now().Add(sessionTTL)
	if _, err := s.Q.CreateSession(r.Context(), clouddb.CreateSessionParams{
		UserID: user.ID, TokenHash: tokenHash, CsrfToken: csrf, ExpiresAt: expires,
	}); err != nil {
		return err
	}
	s.setSessionCookie(w, token, expires)
	return nil
}

// --- login / logout ----------------------------------------------------------

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if s.session(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	page := web.PageLogin{Base: s.base(w, r, nil, nil, "Sign in"), Next: r.URL.Query().Get("next")}
	s.Renderer.Render(w, http.StatusOK, "auth/login.html", page)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	email := auth.NormalizeEmail(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	next := r.PostFormValue("next")

	fail := func() {
		page := web.PageLogin{
			Base:  s.base(w, r, nil, nil, "Sign in"),
			Email: email, Next: next,
			Error: "Incorrect email or password.",
		}
		s.Renderer.Render(w, http.StatusUnauthorized, "auth/login.html", page)
	}
	user, err := s.Q.GetUserByEmail(r.Context(), email)
	if err != nil || !auth.VerifyPassword(user.PasswordHash, password) {
		fail()
		return
	}
	if err := s.startSession(w, r, user); err != nil {
		s.errorPage(w, r, nil, http.StatusInternalServerError, "Could not sign you in")
		return
	}
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if sess := s.session(r); sess != nil {
		_ = s.Q.DeleteSession(r.Context(), sess.ID)
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// --- invites -----------------------------------------------------------------

// handleInvitePage shows an invite and how to accept it.
func (s *Server) handleInvitePage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	invite, err := s.Q.GetInviteByTokenHash(r.Context(), authHash(token))
	if err != nil {
		s.errorPage(w, r, nil, http.StatusNotFound, "This invite link is invalid or has expired")
		return
	}
	org, err := s.Q.GetOrg(r.Context(), invite.OrgID)
	if err != nil {
		s.errorPage(w, r, nil, http.StatusNotFound, "This invite link is invalid or has expired")
		return
	}
	sess := s.session(r)
	page := web.PageInvite{
		Base:      s.base(w, r, sess, nil, "Join "+org.Name),
		OrgName:   org.Name,
		Role:      invite.Role,
		Token:     token,
		NeedsAuth: sess == nil,
	}
	s.Renderer.Render(w, http.StatusOK, "auth/invite.html", page)
}

// handleInviteAccept adds the signed-in user to the org.
func (s *Server) handleInviteAccept(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r)
	if sess == nil {
		http.Redirect(w, r, "/login?next="+url.QueryEscape("/invites/"+r.PathValue("token")), http.StatusSeeOther)
		return
	}
	invite, err := s.Q.GetInviteByTokenHash(r.Context(), authHash(r.PathValue("token")))
	if err != nil {
		s.errorPage(w, r, sess, http.StatusNotFound, "This invite link is invalid or has expired")
		return
	}
	org, err := s.Q.GetOrg(r.Context(), invite.OrgID)
	if err != nil {
		s.errorPage(w, r, sess, http.StatusNotFound, "This invite link is invalid or has expired")
		return
	}
	if _, err := s.Q.GetOrgMember(r.Context(), clouddb.GetOrgMemberParams{OrgID: org.ID, UserID: sess.UserID}); err == nil {
		web.SetFlash(w, "info", "You are already a member of "+org.Name+".")
		http.Redirect(w, r, "/orgs/"+org.Slug, http.StatusSeeOther)
		return
	}
	if _, err := s.Q.CreateOrgMember(r.Context(), clouddb.CreateOrgMemberParams{
		OrgID: org.ID, UserID: sess.UserID, Role: invite.Role,
		CanConfigure: invite.CanConfigure, CanDeploy: invite.CanDeploy,
	}); err != nil {
		s.errorPage(w, r, sess, http.StatusInternalServerError, "Could not join the organization")
		return
	}
	_ = s.Q.AcceptInvite(r.Context(), invite.ID)
	web.SetFlash(w, "success", "Welcome to "+org.Name+"!")
	http.Redirect(w, r, "/orgs/"+org.Slug, http.StatusSeeOther)
}

// --- account -----------------------------------------------------------------

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request, sess *sessionInfo) {
	page := web.PageAccount{
		Base: s.base(w, r, sess, nil, "Account settings"),
		Name: sess.User.Name, Email: sess.User.Email,
		Errors: map[string]string{},
	}
	s.Renderer.Render(w, http.StatusOK, "cloud/account.html", page)
}

func (s *Server) handleAccountProfile(w http.ResponseWriter, r *http.Request, sess *sessionInfo) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		web.SetFlash(w, "error", "Your name is required.")
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	if _, err := s.Q.UpdateUserProfile(r.Context(), clouddb.UpdateUserProfileParams{ID: sess.UserID, Name: name}); err != nil {
		s.errorPage(w, r, sess, http.StatusInternalServerError, "Could not update the profile")
		return
	}
	web.SetFlash(w, "success", "Profile updated.")
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request, sess *sessionInfo) {
	current := r.PostFormValue("current_password")
	newPw := r.PostFormValue("new_password")
	if !auth.VerifyPassword(sess.User.PasswordHash, current) {
		web.SetFlash(w, "error", "Your current password is incorrect.")
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	if msg := auth.CheckPasswordStrength(newPw); msg != "" {
		web.SetFlash(w, "error", msg)
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	hash, err := auth.HashPassword(newPw)
	if err != nil {
		s.errorPage(w, r, sess, http.StatusInternalServerError, "Could not change the password")
		return
	}
	if err := s.Q.UpdateUserPassword(r.Context(), clouddb.UpdateUserPasswordParams{ID: sess.UserID, PasswordHash: hash}); err != nil {
		s.errorPage(w, r, sess, http.StatusInternalServerError, "Could not change the password")
		return
	}
	// Changing the password ends every other session.
	_ = s.Q.DeleteUserSessions(r.Context(), sess.UserID)
	if err := s.startSession(w, r, sess.User); err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	web.SetFlash(w, "success", "Password changed. Other sessions were signed out.")
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}
