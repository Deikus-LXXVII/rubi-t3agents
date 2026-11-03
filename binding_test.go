package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

var mainSess = session{SessionID: "cli-1", AppSessionID: "cli-1", Title: "Rubi main", Cwd: "/Users/alice/rubi", RepoKey: "github.com/alice/rubi", Role: "main", Source: "desktop", PermissionMode: "default", State: "idle"}
var subSess = session{SessionID: "cli-2", AppSessionID: "cli-2", Title: "Rubi sub", Cwd: "/Users/alice/rubi/.claude/worktrees/x", RepoKey: "github.com/alice/rubi", Role: "sub", Parent: "cli-1", Source: "desktop", State: "busy"}
var otherSess = session{SessionID: "cli-3", Title: "Other", Cwd: "/Users/alice/other", RepoKey: "github.com/alice/other", Role: "main", Source: "transcript", PermissionMode: "bypassPermissions", State: "idle"}

func boundFixture(t *testing.T) (*fakeHost, *sisters, string) {
	t.Helper()
	f := &fakeHost{devices: []rubiplugin.HomeDevice{mac}, sessions: []session{mainSess, subSess, otherSess}}
	s := newSisters(&store{})
	if err := s.st.load(f); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.bindTool(ctx, f, bindIn{VerifiedAgent: "Alice", SessionID: "cli-1"}); err != nil {
		t.Fatal(err)
	}
	if len(f.submits) != 1 || f.submits[0].Kind != kindBind {
		t.Fatalf("bind didn't ask for approval: %+v", f.submits)
	}
	raw, _ := json.Marshal(f.submits[0].Payload)
	var tk bindTicket
	_ = json.Unmarshal(raw, &tk)
	res, err := s.executeBind(f, tk)
	if err != nil {
		t.Fatal(err)
	}
	// The approval's result goes to whoever follows approvals: the code must never be in it.
	if raw, _ := json.Marshal(res); strings.Contains(string(raw), "bc_") || strings.Contains(string(raw), `"bind_code"`) {
		t.Fatalf("the approval result carries the bind code: %s", raw)
	}
	ev := f.events[len(f.events)-1]
	if ev["_type"] != eventBound || ev["_agent"] != "Alice" {
		t.Fatalf("the code didn't go to the sister alone: %+v", ev)
	}
	code := ev["bind_code"].(string)
	return f, s, code
}

