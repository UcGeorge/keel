package cloudserver

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/UcGeorge/keel/internal/auth"
	"github.com/UcGeorge/keel/internal/notify"
	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/UcGeorge/keel/internal/web"
)

// notifyURL is the notifications page of an organization.
func (oc *orgCtx) notifyURL() string { return oc.urlBase() + "/notifications" }

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !s.requireAdmin(w, r, oc, "notifications") {
		return
	}
	s.renderNotifications(w, r, oc, nil, web.RecipientVM{}, "", http.StatusOK)
}

func (s *Server) renderNotifications(w http.ResponseWriter, r *http.Request, oc *orgCtx, smtpForm *web.SMTPFormVM, newRecipient web.RecipientVM, newErr string, code int) {
	ctx := r.Context()
	page := web.PageNotifications{
		Base:         s.base(w, r, oc.Sess, oc, "Notifications"),
		URLBase:      oc.urlBase(),
		NewRecipient: newRecipient,
		NewError:     newErr,
	}
	if smtpForm != nil {
		page.SMTP = *smtpForm
	} else {
		page.SMTP = s.smtpFormVM(ctx, oc)
	}
	if page.SMTP.Errors == nil {
		page.SMTP.Errors = map[string]string{}
	}
	if page.NewRecipient.Events == nil {
		page.NewRecipient.Events = map[string]bool{}
	}
	_, _, page.AIConfigured = s.aiClient(ctx, oc.Org.ID)
	page.AIURL = oc.aiURL()
	for _, c := range notify.Categories() {
		cat := web.EventCategoryVM{Name: c.Name}
		for _, e := range c.Events {
			cat.Events = append(cat.Events, web.EventInfoVM{Kind: string(e.Kind), Label: e.Label, Description: e.Description})
		}
		page.Categories = append(page.Categories, cat)
	}
	members := map[string]bool{}
	if rows, err := s.Q.ListOrgMembers(ctx, oc.Org.ID); err == nil {
		for _, m := range rows {
			members[strings.ToLower(m.Email)] = true
			page.MemberEmails = append(page.MemberEmails, m.Email)
		}
	}
	if rows, err := s.Q.ListRecipients(ctx, oc.Org.ID); err == nil {
		for _, rcpt := range rows {
			vm := web.RecipientVM{ID: rcpt.ID.String(), Email: rcpt.Email, Enabled: rcpt.Enabled, Events: map[string]bool{}, IsMember: members[strings.ToLower(rcpt.Email)], IncludeInsight: rcpt.IncludeInsight}
			for _, k := range rcpt.Events {
				if notify.Valid(k) {
					vm.Events[k] = true
					vm.Labels = append(vm.Labels, notify.Label(k))
				}
			}
			vm.Count = len(vm.Events)
			page.Recipients = append(page.Recipients, vm)
		}
	}
	if rows, err := s.Q.ListDeliveries(ctx, clouddb.ListDeliveriesParams{OrgID: oc.Org.ID, Limit: 25}); err == nil {
		for _, d := range rows {
			page.Deliveries = append(page.Deliveries, web.DeliveryVM{
				Event: notify.Label(d.Event), Subject: d.Subject, Recipients: strings.Join(d.Recipients, ", "),
				Status: d.Status, Error: d.Error, CreatedAt: d.CreatedAt,
			})
		}
	}
	s.Renderer.Render(w, code, "cloud/notifications.html", page)
}

func (s *Server) smtpFormVM(ctx context.Context, oc *orgCtx) web.SMTPFormVM {
	row, err := s.Q.GetOrgSMTP(ctx, oc.Org.ID)
	if err != nil {
		return web.SMTPFormVM{Port: "587", Encryption: notify.EncryptionStartTLS, Errors: map[string]string{}}
	}
	return web.SMTPFormVM{
		Configured: true, Host: row.Host, Port: strconv.Itoa(int(row.Port)), Username: row.Username,
		HasPassword: len(row.PasswordEnc) > 0, Encryption: row.Encryption,
		FromAddress: row.FromAddress, FromName: row.FromName,
		LastTestAt: row.LastTestAt, LastTestError: row.LastTestError, Errors: map[string]string{},
	}
}

