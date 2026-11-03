package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

// fakeHost stands in for Rubi: Submit and WaitApproval give scripted approval results; HomeCall plays
// the helper.
type fakeHost struct {
	mu        sync.Mutex
	devices   []rubiplugin.HomeDevice
	submit    func(r rubiplugin.Request) (map[string]any, error)
	states    []string  // WaitApproval answers in turn; the last one repeats
	sessions  []session // played as T3 threads when no fakeAgent is set (derive_test.go)
	submits   []rubiplugin.Request
	events    []map[string]any
	ops       []string // every op called
	audits    []string
	auditLog  []map[string]any
	state     json.RawMessage
	emitToErr error // EmitTo fails (no Bot of that name with a webhook)
	logs      []string
	saveErr   error        // SaveState fails
	agent     *fakeAgent   // the t3-agent operations (nil: a Rubi Home without the capability)
	setup     *fakeSetup   // the t3-setup operations (nil: a Rubi Home without the capability)
	library   *fakeLibrary // the host method library.get (nil: none)
	derived   *fakeAgent   // the t3-agent operations played from sessions (derive_test.go)
}

// answer is what the fake Rubi Home records of a desktop-era claude.decide call (kept for the fake's helpers).
type answer struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Digest    string `json:"digest"`
	Behavior  string `json:"behavior"`
	Message   string `json:"message,omitempty"`
}

type decided struct {
	device string
	answer
}

// LibraryGet plays the host method library.get.
func (f *fakeHost) LibraryGet(_ context.Context, id string, version int, out any) error {
	if f.library == nil {
		return errors.New("library.get is not available")
	}
	v, err := f.library.get(id, version)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(v)
	return json.Unmarshal(b, out)
}

func (f *fakeHost) HomeDevices() ([]rubiplugin.HomeDevice, error) { return f.devices, nil }

func (f *fakeHost) HomeCall(ctx context.Context, device, op string, args, out any) error {
	f.mu.Lock()
	f.ops = append(f.ops, op)
	f.mu.Unlock()
	switch op {
	}
	if f.setup != nil && strings.HasPrefix(op, "t3-setup.") {
		return f.setup.call(op, args, out)
	}
	if strings.HasPrefix(op, "t3-setup.") {
		return errors.New(`Rubi Home doesn't do "` + op + `"`)
	}
	if f.agent != nil && strings.HasPrefix(op, "t3-agent.") {
		return f.agent.call(ctx, op, args, out)
	}
	if strings.HasPrefix(op, "t3-agent.") {
		if a := f.derivedAgent(); a != nil {
			return a.call(ctx, op, args, out)
		}
	}
	if strings.HasPrefix(op, "t3-agent.") {
		return errors.New(`Rubi Home doesn't do "` + op + `"`)
	}
	return errors.New("unexpected op " + op)
}

func (f *fakeHost) EmitTo(agent, typ string, data map[string]any) (string, error) {
	f.mu.Lock()
	err := f.emitToErr
	f.mu.Unlock()
	if agent != "" && err != nil {
		return "", err
	}
	data["_agent"] = agent
	return f.Emit(typ, data)
}

func (f *fakeHost) Audit(event string, fields map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, event)
	f.auditLog = append(f.auditLog, fields)
}

func (f *fakeHost) LoadState(v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.state) == 0 {
		return nil
	}
	return json.Unmarshal(f.state, v)
}

func (f *fakeHost) SaveState(v any) error {
	b, err := json.Marshal(v)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.state = b
	return err
}

func (f *fakeHost) Submit(_ context.Context, r rubiplugin.Request) (map[string]any, error) {
	f.mu.Lock()
	f.submits = append(f.submits, r)
	f.mu.Unlock()
	if f.submit != nil {
		return f.submit(r)
	}
	return map[string]any{"status": "awaiting_approval", "approval_id": "apr_1", "level": "strong",
		"approval_url": "https://rubi-panel.com/#a=apr_1"}, nil
}

func (f *fakeHost) WaitApproval(_ context.Context, id string, _ time.Duration) (rubiplugin.ApprovalState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.states[0]
	if len(f.states) > 1 {
		f.states = f.states[1:]
	}
	return rubiplugin.ApprovalState{ID: id, State: st}, nil
}

