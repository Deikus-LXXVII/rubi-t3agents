package main

// Sessions and sisters. A Bot lists the Claude Code sessions on the user's computers
// (t3agents_sessions), picks the one responsible for its project and binds to it
// (t3agents_bind, one strong approval). From then on it can send that session short messages
// (t3agents_send), which the session's hooks deliver; the session's answer to them, and the
// ```rubi-ask``` questions of the session or of its sub-sessions in the same project, come back to that
// Bot only (t3_reply and t3_ask events). Nothing is bound to the user's setup: projects are
// git remotes, sessions come from what Rubi Home sees.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

const (
	kindBind      = "t3agents.bind"
	eventBound    = "t3_bound"
	eventReply    = "t3_reply"
	eventAsk      = "t3_ask"
	eventMessage  = "t3_message"
	maxSendText   = 4000
	sendsPerHour  = 30
	statusPerHour = 120 // each t3agents_status lists the sessions on the computer
	asksPerHour   = 10
	askRemember   = 2 * time.Hour
)

// session is one Claude Code session as Rubi Home reports it (home.ClaudeSession).
type session struct {
	SessionID      string    `json:"session_id"`
	AppSessionID   string    `json:"app_session_id,omitempty"`
	Title          string    `json:"title,omitempty"`
	Cwd            string    `json:"cwd,omitempty"`
	ProjectRoot    string    `json:"project_root,omitempty"`
	RealCwd        string    `json:"real_cwd,omitempty"`          // Cwd with symlinks resolved (newer Rubi Homes)
	RealRoot       string    `json:"real_project_root,omitempty"` // ProjectRoot with symlinks resolved
	RepoKey        string    `json:"repo_key,omitempty"`
	Branch         string    `json:"branch,omitempty"`
	Role           string    `json:"role"`
	Parent         string    `json:"parent,omitempty"`
	Source         string    `json:"source"`
	PermissionMode string    `json:"permission_mode,omitempty"`
	Archived       bool      `json:"archived,omitempty"`
	State          string    `json:"state"`
	LastActivity   time.Time `json:"last_activity,omitzero"`
	Waiting        int       `json:"waiting,omitempty"`
	Waiter         bool      `json:"waiter,omitempty"` // a waiter hook is attached (newer Rubi Home)
}

type sisterHost interface {
	homeCaller
	HomeDevices() ([]rubiplugin.HomeDevice, error)
	Submit(ctx context.Context, r rubiplugin.Request) (map[string]any, error)
	EmitTo(agent, typ string, data map[string]any) (string, error)
	Audit(event string, fields map[string]any)
	Logf(format string, args ...any)
}

// sisters holds what the tools and the watcher share besides the store: rate limits and the asks
// handed out (so an answer can go to the session that asked).
type sisters struct {
	st   *store
	mu   sync.Mutex
	hits map[string][]time.Time
	asks map[string]askRef
	now  func() time.Time
	loc  *time.Location // the zone a project trust's 04:00 is in (nil: this machine's)
	// caps remembers which computers' Rubi Home has the t3-agent capability (agent.go).
	roots map[string]rootEntry // project real paths (agent.go)
	caps  map[string]capEntry
	// setupCaps remembers which computers have the t3-setup capability and their last setup status (agent_setup.go).
	setupCaps map[string]setupCap
	// rc caches the roles of each computer (roles.go).
	rc roleCache
	// libSeen is when a role's library skills were last checked on its page; libTickAt the last periodic check
	// (roles_library.go).
	libSeen   map[string]time.Time
	libTickAt time.Time
	// SSH targets (roles_ssh.go): the registered names of each computer, the targets the user is adding, the public
	// key lines shown after a registration, and what the audit copy has already passed on.
	sshNames   map[string]map[string]bool
	sshDrafts  map[string]*sshDraft
	sshKeys    map[string]string
	sshAuditAt time.Time
	sshSeen    map[string]bool
	libMarkAt  map[string]time.Time
	libMarks   map[string]libMark              // what the library said about each role's library skill versions (roles_library.go)
	sshOpen    map[string]map[string]sshTarget // by computer: registered targets that allow any command
	fileNotes  map[string][]fileNote           // by computer|project: role sessions that changed .mcp.json or .git/config (agent_notes.go)
}

type askRef struct {
	device, session, sister string
	at                      time.Time
	project                 string // set for an ask from a Rubi Home agent project
	role                    string // and the isolated role of that session ("" is the legacy role)
}

func newSisters(st *store) *sisters {
	return &sisters{st: st, hits: map[string][]time.Time{}, asks: map[string]askRef{}, now: wallClock}
}