// handleSMTPSave validates and stores the mail server settings.
func (s *Server) handleSMTPSave(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !s.requireAdmin(w, r, oc, "notifications") {
		return
	}
	form := web.SMTPFormVM{
		Host: strings.TrimSpace(r.PostFormValue("host")), Port: strings.TrimSpace(r.PostFormValue("port")),
		Username: strings.TrimSpace(r.PostFormValue("username")), Encryption: r.PostFormValue("encryption"),
		FromAddress: strings.TrimSpace(r.PostFormValue("from_address")), FromName: strings.TrimSpace(r.PostFormValue("from_name")),
		Errors: map[string]string{},
	}
	port, _ := strconv.Atoi(form.Port)
	cfg := notify.SMTP{Host: form.Host, Port: port, Username: form.Username, Encryption: form.Encryption, From: form.FromAddress, FromName: form.FromName}
	if errs := cfg.Validate(); len(errs) > 0 {
		form.Errors = errs
		existing, err := s.Q.GetOrgSMTP(r.Context(), oc.Org.ID)
		form.Configured = err == nil
		form.HasPassword = err == nil && len(existing.PasswordEnc) > 0
		s.renderNotifications(w, r, oc, &form, web.RecipientVM{}, "", http.StatusUnprocessableEntity)
		return
	}
	var passwordEnc []byte
	if pw := r.PostFormValue("password"); pw != "" {
		enc, err := s.Box.SealString(pw)
		if err != nil {
			s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not encrypt the password")
			return
		}
		passwordEnc = enc
	} else if existing, err := s.Q.GetOrgSMTP(r.Context(), oc.Org.ID); err == nil {
		passwordEnc = existing.PasswordEnc // blank keeps the saved one
	}
	if form.Username == "" {
		passwordEnc = nil
	}
	if err := s.Q.UpsertOrgSMTP(r.Context(), clouddb.UpsertOrgSMTPParams{
		OrgID: oc.Org.ID, Host: form.Host, Port: int32(port), Username: form.Username, PasswordEnc: passwordEnc,
		Encryption: form.Encryption, FromAddress: form.FromAddress, FromName: form.FromName, UpdatedBy: nullUUID(oc.Sess.UserID),
	}); err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not save the mail server settings")
		return
	}
	web.SetFlash(w, "success", "Mail server saved — send yourself a test email to confirm it works.")
	http.Redirect(w, r, oc.notifyURL(), http.StatusSeeOther)
}

// handleSMTPTest sends a test email to the signed-in user.
func (s *Server) handleSMTPTest(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !s.requireAdmin(w, r, oc, "notifications") {
		return
	}
	cfg, err := s.smtpConfig(r.Context(), oc.Org.ID)
	if err != nil {
		web.SetFlash(w, "error", "Save the mail server settings first.")
		http.Redirect(w, r, oc.notifyURL(), http.StatusSeeOther)
		return
	}
	ev := s.orgEvent(oc.Org, "test", "Test email from Keel Cloud",
		"If you are reading this, the mail server for "+oc.Org.Name+" is configured correctly.",
		s.orgLink(oc.Org, "/notifications"), "Open notification settings",
		notify.Fact{Label: "Server", Value: cfg.Host + ":" + strconv.Itoa(cfg.Port) + " (" + cfg.Encryption + ")"},
		notify.Fact{Label: "Sent to", Value: oc.Sess.User.Email},
	)
	sendErr := cfg.Send(r.Context(), []string{oc.Sess.User.Email}, ev.Subject(), ev.Text(), ev.HTML())
	msg := ""
	if sendErr != nil {
		msg = sendErr.Error()
	}
	_ = s.Q.SetOrgSMTPTest(r.Context(), clouddb.SetOrgSMTPTestParams{OrgID: oc.Org.ID, LastTestError: msg})
	if sendErr != nil {
		web.SetFlash(w, "error", "Test email failed: "+msg)
	} else {
		web.SetFlash(w, "success", "Test email sent to "+oc.Sess.User.Email+".")
	}
	http.Redirect(w, r, oc.notifyURL(), http.StatusSeeOther)
}

