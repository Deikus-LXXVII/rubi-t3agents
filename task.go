package main

// Tasks: a Bot's request for unattended work on one repository, like a cloud agent (docs/plans/t3-cloud-agents.md
// in the Rubi repo). One strong approval grants a task mandate (tk_…): one project and role on one computer, one
// new branch agent/<slug> cut from a base, until an end time (8 hours by default, at most 24) or a turn budget. Under
// it the agent works in its own copy of the repository inside its VM without asking per command; the only ref
// Rubi pushes for it is its own branch, and nothing ever merges. This file holds the record, its states and the
// mandate's checks; it does no I/O.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	taskBranchPattern  = defaultBranchPattern // agent/*
	taskDefaultTTL     = 8 * time.Hour
	taskMinTTL         = 10 * time.Minute
	taskMaxTTL         = 24 * time.Hour
	taskMaxTurns       = 500
	taskMaxPromptRunes = 16000
	taskMaxSlug        = 40
	taskMaxRefLen      = 100
	taskPushGrace      = time.Hour // after the end time, the final push may still happen
)

// taskState is where a task is. preparing: its copy and VM are being made; working: a turn runs; needs_you: the
// agent asked something; idle: the last turn ended and nobody wrote since; finishing: Rubi pushes the branch and
// opens the PR; done, failed and stopped are final.
type taskState string

const (
	taskPreparing taskState = "preparing"
	taskWorking   taskState = "working"
	taskNeedsYou  taskState = "needs_you"
	taskIdle      taskState = "idle"
	taskFinishing taskState = "finishing"
	taskDone      taskState = "done"
	taskFailed    taskState = "failed"
	taskStopped   taskState = "stopped"
)

// taskMoves lists, for each state, the states it may move to. A final state has none: more work on a finished
// task is a new task (and a new approval). finishing may go back to idle when the push or the PR failed, so a
// follow-up can fix it.
var taskMoves = map[taskState][]taskState{
	taskPreparing: {taskWorking, taskFailed, taskStopped},
	taskWorking:   {taskNeedsYou, taskIdle, taskFinishing, taskFailed, taskStopped},
	taskNeedsYou:  {taskWorking, taskFailed, taskStopped},
	taskIdle:      {taskWorking, taskFinishing, taskFailed, taskStopped},
	taskFinishing: {taskDone, taskIdle, taskFailed, taskStopped},
}

// taskIsolation is where a project's agents run: "vm" (the project's own VM, the default) or "host" (no
// container: the project's no-container option, which reaches everything on the machine). It is the project's
// setting, never the Bot's choice, and the task's approval shows it.
type taskIsolation string

const (
	isolationVM   taskIsolation = "vm"
	isolationHost taskIsolation = "host"
)

func (i taskIsolation) valid() bool { return i == isolationVM || i == isolationHost }

// taskSite is where a turn is about to run, as Rubi Home reports it now: the computer, the project and the
// project's isolation at this moment.
type taskSite struct {
	Device    string
	Project   string
	Isolation taskIsolation
}

func (s taskState) final() bool { return s == taskDone || s == taskFailed || s == taskStopped }

func (s taskState) canMove(to taskState) bool { return slices.Contains(taskMoves[s], to) }

// taskMandate is what the task's approval granted. It travels in the approval's payload (as the offer) and is kept
// in the plugin's saved state, so what the user read is what holds.
type taskMandate struct {
	ID         string `json:"id"`
	Device     string `json:"device"`
	DeviceName string `json:"device_name,omitempty"`
	Project    string `json:"project"`
	Role       string `json:"role,omitempty"`
	Base       string `json:"base,omitempty"` // "" until Rubi Home names the repository's default branch
	BaseSHA    string `json:"base_sha,omitempty"`
	Branch     string `json:"branch"`
	// Isolation the task was approved for. A task never continues under another one: a project switched between
	// its VM and no container stops the task (holds), and a record without it reads as invalid (fails closed).
	Isolation taskIsolation `json:"isolation"`
	Sister    string        `json:"sister"` // the Bot that asked; task events go to it only
	Issued    time.Time     `json:"issued"`
	Expires   time.Time     `json:"expires"`
	MaxTurns  int           `json:"max_turns,omitempty"` // 0: no turn budget
	Turns     int           `json:"turns"`
	Ended     bool          `json:"ended,omitempty"` // stopped, finished or revoked
}

// live: valid, not ended, before its end time and within its turn budget (why says which one fails).
func (m taskMandate) live(now time.Time) bool { return m.why(now) == "" }

// why says why the mandate no longer holds ("" while it does).
func (m taskMandate) why(now time.Time) string {
	switch {
	case !m.Isolation.valid():
		return "the task's record has no isolation"
	case m.Ended:
		return "the task has ended"
	case !now.Before(m.Expires):
		return "the task's time ran out at " + m.Expires.UTC().Format(time.RFC3339)
	case m.MaxTurns > 0 && m.Turns >= m.MaxTurns:
		return fmt.Sprintf("the task used its %d turns", m.MaxTurns)
	}
	return ""
}