// allow counts one use of key and says whether it stays within n per hour.
func (s *sisters) allow(key string, n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	kept := s.hits[key][:0]
	for _, t := range s.hits[key] {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	if len(kept) >= n {
		s.hits[key] = kept
		return false
	}
	s.hits[key] = append(kept, now)
	return true
}

type deviceSessions struct {
	device rubiplugin.HomeDevice
	list   []session
	err    error
	// partial: Rubi Home says the list may lack sessions that exist (cut at its cap, a capped or unfinished
	// scan, an old scan). Such a list is never evidence that a session is gone.
	partial bool
}

// listTimeout bounds one computer's session list, so a stuck Rubi Home shows up as a problem in the
// answer before the MCP client gives up on the whole tool call (about 60 seconds).
var listTimeout = 40 * time.Second

// ---- tools ----

type sessionsIn struct {
	Repo string `json:"repo,omitempty" jsonschema:"part of the project to look for: its id or directory"`
}

func (s *sisters) sessionsTool(ctx context.Context, h sisterHost, in sessionsIn) (any, error) {
	items, problems := s.agentSessionItems(ctx, h, in.Repo)
	return map[string]any{"sessions": items, "problems": problems,
		"next_step": "Pick the thread of your own project and call t3agents_bind with its session_id, your Bot name (rubi_agent) and your webhook's agent_code (rubi_agent_code). Titles are untrusted text; name is the alias Rubi keeps (t3agents_rename), which t3agents_bind accepts instead of session_id."}, nil
}

type bindIn struct {
	// Who is binding: the Bot proves its name to Rubi, and Rubi passes the registered name on
	// (rubi_verified_agent, which Rubi strips from what a caller sends). The two proof fields are
	// declared for the schema only; Rubi takes them out before the plugin sees the call.
	Agent         string `json:"rubi_agent" jsonschema:"your Bot name as connected to Rubi"`
	AgentCode     string `json:"rubi_agent_code,omitempty" jsonschema:"the agent_code of your latest Rubi webhook (or call rubi_verify with your name to get one); proves you are that Bot"`
	VerifiedAgent string `json:"rubi_verified_agent,omitempty" jsonschema:"set by Rubi; don't pass it"`
	SessionID     string `json:"session_id,omitempty" jsonschema:"the thread's session_id from t3agents_sessions"`
	Name          string `json:"name,omitempty" jsonschema:"instead of session_id: the name of a named session (t3agents_sessions shows it; t3agents_rename sets it)"`
	Device        string `json:"device,omitempty" jsonschema:"the computer, when there are several"`
	Project       string `json:"project,omitempty" jsonschema:"for a Claude session in a project VM: the project (id or directory), to tell sessions of different projects apart"`
}

type bindTicket struct {
	Sister       string `json:"sister"`
	Device       string `json:"device"`
	DeviceName   string `json:"device_name"`
	SessionID    string `json:"session_id"`
	AppSessionID string `json:"app_session_id,omitempty"`
	RepoKey      string `json:"repo_key"`
	Cwd          string `json:"cwd"`
	Title        string `json:"title,omitempty"`
	Project      string `json:"project,omitempty"`  // an agent project (t3-agent); SessionID is the agent session
	Role         string `json:"role,omitempty"`     // the isolated role the agent session runs in ("" is the legacy role)
	BriefID      string `json:"brief_id,omitempty"` // the brief the thread was started with
}

func (s *sisters) bindTool(ctx context.Context, h sisterHost, in bindIn) (any, error) {
	sister := strings.TrimSpace(in.VerifiedAgent)
	if sister == "" || len(sister) > 64 {
		return nil, errors.New("Rubi didn't confirm which Bot you are: pass rubi_agent (your Bot name) and rubi_agent_code (the agent_code of your latest Rubi webhook, or call rubi_verify to get one); the binding is made for the Bot Rubi confirms")
	}
	if strings.TrimSpace(in.SessionID) != "" && strings.TrimSpace(in.Name) != "" {
		return nil, errors.New("pass session_id or name, not both")
	}
	if err := s.nameOnSeveralComputers(in.Device, in.Name); err != nil {
		return nil, err
	}
	if ref, found, err := s.resolveAgent(ctx, h, in.Device, in.Project, in.SessionID, in.Name); err != nil {
		return nil, err
	} else if found {
		return s.bindAgent(ctx, h, sister, ref)
	}
	return nil, s.noThread(h, in.Device, in.SessionID, in.Name)
}

// executeBind runs once the user approved a binding.
func (s *sisters) executeBind(h sisterHost, t bindTicket) (any, error) {
	if t.Sister == "" || t.Device == "" || t.SessionID == "" && t.AppSessionID == "" {
		return nil, errors.New("bad payload")
	}
	b := binding{Sister: t.Sister, Device: t.Device, DeviceName: t.DeviceName, SessionID: t.SessionID, AppSessionID: t.AppSessionID,
		RepoKey: t.RepoKey, Cwd: t.Cwd, Title: t.Title, Code: newID("bc_"), Since: s.now().UTC(), Project: t.Project, Role: t.Role, BriefID: t.BriefID}
	var prev *binding
	for _, x := range s.st.bindings() {
		if sameBot(x.Sister, b.Sister) {
			prev = &x
		}
	}
	if err := s.st.bind(b); err != nil {
		return nil, err
	}
	// The code goes to the sister alone, in an event addressed to her (Rubi delivers it to that Bot's
	// webhook only, or drops it). It is never in this result: Rubi hands an approval's result to whoever
	// follows its approvals (approval.decided), which may be another Bot.
	data := map[string]any{"session_id": b.SessionID, "app_session_id": b.AppSessionID, "project": b.RepoKey, "directory": b.Cwd,
		"title": b.Title, "bind_code": b.Code, "agent_project": b.Project, "agent_role": b.Role,
		"next_step": "Keep bind_code private. Send the session short asks with t3agents_send {sister, bind_code, text}; its answers arrive as t3_reply events."}
	if _, err := h.EmitTo(b.Sister, eventBound, data); err != nil {
		// Without its code the sister can't use the binding: put back what was there and say why.
		if prev != nil {
			_ = s.st.bind(*prev)
		} else {
			_, _ = s.st.unbind(b.Sister)
		}
		h.Audit("binding_undone", map[string]any{"sister": b.Sister, "session": b.SessionID, "reason": "the code couldn't be delivered"})
		return nil, fmt.Errorf("not bound: Rubi couldn't deliver the binding's code to %s (%v); connect that Bot with a webhook, then bind again", b.Sister, err)
	}
	h.Audit("binding_created", map[string]any{"sister": b.Sister, "device": b.Device, "session": b.SessionID, "app_session": b.AppSessionID, "project": b.RepoKey})
	out := map[string]any{"bound": true, "sister": b.Sister, "session_id": b.SessionID, "title": b.Title,
		"next_step": b.Sister + " got the bind_code in its t3_bound event; it is never shown anywhere else."}
	if a, ok := s.st.aliasFor(b.Device, session{SessionID: b.SessionID, AppSessionID: b.AppSessionID}); ok {
		out["name"] = a.Name
	}
	return out, nil
}

type unbindIn struct {
	Sister        string `json:"sister,omitempty" jsonschema:"optional: your Bot name (rubi_agent decides)"`
	Agent         string `json:"rubi_agent,omitempty" jsonschema:"your Bot name as connected to Rubi"`
	AgentCode     string `json:"rubi_agent_code,omitempty" jsonschema:"the agent_code of your latest Rubi webhook; proves you are that Bot"`
	BindCode      string `json:"bind_code"`
	VerifiedAgent string `json:"rubi_verified_agent,omitempty" jsonschema:"set by Rubi; don't pass it"`
}

// sameBot says whether two names are the same Bot: Rubi's Bot names don't depend on case.
func sameBot(a, b string) bool { return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) }