// recipientForm reads the shared recipient fields.
func recipientForm(r *http.Request) (email string, events []string, enabled, includeInsight bool, selected map[string]bool) {
	_ = r.ParseForm()
	email = auth.NormalizeEmail(r.PostFormValue("email"))
	selected = map[string]bool{}
	for _, k := range r.PostForm["events"] {
		if notify.Valid(k) && !selected[k] {
			selected[k] = true
			events = append(events, k)
		}
	}
	if events == nil {
		events = []string{}
	}
	enabled = r.PostFormValue("enabled") == "true"
	includeInsight = r.PostFormValue("include_insight") == "true"
	return
}

func (s *Server) handleRecipientCreate(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !s.requireAdmin(w, r, oc, "notifications") {
		return
	}
	email, events, _, includeInsight, selected := recipientForm(r)
	form := web.RecipientVM{Email: email, Events: selected, IncludeInsight: includeInsight}
	if !auth.ValidEmail(email) {
		s.renderNotifications(w, r, oc, nil, form, "Enter a valid email address.", http.StatusUnprocessableEntity)
		return
	}
	if len(events) == 0 {
		s.renderNotifications(w, r, oc, nil, form, "Tick at least one event for this recipient.", http.StatusUnprocessableEntity)
		return
	}
	if _, err := s.Q.CreateRecipient(r.Context(), clouddb.CreateRecipientParams{
		OrgID: oc.Org.ID, Email: email, Events: events, Enabled: true, IncludeInsight: includeInsight, CreatedBy: nullUUID(oc.Sess.UserID),
	}); err != nil {
		s.renderNotifications(w, r, oc, nil, form, email+" is already a recipient — edit it in the list above.", http.StatusUnprocessableEntity)
		return
	}
	web.SetFlash(w, "success", email+" will receive "+strconv.Itoa(len(events))+" event type(s).")
	http.Redirect(w, r, oc.notifyURL(), http.StatusSeeOther)
}

func (s *Server) handleRecipientUpdate(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !s.requireAdmin(w, r, oc, "notifications") {
		return
	}
	rcpt, err := s.Q.GetRecipient(r.Context(), clouddb.GetRecipientParams{ID: parseUUID(r.PathValue("id")), OrgID: oc.Org.ID})
	if err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusNotFound, "Recipient not found")
		return
	}
	email, events, enabled, includeInsight, _ := recipientForm(r)
	if !auth.ValidEmail(email) {
		web.SetFlash(w, "error", "Enter a valid email address.")
		http.Redirect(w, r, oc.notifyURL(), http.StatusSeeOther)
		return
	}
	if err := s.Q.UpdateRecipient(r.Context(), clouddb.UpdateRecipientParams{
		ID: rcpt.ID, OrgID: oc.Org.ID, Email: email, Events: events, Enabled: enabled, IncludeInsight: includeInsight,
	}); err != nil {
		web.SetFlash(w, "error", "Another recipient already uses "+email+".")
		http.Redirect(w, r, oc.notifyURL(), http.StatusSeeOther)
		return
	}
	web.SetFlash(w, "success", "Recipient updated.")
	http.Redirect(w, r, oc.notifyURL(), http.StatusSeeOther)
}

func (s *Server) handleRecipientDelete(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	if !s.requireAdmin(w, r, oc, "notifications") {
		return
	}
	if err := s.Q.DeleteRecipient(r.Context(), clouddb.DeleteRecipientParams{ID: parseUUID(r.PathValue("id")), OrgID: oc.Org.ID}); err != nil {
		s.errorPage(w, r, oc.Sess, http.StatusInternalServerError, "Could not remove the recipient")
		return
	}
	web.SetFlash(w, "success", "Recipient removed.")
	http.Redirect(w, r, oc.notifyURL(), http.StatusSeeOther)
}