// at says why the mandate doesn't cover a turn at site ("" when it does): another computer, another project, or the
// project's isolation changed since the approval.
func (m taskMandate) at(site taskSite) string {
	switch {
	case site.Device != m.Device:
		return "the task belongs to another computer"
	case site.Project != m.Project:
		return "the task belongs to project " + oneLine(m.Project, 64)
	case site.Isolation != m.Isolation:
		return fmt.Sprintf("the project now runs agents %s; the task was approved %s, start a new task", isolationWords(site.Isolation), isolationWords(m.Isolation))
	}
	return ""
}

func isolationWords(i taskIsolation) string {
	switch i {
	case isolationVM:
		return "in its VM"
	case isolationHost:
		return "without a container"
	}
	return "in an unknown way"
}

// allowsPush says whether Rubi may push ref for this task: only the task's own branch (as a name or as
// refs/heads/<name>), never a protected branch or the base. The turn budget doesn't apply (the work of the last
// turn is pushed too), and the push may come up to taskPushGrace after the end time, so a task whose time ran out
// while it was finishing still delivers its branch. Whoever pushes uses m.Branch as spelled here, never the
// caller's spelling.
func (m taskMandate) allowsPush(ref string, now time.Time) (bool, string) {
	if m.Ended {
		return false, "the task has ended"
	}
	if !m.Isolation.valid() {
		return false, "the task's record has no isolation"
	}
	if !now.Before(m.Expires.Add(taskPushGrace)) {
		return false, "the task's time ran out at " + m.Expires.UTC().Format(time.RFC3339)
	}
	name := strings.TrimPrefix(ref, "refs/heads/") // a tag or any other refs/… stays unequal to the branch
	if name != m.Branch {
		return false, "a task pushes only its own branch " + m.Branch + ", not " + oneLine(name, 80)
	}
	if protectedBranch(name) || (m.Base != "" && strings.EqualFold(name, m.Base)) {
		return false, "a task never pushes " + name
	}
	return true, ""
}

func protectedBranch(name string) bool {
	low := strings.ToLower(name)
	return slices.Contains(protectedBranches, low)
}