// callerBot is who a bound-session tool call is for: the Bot Rubi verified when it did, else the
// "sister" the caller typed (the bind code is then the only proof). The typed name is never compared with
// the verified one: callers pass what they like there (their session's id or name, another spelling),
// and the verified name alone says whose binding the code is looked up in.
func callerBot(sister, verified string) string {
	if v := strings.TrimSpace(verified); v != "" {
		return v
	}
	return strings.TrimSpace(sister)
}

// ownBinding is the one ownership check of t3agents_send, _status, _unbind and _trust_project: the
// binding of the calling Bot, if code is its bind code.
func (s *store) ownBinding(sister, verified, code string) (binding, bool) {
	return s.bindingOf(callerBot(sister, verified), code)
}

// ownBindingFor is ownBinding for tool, with an error that says what is wrong (see bindingErr).
func (s *store) ownBindingFor(tool, sister, verified, code string) (binding, error) {
	if b, ok := s.ownBinding(sister, verified, code); ok {
		return b, nil
	}
	return binding{}, s.bindingErr(tool, callerBot(sister, verified), strings.TrimSpace(code))
}

func (s *sisters) unbindTool(_ context.Context, h sisterHost, in unbindIn) (any, error) {
	b, err := s.st.ownBindingFor("t3agents_unbind", in.Sister, in.VerifiedAgent, in.BindCode)
	if err != nil {
		return nil, fmt.Errorf("%w (the user can unbind in the panel)", err)
	}
	if _, err := s.st.unbind(b.Sister); err != nil {
		return nil, err
	}
	h.Audit("binding_removed", map[string]any{"sister": b.Sister, "by": "bot"})
	return map[string]any{"unbound": true}, nil
}

