package main

// Trusts, mandates and checks are keyed on the project's real path (the role VMs mount a project at the same path
// on the computer), as t3-agent.projects reports it. A path may hold spaces.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const spacedDir = "/home/alice/my projects/app one"

func spacedFixture(t *testing.T) (*fakeHost, *sisters, *watcher) {
	t.Helper()
	f, s, w := agentFixture(t)
	f.agent.projects = []agentProject{{ID: "p1", Dir: spacedDir, VMState: "running"}}
	return f, s, w
}

func askIn(id, tool, cwd string, in map[string]any, mode string) agentItem {
	b, _ := json.Marshal(in)
	return agentItem{Type: "perm.ask", AskID: id, Project: "p1", SessionID: "s-1", Tool: tool, Input: b, Cwd: cwd, Mode: mode, ExpiresAt: time.Now().Add(time.Minute)}
}

func TestMandateIsKeyedOnTheRealPathWithSpaces(t *testing.T) {
	f, s, w := spacedFixture(t)
	ctx := context.Background()
	f.states = []string{"denied"}
	w.handleAsk(ctx, mac, askIn("a", "Bash", spacedDir+"/src", map[string]any{"command": "make test"}, "default"))
	payload, _ := json.Marshal(lastSubmit(t, f).Payload)
	var tk ticket
	_ = json.Unmarshal(payload, &tk)
	if tk.Cwd != spacedDir {
		t.Fatalf("the ticket is keyed on %q, not the project's real path", tk.Cwd)
	}
	if _, err := runApproved(ctx, f, s, optionTrust, payload); err != nil {
		t.Fatal(err)
	}
	if ms := s.st.mandates(s.now()); len(ms) != 1 || ms[0].Cwd != spacedDir || ms[0].Project != "p1" {
		t.Fatalf("mandates %+v", ms)
	}
	asked := func(it agentItem) bool {
		n := len(f.submits)
		w.handleAsk(ctx, mac, it)
		return len(f.submits) > n
	}
	for _, c := range []agentItem{
		askIn("b", "Bash", spacedDir, map[string]any{"command": "go test ./..."}, "default"),
		askIn("c", "Bash", spacedDir+"/pkg/x", map[string]any{"command": "cat '" + spacedDir + "/README.md'"}, "default"),
		askIn("d", "Edit", spacedDir, map[string]any{"file_path": spacedDir + "/src/a.go"}, "default"),
	} {
		if asked(c) {
			t.Errorf("%s %s asked under the trust", c.Tool, c.Cwd)
		}
	}
	for name, c := range map[string]agentItem{
		"/work, the old constant":    askIn("e", "Bash", "/work", map[string]any{"command": "go test ./..."}, "default"),
		"a sibling sharing a prefix": askIn("f", "Bash", spacedDir+" two", map[string]any{"command": "go test ./..."}, "default"),
		"above the project":          askIn("g", "Bash", "/home/alice/my projects", map[string]any{"command": "ls"}, "default"),
		"a path that leaves it":      askIn("h", "Bash", spacedDir, map[string]any{"command": "cat ../other/x"}, "default"),
		"an edit outside":            askIn("i", "Edit", spacedDir, map[string]any{"file_path": "/home/alice/my projects/other/a.go"}, "default"),
	} {
		if !asked(c) {
			t.Errorf("%s did not ask", name)
		}
	}
}

func TestProjectTrustIsKeyedOnTheRealPathWithSpaces(t *testing.T) {
	f, s, w := spacedFixture(t)
	code := agentBind(t, f, s, "Alice", "s-1")
	f.states = []string{"denied"}
	id := grantT3(t, f, s, code, "")
	pts := s.st.projects(s.now())
	if len(pts) != 1 || pts[0].ID != id || pts[0].Root != spacedDir || pts[0].Project != "p1" {
		t.Fatalf("%+v", pts)
	}
	if pv := lastSubmit(t, f).Preview.(map[string]any); pv["project_root"] != spacedDir {
		t.Fatalf("the approval names %v", pv["project_root"])
	}
	n := len(f.submits)
	w.handleAsk(context.Background(), mac, askIn("a", "Bash", spacedDir+"/cmd", map[string]any{"command": "go vet ./..."}, "default"))
	w.handleAsk(context.Background(), mac, askIn("b", "Bash", "/work", map[string]any{"command": "go vet ./..."}, "default"))
	got := map[string]string{}
	for _, d := range f.agent.decided() {
		got[d.AskID] = d.Behavior
	}
	if got["a"] != "allow" || len(f.submits) != n+1 {
		t.Fatalf("decisions %v, asked %d", got, len(f.submits)-n)
	}
	// A project whose path is not one a trust can be for is refused when it is offered.
	f.agent.projects = []agentProject{{ID: "p1", Dir: "/home", VMState: "running"}}
	s.mu.Lock()
	s.roots = nil
	s.mu.Unlock()
	if _, err := s.trustTool(context.Background(), f, trustIn{VerifiedAgent: "Alice", BindCode: code}); err == nil {
		t.Fatal("a trust for /home was offered")
	}
}

func TestAskWithoutAKnownProjectPathAlwaysAsks(t *testing.T) {
	f, _, w := agentFixture(t)
	f.states = []string{"denied"}
	it := bashAsk("a", "ls", "bypassPermissions")
	it.Project = "nope"
	w.handleAsk(context.Background(), mac, it)
	if len(f.submits) != 1 || !strings.Contains(f.submits[0].Preview.(map[string]any)["why_this_approval"].(string), "did not report this project's path") {
		t.Fatalf("%+v", f.submits)
	}
	// A project path that isn't clean is no path either.
	f.agent.projects = []agentProject{{ID: "p9", Dir: "relative/dir"}, {ID: "p8", Dir: "/a/b/../c"}}
	for _, p := range []string{"p9", "p8"} {
		it.Project = p
		w.handleAsk(context.Background(), mac, it)
	}
	if len(f.submits) != 3 {
		t.Fatalf("submits %d", len(f.submits))
	}
}

func TestProjectPathIsReusedForAMinute(t *testing.T) {
	f, s, w := agentFixture(t)
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for i := range 3 {
		w.handleAsk(context.Background(), mac, bashAsk("a"+string(rune('0'+i)), "ls", "bypassPermissions"))
	}
	if n := len(f.agent.called(opAgentProjects)); n != 1 {
		t.Fatalf("%d project lists for 3 questions", n)
	}
	now = now.Add(2 * time.Minute)
	w.handleAsk(context.Background(), mac, bashAsk("b", "ls", "bypassPermissions"))
	if n := len(f.agent.called(opAgentProjects)); n != 2 {
		t.Fatalf("%d project lists after the reuse time", n)
	}
}
