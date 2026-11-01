package cloudserver

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// This explicit catalog is the MCP allowlist, not an arbitrary HTTP proxy.
// Its route coverage test makes newly added portal actions visible in review.
type mcpOperation struct {
	Name, Method, Path, Description string
	Fields                          []mcpField
}
type mcpField struct {
	Name, Kind, Description string
	Required                bool
}

func sf(name, desc string) mcpField  { return mcpField{Name: name, Kind: "string", Description: desc} }
func req(name, desc string) mcpField { f := sf(name, desc); f.Required = true; return f }
func bf(name, desc string) mcpField  { return mcpField{Name: name, Kind: "boolean", Description: desc} }
func arr(name, desc string) mcpField { return mcpField{Name: name, Kind: "array", Description: desc} }
func vals(desc string) mcpField      { return mcpField{Name: "values", Kind: "object", Description: desc} }
func num(name, desc string) mcpField { return mcpField{Name: name, Kind: "integer", Description: desc} }

func mcpOperations() []mcpOperation {
	const o = "/orgs/{org}"
	const r = o + "/repos/{repo}"
	const d = r + "/deployments/{dep}"
	const t = d + "/targets/{target}"
	const run = r + "/runs/{id}"
	targetFields := []mcpField{req("name", "Target name."), sf("description", "Target description."), bf("auto_deploy", "Deploy on GitHub pushes; false by default.")}
	recipientFields := []mcpField{req("email", "Recipient email."), arr("events", "Event names from get_notifications.Categories."), bf("enabled", "Enable this recipient."), bf("include_insight", "Include AI insights for failures.")}
	aiFields := []mcpField{req("base_url", "OpenAI-compatible API base URL."), sf("api_key", "New API key; omit to retain the stored key."), sf("model", "Model identifier."), sf("model_custom", "Custom model identifier; overrides model.")}
	page := []mcpField{num("offset", "Zero-based offset; default 0."), num("limit", "Page size, 1–100; default 50.")}
	return []mcpOperation{
		{"list_organizations", "GET", "/mcp-internal/organizations", "List your organizations and current permissions.", nil},
		{"list_agent_connections", "GET", "/account/agents", "List your active OAuth agent connections, scopes and expiry dates.", nil},
		{"revoke_agent_connection", "POST", "/account/agents/revoke", "Revoke one of your agent connections, including all its access and refresh tokens.", []mcpField{req("grant_id", "Connection ID from list_agent_connections.")}},
		{"get_account", "GET", "/account", "Read your account profile.", nil},
		{"update_profile", "POST", "/account/profile", "Update your display name.", []mcpField{req("name", "New display name.")}},
		{"change_password", "POST", "/account/password", "Change your password. Revokes ALL agent connections and other browser sessions.", []mcpField{req("current_password", "Current password."), req("new_password", "New password, 10–512 characters.")}},
		{"create_organization", "POST", "/orgs/new", "Create an organization; you become its owner.", []mcpField{req("name", "Organization display name; also determines the slug.")}},
		{"get_invite", "GET", "/invites/{token}", "Inspect an organization invitation.", nil},
		{"accept_invite", "POST", "/invites/{token}/accept", "Accept an organization invitation as the authorized user.", nil},
		{"list_repositories", "GET", o, "List connected repositories in an organization.", nil},
		{"get_repository_choices", "GET", o + "/repos/new", "List available GitHub App repositories and connection options.", nil},
		{"connect_repository", "POST", o + "/repos", "Connect and sync a Git repository.", []mcpField{req("provider", "git or github_app."), sf("name", "Repository name; derived if omitted."), sf("branch", "Branch; defaults to main."), sf("git_url", "HTTPS Git URL for provider git."), sf("token", "Git access token, encrypted at rest."), sf("github_repo", "For github_app: installation_id|owner/repository from get_repository_choices.")}},
		{"list_org_runs", "GET", o + "/runs", "List all organization runs with pagination, newest first.", page},
		{"list_members", "GET", o + "/members", "List members and permissions; admins also see pending invites.", nil},
		{"invite_member", "POST", o + "/members/invite", "Create an invitation. Returns the invitation link.", []mcpField{req("email", "Invitee email."), req("role", "member or admin."), bf("can_configure", "Allow configuration."), bf("can_deploy", "Allow deployments.")}},
		{"update_member", "POST", o + "/members/{user}/update", "Update a member role and permissions under the portal's owner/admin rules.", []mcpField{req("role", "member, admin or owner (owners only)."), bf("can_configure", "Allow configuration."), bf("can_deploy", "Allow deployments.")}},
		{"remove_member", "POST", o + "/members/{user}/remove", "Remove a member from the organization.", nil},
		{"revoke_invite", "POST", o + "/invites/{invite}/revoke", "Revoke a pending invitation.", nil},
		{"get_organization_settings", "GET", o + "/settings", "Read organization settings (owner only).", nil},
		{"rename_organization", "POST", o + "/settings", "Rename the organization; its slug is unchanged.", []mcpField{req("name", "New display name.")}},
		{"delete_organization", "POST", o + "/delete", "Permanently delete a non-personal organization and its resources.", []mcpField{req("confirm_name", "Exact current organization display name to confirm deletion.")}},
		{"get_notifications", "GET", o + "/notifications", "Read SMTP configuration, recipient subscriptions, supported events and delivery history.", nil},
		{"configure_smtp", "POST", o + "/notifications/smtp", "Configure SMTP delivery (admin). Omitted password retains the existing credential.", []mcpField{req("host", "SMTP host."), req("port", "SMTP port, e.g. 587."), sf("username", "SMTP username."), sf("password", "New SMTP password."), req("encryption", "starttls, tls or none."), req("from_address", "Sender email."), sf("from_name", "Sender display name.")}},
		{"test_smtp", "POST", o + "/notifications/smtp/test", "Send a test email using saved SMTP settings.", nil},
		{"create_recipient", "POST", o + "/notifications/recipients", "Add a notification recipient and subscriptions.", recipientFields},
		{"update_recipient", "POST", o + "/notifications/recipients/{id}/update", "Replace a notification recipient's settings and subscriptions.", recipientFields},
		{"delete_recipient", "POST", o + "/notifications/recipients/{id}/delete", "Delete a notification recipient.", nil},
		{"get_ai_settings", "GET", o + "/ai", "Read AI provider settings without exposing the stored API key.", nil},
		{"configure_ai", "POST", o + "/ai", "Verify and save AI provider credentials and model.", aiFields},
		{"list_ai_models", "POST", o + "/ai/models", "Query models from the specified AI provider (full scope; contacts the provider).", aiFields},
		{"test_ai", "POST", o + "/ai/test", "Send a test prompt to the specified AI provider.", aiFields},
		{"delete_ai_settings", "POST", o + "/ai/delete", "Remove the AI provider configuration and stored key.", nil},
		{"get_repository", "GET", r, "Read repository configuration, deployments, variables, steps and targets.", nil},
		{"sync_repository", "POST", r + "/sync", "Fetch the configured branch and refresh keel.yaml.", nil},
		{"get_repository_settings", "GET", r + "/settings", "Read repository settings without exposing the Git token.", nil},
		{"update_repository", "POST", r + "/settings", "Replace repository name, branch and URL; optionally rotate or clear its Git token.", []mcpField{req("name", "Repository name."), req("branch", "Branch."), sf("git_url", "Git URL for plain Git repositories."), sf("token", "New Git token; omitted retains existing."), bf("clear_token", "Delete the stored Git token.")}},
		{"delete_repository", "POST", r + "/delete", "Delete a repository and its targets and run history.", []mcpField{req("confirm_name", "Exact current repository name.")}},
		{"list_repo_runs", "GET", r + "/runs", "List repository runs with pagination, newest first.", page},
		{"get_run", "GET", run, "Read run status, steps, input snapshots, outputs and AI insight. Use get_run_logs for paginated logs.", nil},
		{"get_run_logs", "GET", run + "/events", "Read persisted logs after a sequence number; poll for live troubleshooting. Response includes next_after_seq and has_more.", []mcpField{num("after_seq", "Exclusive log cursor; default 0."), num("limit", "Maximum lines, 1–100; default 50.")}},
		{"cancel_run", "POST", run + "/cancel", "Cancel an active run; deploy permission required.", nil},
		{"generate_run_insight", "POST", run + "/insight", "Generate or regenerate an AI explanation of a failed run.", nil},
		{"get_deployment", "GET", d, "Read deployment definition and all targets.", nil},
		{"create_target", "POST", d + "/targets", "Create a deployment target.", targetFields},
		{"get_deployment_manifest", "GET", d + "/manifest", "Generate a variable manifest in Markdown.", []mcpField{arr("var", "Selected variable names; omitted uses default selection.")}},
		{"get_target", "GET", t, "Read target settings, variable schema, saved non-secret values, secret presence and latest outputs.", nil},
		{"get_target_manifest", "GET", t + "/manifest", "Generate a target-specific variable manifest in Markdown.", []mcpField{arr("var", "Selected variable names; omitted uses default selection.")}},
		{"save_target_values", "POST", t + "/values", "Save selected configuration variables. Omitted values remain unchanged; blank non-secrets clear saved values; blank secrets retain them. Valid fields are saved even if other fields fail validation, matching the portal.", []mcpField{vals("Map of declared configuration variable names to string values; booleans use strings true/false.")}},
		{"deploy_target", "POST", t + "/deploy", "Start deployment with saved configuration and supplied deploy-time inputs. Returns the run URL. Do not retry blindly after a network timeout: check recent runs first.", []mcpField{vals("Map of deploy-time variable names to string values.")}},
		{"update_target", "POST", t + "/update", "Replace target name, description and automatic deployment setting.", targetFields},
		{"delete_target", "POST", t + "/delete", "Delete a target and its saved values. Existing run history remains.", nil},
	}
}

