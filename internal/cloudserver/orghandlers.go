package cloudserver

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/smart-minds/keel/internal/auth"
	"github.com/smart-minds/keel/internal/store/clouddb"
	"github.com/smart-minds/keel/internal/web"
)

func nullUUID(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: true} }

// --- create organization -----------------------------------------------------

func (s *Server) handleOrgNewPage(w http.ResponseWriter, r *http.Request, sess *sessionInfo) {
	page := web.PageOrgNew{Base: s.base(w, r, sess, nil, "New organization")}
	s.Renderer.Render(w, http.StatusOK, "cloud/org_new.html", page)
}

func (s *Server) handleOrgNew(w http.ResponseWriter, r *http.Request, sess *sessionInfo) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	renderErr := func(msg string) {
		page := web.PageOrgNew{Base: s.base(w, r, sess, nil, "New organization"), Name: name, Error: msg}
		s.Renderer.Render(w, http.StatusUnprocessableEntity, "cloud/org_new.html", page)
	}
	if name == "" {
		renderErr("The organization name is required.")
		return
	}
	slug := auth.Slugify(name)
	if !auth.ValidSlug(slug) {
		renderErr("Pick a name with at least two letters or digits.")
		return
	}
	org, err := s.Q.CreateOrg(r.Context(), clouddb.CreateOrgParams{
		Slug: slug, Name: name, Personal: false, CreatedBy: nullUUID(sess.UserID),
	})
	if err != nil {
		renderErr(fmt.Sprintf("The name %q is taken (an organization already uses the URL /orgs/%s).", name, slug))
		return
	}
	if _, err := s.Q.CreateOrgMember(r.Context(), clouddb.CreateOrgMemberParams{
		OrgID: org.ID, UserID: sess.UserID, Role: "owner", CanConfigure: true, CanDeploy: true,
	}); err != nil {
		s.errorPage(w, r, sess, http.StatusInternalServerError, "Could not create the organization")
		return
	}
	web.SetFlash(w, "success", "Organization created — connect a repository to get started.")
	http.Redirect(w, r, "/orgs/"+org.Slug, http.StatusSeeOther)
}

// --- members -----------------------------------------------------------------

func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	s.renderMembers(w, r, oc, "", "", http.StatusOK)
}

func (s *Server) renderMembers(w http.ResponseWriter, r *http.Request, oc *orgCtx, inviteLink, errMsg string, code int) {
	rows, err := s.Q.ListOrgMembers(r.Context(), oc.Org.ID)
	if err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not load members")
		return
	}
	page := web.PageMembers{
		Base:       s.base(w, r, oc.Sess, oc, "Members"),
		CanManage:  oc.isAdmin(),
		IsOwner:    oc.isOwner(),
		InviteLink: inviteLink,
		Error:      errMsg,
	}
	for _, m := range rows {
		vm := web.MemberVM{
			UserID: m.UserID.String(), Name: m.Name, Email: m.Email,
			Role: m.Role, CanConfigure: m.CanConfigure, CanDeploy: m.CanDeploy,
			IsSelf: m.UserID == oc.Sess.UserID,
		}
		// Owners can edit everyone but themselves; admins only members.
		switch {
		case vm.IsSelf:
			vm.Editable = false
		case oc.isOwner():
			vm.Editable = true
		case oc.isAdmin() && m.Role == "member":
			vm.Editable = true
		}
		page.Members = append(page.Members, vm)
	}
	if oc.isAdmin() {
		invites, err := s.Q.ListOrgInvites(r.Context(), oc.Org.ID)
		if err == nil {
			for _, inv := range invites {
				page.Invites = append(page.Invites, web.InviteVM{
					ID: inv.ID.String(), Email: inv.Email, Role: inv.Role, ExpiresAt: inv.ExpiresAt,
				})
			}
		}
	}
	s.Renderer.Render(w, code, "cloud/members.html", page)
}