// taskStartIn is a Bot's t3agents_task_start, before any check.
type taskStartIn struct {
	Project  string `json:"project"`
	Prompt   string `json:"prompt"`
	Base     string `json:"base,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Role     string `json:"role,omitempty"`
	Expires  string `json:"expires,omitempty"` // a duration like "8h"
	MaxTurns int    `json:"max_turns,omitempty"`
}

// taskOffer is what the task's strong approval shows and grants.
type taskOffer struct {
	Project   string        `json:"project"`
	Role      string        `json:"role,omitempty"`
	Base      string        `json:"base,omitempty"`
	Branch    string        `json:"branch"`
	Expires   time.Time     `json:"expires"`
	MaxTurns  int           `json:"max_turns,omitempty"`
	Isolation taskIsolation `json:"isolation"`
}

// warning is the line the approval shows in capitals for a project without a container ("" in its VM).
func (o taskOffer) warning() string {
	if o.Isolation == isolationHost {
		return "NO CONTAINER: the agents of " + o.Project + " run on the computer itself and can reach everything on it, including other projects and your provider sign-ins."
	}
	return ""
}

// newTaskOffer checks a start request and fills in the defaults: the branch agent/<slug of the prompt>, an end
// eight hours from now. iso is the project's isolation as Rubi Home reports it.
func newTaskOffer(in taskStartIn, iso taskIsolation, now time.Time) (taskOffer, error) {
	if !iso.valid() {
		return taskOffer{}, errors.New("Rubi Home didn't say how the project's agents are isolated")
	}
	project := strings.TrimSpace(in.Project)
	if project == "" {
		return taskOffer{}, errors.New("say which project the task is for (t3agents_sessions lists them)")
	}
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		return taskOffer{}, errors.New("the task's prompt is empty")
	}
	if utf8.RuneCountInString(prompt) > taskMaxPromptRunes {
		return taskOffer{}, fmt.Errorf("the task's prompt is longer than %d characters", taskMaxPromptRunes)
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = "agent/" + taskSlug(prompt)
	}
	if err := checkTaskBranch(branch); err != nil {
		return taskOffer{}, err
	}
	base := strings.TrimSpace(in.Base)
	if base != "" {
		if err := checkRefName(base); err != nil {
			return taskOffer{}, fmt.Errorf("base: %w", err)
		}
		if strings.EqualFold(base, branch) { // one ref file on a case-insensitive disk
			return taskOffer{}, errors.New("the task's branch must be new, not its base")
		}
	}
	ttl := taskDefaultTTL
	if s := strings.TrimSpace(in.Expires); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return taskOffer{}, errors.New(`expires must be a duration like "8h" or "90m"`)
		}
		if d < taskMinTTL || d > taskMaxTTL {
			return taskOffer{}, fmt.Errorf("expires must be between %v and %v", taskMinTTL, taskMaxTTL)
		}
		ttl = d
	}
	if in.MaxTurns < 0 || in.MaxTurns > taskMaxTurns {
		return taskOffer{}, fmt.Errorf("max_turns must be between 0 (no budget) and %d", taskMaxTurns)
	}
	return taskOffer{Project: project, Role: strings.TrimSpace(in.Role), Base: base, Branch: branch,
		Expires: now.Add(ttl), MaxTurns: in.MaxTurns, Isolation: iso}, nil
}

// checkTaskBranch: a plain new branch under agent/*, never a protected name.
func checkTaskBranch(b string) error {
	if err := checkRefName(b); err != nil {
		return err
	}
	if !branchMatches(taskBranchPattern, b) {
		return fmt.Errorf("a task's branch must match %s, not %s", taskBranchPattern, oneLine(b, 60))
	}
	if protectedBranch(b) || protectedBranch(strings.TrimPrefix(b, "agent/")) {
		return fmt.Errorf("%s can't be a task's branch", oneLine(b, 60))
	}
	return nil
}

// checkRefName is the part of git check-ref-format that matters here, kept stricter: ASCII letters, digits and
// ._-/ only; no empty, dot-led or .lock component; no "..", "//" or "@{"; not led by "-" or "/", not ended by "/" or ".".
func checkRefName(r string) error {
	if r == "" || len(r) > taskMaxRefLen {
		return fmt.Errorf("a branch name must be 1 to %d characters", taskMaxRefLen)
	}
	for _, c := range r {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-/", c)) {
			return fmt.Errorf("the branch name %s has a character (%q) that isn't allowed; use letters, digits and ._-/", oneLine(r, 60), c)
		}
	}
	if strings.HasPrefix(r, "-") || strings.HasPrefix(r, "/") || strings.HasSuffix(r, "/") || strings.HasSuffix(r, ".") ||
		strings.Contains(r, "..") || strings.Contains(r, "//") || strings.Contains(r, "@{") {
		return fmt.Errorf("%s isn't a valid branch name", oneLine(r, 60))
	}
	for _, part := range strings.Split(r, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(strings.ToLower(part), ".lock") {
			return fmt.Errorf("%s isn't a valid branch name", oneLine(r, 60))
		}
	}
	return nil
}

// taskSlug makes a branch-safe name from the prompt's first words: lowercase ASCII letters and digits joined by
// "-", at most taskMaxSlug characters; "task" when nothing usable is left.
func taskSlug(prompt string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(prompt) {
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		switch {
		case ok:
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(c)
			dash = false
		default:
			dash = true
		}
		if b.Len() >= taskMaxSlug {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > taskMaxSlug {
		s = strings.TrimRight(s[:taskMaxSlug], "-")
	}
	if s == "" {
		return "task"
	}
	if protectedBranch(s) { // agent/main would be refused; agent/main-task is not
		s += "-task"
	}
	return s
}

// grantTask turns an approved offer into the mandate.
func grantTask(o taskOffer, device, deviceName, sister string, now time.Time) taskMandate {
	return taskMandate{ID: newID("tk_"), Device: device, DeviceName: deviceName, Project: o.Project, Role: o.Role,
		Base: o.Base, Branch: o.Branch, Isolation: o.Isolation, Sister: sister, Issued: now, Expires: o.Expires, MaxTurns: o.MaxTurns}
}

// task is one task as the plugin keeps it.
type task struct {
	ID       string      `json:"id"` // the mandate's id
	Mandate  taskMandate `json:"mandate"`
	ThreadID string      `json:"thread_id,omitempty"`
	State    taskState   `json:"state"`
	LastBy   string      `json:"last_by,omitempty"` // who started the last turn: "bot", "owner-t3" or "agent"
	Reason   string      `json:"reason,omitempty"`  // why it failed or stopped
	Created  time.Time   `json:"created"`
	Updated  time.Time   `json:"updated"`
}

func newTask(m taskMandate, now time.Time) task {
	return task{ID: m.ID, Mandate: m, State: taskPreparing, Created: now, Updated: now}
}

// move changes the task's state if the move is allowed. A final state ends the mandate.
func (t *task) move(to taskState, reason string, now time.Time) error {
	if !t.State.canMove(to) {
		return fmt.Errorf("task %s can't go from %s to %s", t.ID, t.State, to)
	}
	t.State, t.Updated = to, now
	t.Reason = oneLine(reason, 300) // an earlier failure's reason doesn't outlive the next move
	if to.final() {
		t.Mandate.Ended = true
	}
	return nil
}

// startTurn records a new turn by who ("bot", "owner-t3", "agent") at site: the task must be live, not final and
// still covered where the turn runs (computer, project, isolation), and a turn counts against the budget.
func (t *task) startTurn(by string, site taskSite, now time.Time) error {
	switch by {
	case "bot", "owner-t3", "agent":
	default:
		return fmt.Errorf("unknown turn origin %q", oneLine(by, 20))
	}
	if w := t.Mandate.why(now); w != "" {
		return errors.New(w)
	}
	if w := t.Mandate.at(site); w != "" {
		return errors.New(w)
	}
	if t.State != taskWorking { // a turn queued behind a running one keeps the task working
		if err := t.move(taskWorking, "", now); err != nil {
			return err
		}
	}
	t.Mandate.Turns++
	t.LastBy = by
	return nil
}
