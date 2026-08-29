package web

import (
	"html/template"
	"time"
)

// RunsTableVM feeds the runs_table partial.
type RunsTableVM struct {
	Runs       []RunVM
	ShowRepo   bool
	ShowTarget bool
	Poll       bool
	PollURL    string
}

// HasInputs reports whether any listed run carries deploy-time chips, so
// tables without a deployment column can add one for them.
func (t RunsTableVM) HasInputs() bool {
	for _, r := range t.Runs {
		if len(r.Inputs) > 0 {
			return true
		}
	}
	return false
}

// LogLineVM is one persisted log line for initial run-page render.
type LogLineVM struct {
	Seq  int64
	Line string
}

// PageDashboard is the Keel Dev home page.
type PageDashboard struct {
	Base
	Config      ConfigStatusVM
	Deployments []*DeploymentVM
	ConfigPath  string
}

// PageDeployment is the deployment detail page (dev and cloud).
type PageDeployment struct {
	Base
	Dep          *DeploymentVM
	CanConfigure bool
	CanDeploy    bool
	ShowAuto     bool
	TargetForm   *TargetFormVM
	BackURL      string
	BackLabel    string
}

// PageTarget is the target detail page (dev and cloud).
type PageTarget struct {
	Base
	Dep    *DeploymentVM
	Target TargetVM
	Fields []VarFieldVM
	Layout VarLayoutVM
	// Deploy-time variables, asked for in a modal when a deploy starts.
	DeployFields []VarFieldVM
	DeployLayout VarLayoutVM
	// DeployOpen re-opens the modal on load (after a validation error).
	DeployOpen bool
	// LatestOutputs are the outputs of the most recent successful run.
	LatestOutputs    []OutputVM
	LatestOutputsRun *RunVM
	Runs             RunsTableVM
	CanConfigure     bool
	CanDeploy        bool
	SaveURL          string
	DeployURL        string
	DeleteURL        string
	ManifestURL      string
	EditForm         *TargetFormVM
	ShowAuto         bool
	// Problems lists deploy blockers (missing/invalid variables).
	Problems []string
	BackURL  string
}

// PageRun is the run detail page (dev and cloud).
type PageRun struct {
	Base
	Run     RunVM
	Steps   []StepVM
	Inputs  *RunInputsVM
	Outputs []OutputVM
	// Insight is the AI-insight card for failed runs (cloud); nil hides it.
	Insight   *InsightCardVM
	LogLines  []LogLineVM
	LastSeq   int64
	EventsURL string
	CanCancel bool
	Live      bool
	BackURL   string
}

// PageRuns is a runs listing page.
type PageRuns struct {
	Base
	Heading string
	Table   RunsTableVM
}

// PageManifest is the manifest builder page.
type PageManifest struct {
	Base
	Dep        *DeploymentVM
	TargetName string
	Selected   map[string]bool
	Preview    template.HTML
	Markdown   string
	FormAction string
	BackURL    string
	Error      string
}

// PageConfig is the Keel Dev configuration page.
type PageConfig struct {
	Base
	Config  ConfigStatusVM
	RawYAML string
	Path    string
}

// PageError is the shared error page.
type PageError struct {
	Base
	Code    int
	Message string
	HomeURL string
}

// --- Cloud page data ---------------------------------------------------------

// RepoVM is a connected repository as templates see it.
type RepoVM struct {
	ID             string
	Name           string
	Provider       string // "git" | "github_app"
	GitURL         string
	Branch         string
	GithubFullName string
	Status         string // pending | ok | config_missing | config_invalid | error
	ConfigError    string
	LastCommitSHA  string
	LastSyncedAt   *time.Time
	URL            string
	Deployments    int
}

// PageLogin is the sign-in page.
type PageLogin struct {
	Base
	Email string
	Error string
	Next  string
}

// PageSignup is the registration page.
type PageSignup struct {
	Base
	Name   string
	Email  string
	Next   string
	Errors map[string]string
}