var pathParam = regexp.MustCompile(`\{([a-z_]+)\}`)

func (op mcpOperation) fields() []mcpField {
	fields := []mcpField{}
	for _, m := range pathParam.FindAllStringSubmatch(op.Path, -1) {
		fields = append(fields, req(m[1], "Path identifier: "+m[1]+" (use the exact name/slug or ID returned by the read tools)."))
	}
	return append(fields, op.Fields...)
}
func (op mcpOperation) schema() map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, f := range op.fields() {
		p := map[string]any{"type": f.Kind, "description": f.Description}
		switch f.Kind {
		case "string":
			p["maxLength"] = 65536
			if f.Required {
				p["minLength"] = 1
			}
		case "array":
			p["items"] = map[string]any{"type": "string"}
			p["maxItems"] = 256
		case "object":
			p["additionalProperties"] = map[string]any{"type": "string"}
			p["maxProperties"] = 256
		case "integer":
			p["minimum"] = 0
			p["maximum"] = 2147483647
			if f.Name == "limit" {
				p["minimum"] = 1
				p["maximum"] = 100
			}
		}
		props[f.Name] = p
		if f.Required {
			required = append(required, f.Name)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

// decode independently enforces the advertised schema; Server.AddTool does not.
func (op mcpOperation) decode(raw json.RawMessage) (string, url.Values, error) {
	var args map[string]json.RawMessage
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return "", nil, fmt.Errorf("arguments must be an object")
	}
	known := map[string]mcpField{}
	for _, f := range op.fields() {
		known[f.Name] = f
	}
	for k := range args {
		if _, ok := known[k]; !ok {
			return "", nil, fmt.Errorf("unknown argument %q", k)
		}
	}
	path := op.Path
	form := url.Values{}
	for _, f := range op.fields() {
		v, ok := args[f.Name]
		if !ok {
			if f.Required {
				return "", nil, fmt.Errorf("%s is required", f.Name)
			}
			continue
		}
		if string(v) == "null" {
			return "", nil, fmt.Errorf("%s cannot be null", f.Name)
		}
		switch f.Kind {
		case "string":
			var str string
			if json.Unmarshal(v, &str) != nil || len(str) > 65536 || f.Required && str == "" {
				return "", nil, fmt.Errorf("invalid string %s", f.Name)
			}
			marker := "{" + f.Name + "}"
			if strings.Contains(path, marker) {
				if len(str) > 256 || str == "." || str == ".." || strings.ContainsAny(str, "/\\\x00\r\n") {
					return "", nil, fmt.Errorf("invalid path identifier %s", f.Name)
				}
				path = strings.ReplaceAll(path, marker, url.PathEscape(str))
			} else {
				form.Set(f.Name, str)
			}
		case "boolean":
			var b bool
			if json.Unmarshal(v, &b) != nil {
				return "", nil, fmt.Errorf("%s must be boolean", f.Name)
			}
			form.Set(f.Name, strconv.FormatBool(b))
		case "integer":
			var n int64
			if json.Unmarshal(v, &n) != nil || n < 0 || n > 2147483647 || f.Name == "limit" && (n < 1 || n > 100) {
				return "", nil, fmt.Errorf("invalid integer %s", f.Name)
			}
			form.Set(f.Name, strconv.FormatInt(n, 10))
		case "array":
			var a []string
			if json.Unmarshal(v, &a) != nil || len(a) > 256 {
				return "", nil, fmt.Errorf("%s must be an array of up to 256 strings", f.Name)
			}
			form[f.Name] = a
			if f.Name == "var" {
				form.Set("sel", "1")
			}
		case "object":
			var m map[string]string
			if json.Unmarshal(v, &m) != nil || len(m) > 256 {
				return "", nil, fmt.Errorf("%s must map at most 256 names to strings", f.Name)
			}
			for k, value := range m {
				if !variableName.MatchString(k) || len(value) > 65536 {
					return "", nil, fmt.Errorf("invalid variable name or oversized value")
				}
				form.Set(k, value)
			}
		}
	}
	return path, form, nil
}

var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