// handleMemberInvite creates an invite link.
func (s *Server) handleMemberInvite(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !oc.isAdmin() {
		s.errorPage(w, r, oc.Sess, http.StatusForbidden, "Only owners and admins can invite members")
		return
	}
	email := auth.NormalizeEmail(r.PostFormValue("email"))
	role := r.PostFormValue("role")
	canConfigure := r.PostFormValue("can_configure") == "true"
	canDeploy := r.PostFormValue("can_deploy") == "true"

	if !auth.ValidEmail(email) {
		s.renderMembers(w, r, oc, "", "Enter a valid email address for the invite.", http.StatusUnprocessableEntity)
		return
	}
	if role != "member" && role != "admin" {
		s.renderMembers(w, r, oc, "", "Pick a valid role.", http.StatusUnprocessableEntity)
		return
	}
	if role == "admin" && !oc.isOwner() {
		s.renderMembers(w, r, oc, "", "Only the owner can invite admins.", http.StatusUnprocessableEntity)
		return
	}
	if role == "admin" {
		canConfigure, canDeploy = true, true
	}
	if user, err := s.Q.GetUserByEmail(r.Context(), email); err == nil {
		if _, err := s.Q.GetOrgMember(r.Context(), clouddb.GetOrgMemberParams{OrgID: oc.Org.ID, UserID: user.ID}); err == nil {
			s.renderMembers(w, r, oc, "", email+" is already a member of this organization.", http.StatusUnprocessableEntity)
			return
		}
	}

	token, tokenHash, err := auth.NewToken()
	if err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not create the invite")
		return
	}
	if _, err := s.Q.CreateInvite(r.Context(), clouddb.CreateInviteParams{
		OrgID: oc.Org.ID, Email: email, Role: role,
		CanConfigure: canConfigure, CanDeploy: canDeploy,
		TokenHash: tokenHash, InvitedBy: nullUUID(oc.Sess.UserID),
		ExpiresAt: time.Now().Add(14 * 24 * time.Hour),
	}); err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not create the invite")
		return
	}
	link := s.Cfg.BaseURL + "/invites/" + token
	s.renderMembers(w, r, oc, link, "", http.StatusOK)
}

// handleMemberUpdate changes a member's role and scopes.
func (s *Server) handleMemberUpdate(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	target, ok := s.editableMember(w, r, oc)
	if !ok {
		return
	}
	role := r.PostFormValue("role")
	canConfigure := r.PostFormValue("can_configure") == "true"
	canDeploy := r.PostFormValue("can_deploy") == "true"

	validRoles := map[string]bool{"member": true}
	if oc.isOwner() {
		validRoles["admin"] = true
		validRoles["owner"] = true
	}
	if !validRoles[role] {
		s.errorPage(w, r, oc.Sess, http.StatusForbidden, "You cannot assign that role")
		return
	}
	if role != "member" {
		canConfigure, canDeploy = true, true
	}
	if _, err := s.Q.UpdateOrgMember(r.Context(), clouddb.UpdateOrgMemberParams{
		OrgID: oc.Org.ID, UserID: target.UserID, Role: role,
		CanConfigure: canConfigure, CanDeploy: canDeploy,
	}); err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not update the member")
		return
	}
	web.SetFlash(w, "success", "Member updated.")
	http.Redirect(w, r, oc.urlBase()+"/members", http.StatusSeeOther)
}

// handleMemberRemove removes a member from the organization.
func (s *Server) handleMemberRemove(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	target, ok := s.editableMember(w, r, oc)
	if !ok {
		return
	}
	if err := s.Q.DeleteOrgMember(r.Context(), clouddb.DeleteOrgMemberParams{OrgID: oc.Org.ID, UserID: target.UserID}); err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not remove the member")
		return
	}
	web.SetFlash(w, "success", "Member removed.")
	http.Redirect(w, r, oc.urlBase()+"/members", http.StatusSeeOther)
}