// PageInvite is the invite acceptance page.
type PageInvite struct {
	Base
	OrgName   string
	Role      string
	Token     string
	NeedsAuth bool
	Error     string
}

// PageOrgNew is the create-organization page.
type PageOrgNew struct {
	Base
	Name  string
	Error string
}

// PageRepos is the organization home: connected repositories.
type PageRepos struct {
	Base
	Repos        []RepoVM
	CanConfigure bool
	ConnectURL   string
}

// GithubPickVM is one selectable repository from a GitHub App installation.
type GithubPickVM struct {
	FullName       string
	InstallationID int64
}

// PageRepoNew is the connect-repository page.
type PageRepoNew struct {
	Base
	Name          string
	GitURL        string
	Branch        string
	Errors        map[string]string
	GithubEnabled bool
	GithubRepos   []GithubPickVM
	GithubError   string
	InstallURL    string
	FormAction    string
}

// PageRepo is the repository overview page.
type PageRepo struct {
	Base
	Repo         RepoVM
	Config       ConfigStatusVM
	Deployments  []*DeploymentVM
	CanConfigure bool
	SyncURL      string
}

// PageRepoSettings is the repository settings page.
type PageRepoSettings struct {
	Base
	Repo      RepoVM
	Errors    map[string]string
	HasToken  bool
	DeleteURL string
}

// MemberVM is one organization member row.
type MemberVM struct {
	UserID       string
	Name         string
	Email        string
	Role         string
	CanConfigure bool
	CanDeploy    bool
	IsSelf       bool
	// Editable reports whether the viewer may change this member.
	Editable bool
}

// InviteVM is one pending invite row.
type InviteVM struct {
	ID        string
	Email     string
	Role      string
	Link      string
	ExpiresAt time.Time
}

// PageMembers is the organization members page.
type PageMembers struct {
	Base
	Members    []MemberVM
	Invites    []InviteVM
	CanManage  bool
	IsOwner    bool
	InviteLink string // set right after creating an invite
	Error      string
}

// PageOrgSettings is the organization settings page.
type PageOrgSettings struct {
	Base
	OrgName  string
	Personal bool
	IsOwner  bool
	Error    string
}

// PageAccount is the user account page.
type PageAccount struct {
	Base
	Name    string
	Email   string
	Errors  map[string]string
	Success string
}

// --- AI insights (cloud) ----------------------------------------------------

// AIPresetVM is one provider shortcut on the AI settings page.
type AIPresetVM struct {
	Name    string
	BaseURL string
	Hint    string
}

// PageAI is the organization AI settings page.
type PageAI struct {
	Base
	URLBase    string
	Configured bool
	BaseURL    string
	Model      string
	HasKey     bool
	VerifiedAt *time.Time
	Presets    []AIPresetVM
	Error      string
	// ModelPicker and TestResult seed the fragments on first render.
	ModelPicker AIModelsVM
	TestResult  AITestVM
	SaveEnabled bool
}

// AIModelsVM is the model picker fragment.
type AIModelsVM struct {
	Models  []string
	Current string
	Error   string
	// Hidden reports that non-chat models were filtered out.
	Hidden int
}

// AITestVM is the connectivity test fragment.
type AITestVM struct {
	OK    bool
	Model string
	Reply string
	Error string
}

// InsightVM is a stored AI explanation of a failed run.
type InsightVM struct {
	Content   template.HTML
	Model     string
	CreatedAt time.Time
	CreatedBy string
	// Auto marks an insight generated for a failure email rather than on
	// request.
	Auto bool
}

// InsightCardVM drives the insight card on a failed run's page.
type InsightCardVM struct {
	Insight *InsightVM
	// URL generates (or regenerates) the insight; empty when AI insights
	// are not set up.
	URL string
	// SetupURL points admins at the AI settings page when unconfigured.
	SetupURL string
	Error    string
	CSRF     string
}