func (f *fakeHost) Emit(typ string, data map[string]any) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data["_type"] = typ
	f.events = append(f.events, data)
	return "ev_1", nil
}

func (f *fakeHost) Logf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

func newRequest(command string) request {
	in, _ := json.Marshal(map[string]any{"command": command, "description": "List files"})
	sum := sha256.Sum256(in)
	return request{ID: "cc_1", SessionID: "sess-1", ToolName: "Bash", ToolInput: string(in), Digest: hex.EncodeToString(sum[:]),
		Command: command, Description: "List files", Cwd: "/Users/alice/project", ExpiresAt: time.Now().Add(time.Minute)}
}

var mac = rubiplugin.HomeDevice{ID: "dev_mac", Name: "Mac"}

func TestManifest(t *testing.T) {
	m := manifest()
	if m.ID != "t3agents" || m.Name != "T3 Agents" || len(m.Egress) != 0 || len(m.Secrets) != 0 || len(m.Home) != 2 || m.Home[0] != "t3-agent" || m.Home[1] != "t3-setup" || m.Hooks {
		t.Fatalf("identity or permissions: %+v", m)
	}
	if m.Entry != "t3agents" || m.Source != "https://github.com/Deikus-LXXVII/rubi-t3agents" {
		t.Fatalf("entry or source: %q %q", m.Entry, m.Source)
	}
	// The desktop path is gone: no desktop capability, no desktop action.
	for _, c := range m.Home {
		if c == "claude" || c == "claude-sessions" || c == "claude-start" {
			t.Errorf("manifest still asks for the desktop capability %q", c)
		}
	}
	for _, a := range m.Actions {
		if !strings.HasPrefix(a.Kind, "t3agents.") {
			t.Errorf("action kind %q is not under the plugin's id", a.Kind)
		}
	}
	if opts := func() []rubiplugin.Option { a, _ := m.Action(kindRun); return a.Options }(); len(opts) != 2 || opts[0].Key != optionAllow || opts[1].Key != optionTrust {
		t.Errorf("a run approval offers only one command or the 2-hour trust: %+v", opts)
	}
	run, _ := m.Action(kindRun)
	safe, _ := m.Action(kindSafe)
	if run.DefaultLevel != rubiplugin.Strong || !run.Locked || safe.DefaultLevel != rubiplugin.Tap || safe.Locked {
		t.Fatalf("levels: %+v %+v", run, safe)
	}
	ev, ok := m.Event(eventNeeded)
	if !ok || len(ev.Untrusted) == 0 {
		t.Fatalf("event: %+v", ev)
	}
	// Rubi refuses an event the manifest doesn't declare.
	for _, typ := range []string{eventBound, eventReply, eventAsk, eventMessage} {
		if _, ok := m.Event(typ); !ok {
			t.Errorf("event %s isn't declared in the manifest", typ)
		}
	}
}

// The tool names are the t3agents_ family and no tool keeps the old prefix.
func TestToolNames(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"t3agents_start", "t3agents_send", "t3agents_status", "t3agents_sessions", "t3agents_bind", "t3agents_unbind", "t3agents_fork", "t3agents_mode", "t3agents_rename", "t3agents_mandates", "t3agents_trust_project", "t3agents_project_add"} {
		if !strings.Contains(string(src), `"`+name+`"`) {
			t.Errorf("tool %s is not registered", name)
		}
	}
	rs, err := os.ReadFile("roles_tools.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rs), `"t3agents_role_create"`) || !strings.Contains(string(rs), `"t3agents_role_update"`) {
		t.Error("the role tools are not t3agents_role_*")
	}
	all, _ := filepath.Glob("*.go")
	for _, f := range all {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "claude_code_") {
			t.Errorf("%s still names a claude_code_ tool", f)
		}
	}
}

// The plugin talks to Rubi Home only in the t3-agent and t3-setup families, and the watcher polls only the
// t3-agent queue.
func TestWatcherCallsOnlyT3Ops(t *testing.T) {
	f, s, w := agentFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.run(ctx); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := slices.Contains(f.ops, opAgentNext)
		f.mu.Unlock()
		if got {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	_ = s
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.ops, opAgentNext) {
		t.Fatalf("the t3-agent queue was never polled: %v", f.ops)
	}
	for _, op := range f.ops {
		if !strings.HasPrefix(op, "t3-agent.") && !strings.HasPrefix(op, "t3-setup.") {
			t.Errorf("a call to %q, which is not a t3 op", op)
		}
	}
}