// editableMember loads the member in the path and enforces who may edit whom.
func (s *Server) editableMember(w http.ResponseWriter, r *http.Request, oc *orgCtx) (*clouddb.OrgMember, bool) {
	userID := parseUUID(r.PathValue("user"))
	if userID == uuid.Nil {
		s.errorPage(w, r, oc.Sess, http.StatusNotFound, "Member not found")
		return nil, false
	}
	target, err := s.Q.GetOrgMember(r.Context(), clouddb.GetOrgMemberParams{OrgID: oc.Org.ID, UserID: userID})
	if err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusNotFound, "Member not found")
		return nil, false
	}
	switch {
	case target.UserID == oc.Sess.UserID:
		s.errorPage(w, r, oc.Sess, http.StatusForbidden, "You cannot change your own membership")
		return nil, false
	case oc.isOwner():
		// Owners can edit everyone else; but never demote the last owner.
		if target.Role == "owner" {
			owners, err := s.Q.CountOrgOwners(r.Context(), oc.Org.ID)
			if err != nil || owners <= 1 {
				s.errorPage(w, r, oc.Sess, http.StatusForbidden, "The organization must keep at least one owner")
				return nil, false
			}
		}
		return target, true
	case oc.isAdmin() && target.Role == "member":
		return target, true
	default:
		s.errorPage(w, r, oc.Sess, http.StatusForbidden, "You don't have permission to manage this member")
		return nil, false
	}
}

// handleInviteRevoke deletes a pending invite.
func (s *Server) handleInviteRevoke(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !oc.isAdmin() {
		s.errorPage(w, r, oc.Sess, http.StatusForbidden, "Only owners and admins can revoke invites")
		return
	}
	id := parseUUID(r.PathValue("invite"))
	if err := s.Q.DeleteInvite(r.Context(), clouddb.DeleteInviteParams{ID: id, OrgID: oc.Org.ID}); err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not revoke the invite")
		return
	}
	web.SetFlash(w, "success", "Invite revoked.")
	http.Redirect(w, r, oc.urlBase()+"/members", http.StatusSeeOther)
}

// --- settings ----------------------------------------------------------------

func (s *Server) handleOrgSettings(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !oc.isOwner() {
		s.errorPage(w, r, oc.Sess, http.StatusForbidden, "Only the owner can manage organization settings")
		return
	}
	page := web.PageOrgSettings{
		Base:    s.base(w, r, oc.Sess, oc, "Organization settings"),
		OrgName: oc.Org.Name, Personal: oc.Org.Personal, IsOwner: true,
	}
	s.Renderer.Render(w, http.StatusOK, "cloud/org_settings.html", page)
}

func (s *Server) handleOrgSettingsSave(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !oc.isOwner() {
		s.errorPage(w, r, oc.Sess, http.StatusForbidden, "Only the owner can manage organization settings")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		web.SetFlash(w, "error", "The organization name is required.")
		http.Redirect(w, r, oc.urlBase()+"/settings", http.StatusSeeOther)
		return
	}
	if _, err := s.Q.UpdateOrgName(r.Context(), clouddb.UpdateOrgNameParams{ID: oc.Org.ID, Name: name}); err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not rename the organization")
		return
	}
	web.SetFlash(w, "success", "Organization renamed.")
	http.Redirect(w, r, oc.urlBase()+"/settings", http.StatusSeeOther)
}

func (s *Server) handleOrgDelete(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !oc.isOwner() {
		s.errorPage(w, r, oc.Sess, http.StatusForbidden, "Only the owner can delete the organization")
		return
	}
	if oc.Org.Personal {
		s.errorPage(w, r, oc.Sess, http.StatusForbidden, "Personal organizations cannot be deleted")
		return
	}
	if r.PostFormValue("confirm_name") != oc.Org.Name {
		web.SetFlash(w, "error", "Type the organization name exactly to confirm deletion.")
		http.Redirect(w, r, oc.urlBase()+"/settings", http.StatusSeeOther)
		return
	}
	if err := s.Q.DeleteOrg(r.Context(), oc.Org.ID); err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not delete the organization")
		return
	}
	web.SetFlash(w, "success", "Organization deleted.")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