func TestBindOneBotPerSession(t *testing.T) {
	f, s, code := boundFixture(t)
	ctx := context.Background()
	if _, err := s.bindTool(ctx, f, bindIn{VerifiedAgent: "Other", SessionID: "cli-1"}); err == nil {
		t.Fatal("a second Bot could ask to bind the same session")
	}
	if _, err := s.unbindTool(ctx, f, unbindIn{Sister: "Alice", BindCode: "nope"}); err == nil {
		t.Fatal("unbind without the code")
	}
	if _, err := s.unbindTool(ctx, f, unbindIn{Sister: "Alice", BindCode: code}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sendTool(ctx, f, sendIn{Sister: "Alice", BindCode: code, Text: "hi"}); err == nil {
		t.Fatal("sent after unbinding")
	}
}

func TestSendRateLimit(t *testing.T) {
	f, s, code := boundFixture(t)
	for i := range sendsPerHour {
		if _, err := s.sendTool(context.Background(), f, sendIn{Sister: "Alice", BindCode: code, Text: "x"}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if _, err := s.sendTool(context.Background(), f, sendIn{Sister: "Alice", BindCode: code, Text: "x"}); err == nil {
		t.Fatal("no rate limit")
	}
}

func TestPanel(t *testing.T) {
	f, s, _ := boundFixture(t)
	now := time.Now()
	_ = s.st.issue(mandate{ID: "md_x", Device: mac.ID, SessionID: "s", Cwd: "/p", Expires: now.Add(time.Hour), MaxUses: 5}, now)
	v, err := s.panel(context.Background(), f, "view", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if !strings.Contains(string(b), "md_x") || !strings.Contains(string(b), "Alice") {
		t.Fatalf("view: %s", b)
	}
	if _, err := s.panel(context.Background(), f, "revoke", json.RawMessage(`{"id":"md_x"}`)); err != nil || len(s.st.mandates(now)) != 0 {
		t.Fatal("revoke from the panel failed")
	}
	if _, err := s.panel(context.Background(), f, "unbind", json.RawMessage(`{"sister":"Alice"}`)); err != nil || len(s.st.bindings()) != 0 {
		t.Fatal("unbind from the panel failed")
	}
	// Unbinding what isn't bound changes nothing, so the audit log doesn't claim a removal.
	removed := func() int {
		return len(slices.DeleteFunc(slices.Clone(f.audits), func(a string) bool { return a != "binding_removed" }))
	}
	n := removed()
	if v, err := s.panel(context.Background(), f, "unbind", json.RawMessage(`{"sister":"Alice"}`)); err != nil || v.(map[string]any)["unbound"] != false || removed() != n {
		t.Fatalf("unbinding nothing: %v %v, removals %d -> %d", v, err, n, removed())
	}
}

// The binding is made for the Bot Rubi verified, never for a name the caller typed; a call Rubi verified
// as another Bot can't use someone else's binding.
func TestBindNeedsTheVerifiedBot(t *testing.T) {
	f, s, code := boundFixture(t)
	ctx := context.Background()
	n := len(f.submits)
	if _, err := s.bindTool(ctx, f, bindIn{Agent: "Mallory", SessionID: "cli-3"}); err == nil || !strings.Contains(err.Error(), "rubi_agent_code") {
		t.Fatalf("a self-declared name bound: %v", err)
	}
	if len(f.submits) != n {
		t.Fatal("an unverified bind asked the user")
	}
	if _, err := s.bindTool(ctx, f, bindIn{Agent: "Mallory", VerifiedAgent: "Vega", SessionID: "cli-3"}); err != nil {
		t.Fatal(err)
	}
	var tk bindTicket
	raw, _ := json.Marshal(f.submits[len(f.submits)-1].Payload)
	_ = json.Unmarshal(raw, &tk)
	if tk.Sister != "Vega" || f.submits[len(f.submits)-1].Preview.(map[string]any)["bot"] != "Vega" {
		t.Fatalf("bound for %q, not the verified Bot", tk.Sister)
	}
	if _, err := s.sendTool(ctx, f, sendIn{Sister: "Alice", BindCode: code, Text: "hi", VerifiedAgent: "Vega"}); err == nil {
		t.Fatal("another verified Bot used Alice's binding")
	}
	if _, err := s.sendTool(ctx, f, sendIn{Sister: "Alice", BindCode: code, Text: "hi", VerifiedAgent: "alice"}); err != nil {
		t.Fatalf("the binding's own Bot: %v", err)
	}
	if _, err := s.unbindTool(ctx, f, unbindIn{Sister: "Alice", BindCode: code, VerifiedAgent: "Vega"}); err == nil {
		t.Fatal("another verified Bot unbound Alice")
	}
}

// TestBindUndoneWhenTheCodeCantBeDelivered: when the addressed t3_bound event can't reach the
// sister, the binding isn't made (or the earlier one stays) and the error carries no code.
func TestBindUndoneWhenTheCodeCantBeDelivered(t *testing.T) {
	f, s, code := boundFixture(t)
	ctx := context.Background()
	f.mu.Lock()
	f.emitToErr = errors.New("no Bot named Alice is connected")
	f.mu.Unlock()
	if _, err := s.bindTool(ctx, f, bindIn{VerifiedAgent: "Alice", SessionID: "cli-3"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.submits[len(f.submits)-1].Payload)
	var tk bindTicket
	_ = json.Unmarshal(raw, &tk)
	res, err := s.executeBind(f, tk)
	if err == nil || res != nil || strings.Contains(err.Error(), "bc_") {
		t.Fatalf("undelivered bind: %v %v", res, err)
	}
	bs := s.st.bindings()
	if len(bs) != 1 || bs[0].SessionID != "cli-1" || bs[0].Code != code || !slices.Contains(f.audits, "binding_undone") {
		t.Fatalf("the earlier binding should stay: %+v %v", bs, f.audits)
	}

	// A first binding that can't be delivered leaves nothing behind.
	if _, err := s.unbindTool(ctx, f, unbindIn{Sister: "Alice", BindCode: code, VerifiedAgent: "Alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.executeBind(f, tk); err == nil || len(s.st.bindings()) != 0 {
		t.Fatalf("a binding without a delivered code was kept: %v %+v", err, s.st.bindings())
	}
}

// TestSisterNameIgnoresCase: Rubi's Bot names don't depend on case, so "alice" uses Alice's binding;
// the code is still what proves it, and another name is still another Bot.
func TestSisterNameIgnoresCase(t *testing.T) {
	f, s, code := boundFixture(t)
	ctx := context.Background()
	for _, name := range []string{"alice", " ALICE "} {
		if _, err := s.sendTool(ctx, f, sendIn{Sister: name, BindCode: code, Text: "hi", VerifiedAgent: "Alice"}); err != nil {
			t.Fatalf("%q: %v", name, err)
		}
	}
	if _, err := s.sendTool(ctx, f, sendIn{Sister: "alice", BindCode: "bc_wrong", Text: "hi"}); err == nil {
		t.Fatal("a wrong code went through")
	}
	if _, err := s.sendTool(ctx, f, sendIn{Sister: "Alicene", BindCode: code, Text: "hi"}); err == nil {
		t.Fatal("another Bot's name went through")
	}
	if _, err := s.unbindTool(ctx, f, unbindIn{Sister: "ALICE", BindCode: code, VerifiedAgent: "alice"}); err != nil || len(s.st.bindings()) != 0 {
		t.Fatalf("unbind by another case: %v %+v", err, s.st.bindings())
	}
}

// bindAs binds bot to a session through the approval, as the user would approve it.
func bindAs(t *testing.T, f *fakeHost, s *sisters, bot, sessionID string) {
	t.Helper()
	if _, err := s.bindTool(context.Background(), f, bindIn{VerifiedAgent: bot, SessionID: sessionID}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.submits[len(f.submits)-1].Payload)
	var tk bindTicket
	_ = json.Unmarshal(raw, &tk)
	if _, err := s.executeBind(f, tk); err != nil {
		t.Fatal(err)
	}
}

// A Bot's own binding is its own whatever the "sister" argument says: callers pass the session's id or
// name (or nothing, or another spelling) there, and only the Bot Rubi verified and the code decide.
// t3agents_status and _send used to answer "this binding belongs to another Bot" for them.
func TestOwnBindingIgnoresTypedSisterWhenVerified(t *testing.T) {
	f, s, code := boundFixture(t)
	ctx := context.Background()
	if _, err := s.renameTool(ctx, f, renameIn{VerifiedAgent: "Alice", Name: "Rubi core"}); err != nil {
		t.Fatal(err)
	}
	for _, typed := range []string{"", "cli-1", "cli-1", "Rubi core", "rubi core", "Rubi main"} {
		if _, err := s.statusTool(ctx, f, statusIn{Sister: typed, BindCode: code, VerifiedAgent: "Alice"}); err != nil {
			t.Fatalf("status with sister %q: %v", typed, err)
		}
		if _, err := s.sendTool(ctx, f, sendIn{Sister: typed, BindCode: code, Text: "hi", VerifiedAgent: " alice "}); err != nil {
			t.Fatalf("send with sister %q: %v", typed, err)
		}
	}
	// Still refused: another verified Bot, a wrong code, and nobody verified with a typed name that isn't the Bot's.
	if _, err := s.statusTool(ctx, f, statusIn{Sister: "Alice", BindCode: code, VerifiedAgent: "Vega"}); err == nil {
		t.Fatal("another verified Bot read Alice's status")
	}
	if _, err := s.statusTool(ctx, f, statusIn{Sister: "cli-1", BindCode: "bc_wrong", VerifiedAgent: "Alice"}); err == nil {
		t.Fatal("a wrong code went through")
	}
	if _, err := s.statusTool(ctx, f, statusIn{Sister: "cli-1", BindCode: code}); err == nil {
		t.Fatal("an unverified call with a non-Bot name went through")
	}
	if _, err := s.unbindTool(ctx, f, unbindIn{Sister: "Rubi core", BindCode: code, VerifiedAgent: "Alice"}); err != nil || len(s.st.bindings()) != 0 {
		t.Fatalf("unbind: %v %+v", err, s.st.bindings())
	}
}