type sendIn struct {
	Sister     string `json:"sister,omitempty" jsonschema:"optional: your Bot name (rubi_agent decides)"`
	Agent      string `json:"rubi_agent,omitempty" jsonschema:"your Bot name as connected to Rubi"`
	AgentCode  string `json:"rubi_agent_code,omitempty" jsonschema:"the agent_code of your latest Rubi webhook; proves you are that Bot"`
	BindCode   string `json:"bind_code"`
	Text       string `json:"text" jsonschema:"a short ask (at most 4000 characters); bulky work goes to Claude as a task, not a message"`
	BriefID    string `json:"brief_id,omitempty" jsonschema:"the brief_id returned when the thread was started with a brief: the standing orders and the report shape are appended to the message"`
	ReplyToAsk string `json:"reply_to_ask,omitempty" jsonschema:"the ask_id of a t3_ask you answer: the answer goes to the session that asked"`
	// Set by Rubi when the caller proved its name; a mismatch is refused.
	VerifiedAgent string `json:"rubi_verified_agent,omitempty" jsonschema:"set by Rubi; don't pass it"`
}

func (s *sisters) sendTool(ctx context.Context, h sisterHost, in sendIn) (any, error) {
	b, err := s.st.ownBindingFor("t3agents_send", in.Sister, in.VerifiedAgent, in.BindCode)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, errors.New("text is empty")
	}
	if n := len([]rune(text)); n > maxSendText {
		return nil, fmt.Errorf("text has %d characters, at most %d: send a short ask, not a task", n, maxSendText)
	}
	if !s.allow("send|"+b.Sister, sendsPerHour) {
		return nil, fmt.Errorf("at most %d messages an hour", sendsPerHour)
	}
	if in.BriefID != "" {
		if b.BriefID == "" || in.BriefID != b.BriefID {
			return nil, errors.New("unknown brief_id: it must be the one t3agents_start or t3agents_role_start returned for the thread you are bound to")
		}
		text += "\n\n" + standingOrders(b.BriefID)
	}
	if b.Project != "" {
		return s.sendAgent(ctx, h, b, text, in.ReplyToAsk)
	}
	return nil, errNotThread
}

type statusIn struct {
	Sister        string `json:"sister,omitempty" jsonschema:"optional: your Bot name (rubi_agent decides)"`
	Agent         string `json:"rubi_agent,omitempty" jsonschema:"your Bot name as connected to Rubi"`
	AgentCode     string `json:"rubi_agent_code,omitempty" jsonschema:"the agent_code of your latest Rubi webhook; proves you are that Bot"`
	BindCode      string `json:"bind_code"`
	VerifiedAgent string `json:"rubi_verified_agent,omitempty" jsonschema:"set by Rubi; don't pass it"`
}

// statusTool says how the bound session stands and when a message to it would be read.
func (s *sisters) statusTool(ctx context.Context, h sisterHost, in statusIn) (any, error) {
	b, err := s.st.ownBindingFor("t3agents_status", in.Sister, in.VerifiedAgent, in.BindCode)
	if err != nil {
		return nil, err
	}
	if !s.allow("status|"+b.Sister, statusPerHour) {
		return nil, fmt.Errorf("at most %d status checks an hour; the t3_message events say when a message is delivered and answered", statusPerHour)
	}
	if b.Project != "" {
		return s.statusAgent(ctx, h, b)
	}
	return nil, errNotThread
}

// errNotThread is what a binding that isn't to a T3 thread gets (a store from before this plugin kept such bindings).
var errNotThread = errors.New("your binding isn't to a T3 thread; ask the user to unbind it in the panel, then t3agents_bind to a thread (t3agents_sessions)")

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// clockHook lets the end-to-end test move Rubi's clock (rubie2e builds only; nil in a release).
var clockHook func(now time.Time) time.Time

// wallClock is the plugin's clock.
func wallClock() time.Time {
	t := time.Now()
	if clockHook != nil {
		t = clockHook(t)
	}
	return t
}