// TestTrustOptionIsHonest: the 2-hour trust says builds and tests run the project's own code, and
// makes no promise the command-line check can't keep (no network or writes outside at all).
func TestTrustOptionIsHonest(t *testing.T) {
	m := manifest()
	run, _ := m.Action(kindRun)
	var meaning string
	for _, o := range run.Options {
		if o.Key == optionTrust {
			meaning = o.Meaning
		}
	}
	for _, want := range []string{"builds and tests", "project's own code", "still ask", ".git", "Revoke"} {
		if !strings.Contains(meaning, want) {
			t.Errorf("trust meaning lacks %q: %s", want, meaning)
		}
	}
	if strings.Contains(meaning, "never push, --force, sudo, network") {
		t.Errorf("trust meaning still promises no network at all: %s", meaning)
	}
}

// toolPattern is core's rule for a tool name, copied from internal/plugins/manifest.go in the Rubi core
// (the regexp toolPattern, next to idPattern and namePattern). Keep it the same as core's.
var toolPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// toolPrefix is rubiplugin.ToolPrefix(id) from the SDK (strings.ReplaceAll(id, "-", "_") + "_"); core requires
// every tool name of a plugin to start with it. Written out here so the check does not depend on the SDK having it.
func toolPrefix(id string) string { return strings.ReplaceAll(id, "-", "_") + "_" }

// manifestLikeCore is the manifest as `--manifest` prints it: the plugin built as main builds it, with every tool
// registered, so the tool list is the one core sees.
func manifestLikeCore(t *testing.T) rubiplugin.Manifest {
	t.Helper()
	m := newPlugin().Manifest()
	var round rubiplugin.Manifest
	b, err := json.Marshal(m)
	if err != nil || json.Unmarshal(b, &round) != nil {
		t.Fatal("manifest does not round-trip through JSON", err)
	}
	return round
}

// Core refuses a plugin whose tool does not start with the id-derived prefix or does not match the tool pattern,
// and one whose action kind does not start with id+".".
func TestToolsAndKindsFollowCoresRules(t *testing.T) {
	m := manifestLikeCore(t)
	if toolPrefix(m.ID) != rubiplugin.ToolPrefix(m.ID) {
		t.Fatalf("local prefix %q differs from the SDK's %q", toolPrefix(m.ID), rubiplugin.ToolPrefix(m.ID))
	}
	checkTools(t, m.ID, toolNames(m))
	for _, a := range m.Actions {
		if !strings.HasPrefix(a.Kind, m.ID+".") {
			t.Errorf("action kind %q must start with %q", a.Kind, m.ID+".")
		}
	}
	if len(m.Tools) < 30 {
		t.Errorf("only %d tools in the manifest: the registered tools are missing", len(m.Tools))
	}
}

func toolNames(m rubiplugin.Manifest) []string {
	var out []string
	for _, tl := range m.Tools {
		out = append(out, tl.Name)
	}
	return out
}

func checkTools(t *testing.T, id string, names []string) (bad int) {
	t.Helper()
	prefix := toolPrefix(id)
	for _, n := range names {
		if !strings.HasPrefix(n, prefix) || !toolPattern.MatchString(n) {
			t.Errorf("tool %q must start with %q and match %s", n, prefix, toolPattern)
			bad++
		}
	}
	return bad
}

// The check itself fails on a bad name (it is not vacuous).
func TestToolRuleCheckCatchesABadName(t *testing.T) {
	for _, n := range []string{"t3_sessions", "t3agents-sessions", "T3agents_x", "t3agents_" + strings.Repeat("a", 64), "sessions"} {
		if strings.HasPrefix(n, toolPrefix("t3agents")) && toolPattern.MatchString(n) {
			t.Errorf("%q passes the rule", n)
		}
	}
	if !toolPattern.MatchString("t3agents_role_create") || !strings.HasPrefix("t3agents_role_create", toolPrefix("t3agents")) {
		t.Error("a good name fails the rule")
	}
}
