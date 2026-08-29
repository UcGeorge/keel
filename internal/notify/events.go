// Package notify defines the events Keel Cloud can announce, renders them
// as emails, and delivers them over SMTP.
//
// An organization configures an SMTP server and a list of recipients, each
// subscribed to the event kinds it cares about. The server publishes an
// Event when something happens; the dispatcher (in cloudserver) resolves
// the subscribed recipients and sends one email per event.
package notify

// Kind identifies an event type. The string form is what recipients
// subscribe to and what the delivery log records.
type Kind string

const (
	RunStarted   Kind = "run.started"
	RunSucceeded Kind = "run.succeeded"
	RunFailed    Kind = "run.failed"
	RunCanceled  Kind = "run.canceled"

	RepoConnected    Kind = "repo.connected"
	RepoSynced       Kind = "repo.synced"
	RepoDisconnected Kind = "repo.disconnected"

	TargetCreated       Kind = "target.created"
	TargetValuesChanged Kind = "target.values_changed"
	TargetDeleted       Kind = "target.deleted"

	MemberInvited Kind = "member.invited"
	MemberJoined  Kind = "member.joined"
	MemberRemoved Kind = "member.removed"
)

// Info describes one event kind for the recipients form.
type Info struct {
	Kind        Kind
	Label       string
	Description string
	Category    string
}

// Catalog lists every event kind in display order.
var Catalog = []Info{
	{RunStarted, "Deployment started", "A run was started, manually or by a push.", "Deployments"},
	{RunSucceeded, "Deployment succeeded", "Every step finished; outputs are available.", "Deployments"},
	{RunFailed, "Deployment failed", "A step failed or the run could not start — includes the last log lines.", "Deployments"},
	{RunCanceled, "Deployment canceled", "Someone canceled a run in progress.", "Deployments"},

	{RepoConnected, "Repository connected", "A repository was connected to the organization.", "Repositories"},
	{RepoSynced, "Repository synced", "keel.yaml was re-read: a manual sync, a settings change, or a push that changed the configuration.", "Repositories"},
	{RepoDisconnected, "Repository disconnected", "A repository was removed with its targets and runs.", "Repositories"},

	{TargetCreated, "Target created", "A deployment target was created.", "Targets"},
	{TargetValuesChanged, "Target variables changed", "Someone saved a target's variables.", "Targets"},
	{TargetDeleted, "Target deleted", "A deployment target was deleted.", "Targets"},

	{MemberInvited, "Member invited", "An invite link was created.", "Members"},
	{MemberJoined, "Member joined", "Someone accepted an invite.", "Members"},
	{MemberRemoved, "Member removed", "A member was removed from the organization.", "Members"},
}

// Category groups catalog entries for the form.
type Category struct {
	Name   string
	Events []Info
}

// Categories returns the catalog grouped by category, in catalog order.
func Categories() []Category {
	var out []Category
	idx := map[string]int{}
	for _, e := range Catalog {
		i, ok := idx[e.Category]
		if !ok {
			i = len(out)
			idx[e.Category] = i
			out = append(out, Category{Name: e.Category})
		}
		out[i].Events = append(out[i].Events, e)
	}
	return out
}

// Lookup returns the catalog entry for a kind string.
func Lookup(kind string) (Info, bool) {
	for _, e := range Catalog {
		if string(e.Kind) == kind {
			return e, true
		}
	}
	return Info{}, false
}

// Valid reports whether kind names a catalog entry.
func Valid(kind string) bool {
	_, ok := Lookup(kind)
	return ok
}

// Label returns the human label for a kind string, or the string itself.
func Label(kind string) string {
	if e, ok := Lookup(kind); ok {
		return e.Label
	}
	return kind
}

// Fact is one label/value pair shown in an email.
type Fact struct {
	Label string
	Value string
}

// Event is one occurrence to announce.
type Event struct {
	Kind Kind
	// OrgName is the organization the event belongs to.
	OrgName string
	// Title is the headline, e.g. "prod → client-acme failed".
	Title string
	// Summary is one plain sentence under the headline.
	Summary string
	// Error is the error Keel recorded (failed runs); rendered as a callout.
	Error string
	// Facts are the details table: repository, trigger, duration, …
	Facts []Fact
	// Inputs are the run's deploy-time values (runs only, non-secret).
	Inputs []Fact
	// LogTail holds the last log lines (failed runs).
	LogTail []string
	// Insight is the AI explanation of a failed run, in Markdown, for
	// recipients who asked for it; InsightNote explains its absence.
	Insight     string
	InsightNote string
	// Link is the absolute URL the email's button opens; LinkLabel its text.
	Link      string
	LinkLabel string
	// SettingsLink is where recipients are managed, for the footer.
	SettingsLink string
}
