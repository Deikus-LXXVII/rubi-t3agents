package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// t3Status is a t3-setup.status answer for a computer where only the Apple container tool is done.
func t3Status() map[string]any {
	return map[string]any{"steps": []map[string]any{
		step("container", "done", "Installed 1.2", "", nil), step("service", "done", "Running", "", nil),
		step("image", "done", "Built", "", nil),
		step("t3", "todo", "T3 is not installed", "install", nil),
		step("pair", "todo", "Needs T3 first", "", nil),
		step("projects", "done", "1 folder", "add", map[string]any{"projects": []any{row("done", "", "remove")}}),
		step("login", "todo", "Needs T3 first", "", nil),
		step("selftest", "todo", "Needs sign-in first", "", nil)}}
}

func sectionTitles(pg map[string]any) []string {
	var out []string
	for _, sec := range pg["sections"].([]map[string]any) {
		out = append(out, sec["title"].(string))
	}
	return out
}

// The page lists the T3 steps in Rubi Home's order, numbered, with the folders apart.
func TestT3SetupPageListsTheSteps(t *testing.T) {
	f, s := setupFixture(t, t3Status())
	pg := setupView(t, s, f)
	var heads []string // each step's first row: "n. Title: state"
	for _, sec := range pg["sections"].([]map[string]any) {
		if its, _ := sec["items"].([]map[string]any); len(its) > 0 && sec["title"] != "Project folders" && sec["title"] != "Setup" {
			heads = append(heads, its[0]["title"].(string))
		}
	}
	want := []string{"1. Apple container: done", "2. Container service: done", "3. Runner image: done", "4. T3 installed, pinned and running: not done",
		"5. T3 paired with Rubi Home: not done", "6. Claude sign-in: not done", "7. Self-test: not done"}
	if strings.Join(heads, "|") != strings.Join(want, "|") {
		t.Errorf("steps %q, want %q", heads, want)
	}
	if titles := sectionTitles(pg); titles[len(titles)-1] != "Project folders" {
		t.Errorf("sections %v", titles)
	}
	if pg["title"] != "Set up T3 Agents: Mac" {
		t.Errorf("title %v", pg["title"])
	}
	// Only the step Home names a button for has one; the later steps wait.
	a := allActions(pg)
	if a["Install the pinned T3"] == nil {
		t.Fatalf("no T3 install button: %v", a)
	}
	for _, no := range []string{"Install", "Pair", "Sign in", "Run self-test", "Check the sign-in", "Cancel"} {
		if a[no] != nil {
			t.Errorf("%q shown too early", no)
		}
	}
	// The old Agent SDK sign-in text is gone.
	note, _ := pg["note"].(string)
	for _, bad := range []string{"Anthropic's terms", "subscription", "Console", "adds it to their requests"} {
		if strings.Contains(note, bad) {
			t.Errorf("page note still says %q", bad)
		}
	}
}

// Installing T3 needs no approval (it is a pinned download into Rubi Home's own folder, not the macOS installer);
// the container tool still does.
func TestT3SetupInstallAndPairGoStraightToHome(t *testing.T) {
	f, s := setupFixture(t, t3Status())
	ctx := context.Background()
	a := allActions(setupView(t, s, f))["Install the pinned T3"]
	if a["op"] != "setup_run" || a["args"].(map[string]any)["step"] != "t3" {
		t.Fatalf("%v", a)
	}
	args, _ := json.Marshal(a["args"])
	if _, err := s.panel(ctx, f, "setup_run", args); err != nil {
		t.Fatal(err)
	}
	if got := f.setup.called(opSetupRun); len(got) != 1 || got[0]["step"] != "t3" {
		t.Fatalf("calls %v", got)
	}
	if len(f.submits) != 0 {
		t.Error("the T3 install asked for an approval")
	}

	// T3 installed: pairing is the next button; done again offers "Pair again".
	st := t3Status()
	steps := st["steps"].([]map[string]any)
	steps[3] = step("t3", "done", "T3 0.0.9 (pinned)", "", nil)
	steps[4] = step("pair", "todo", "Not paired", "pair", nil)
	f.setup.status = st
	pa := allActions(setupView(t, s, f))
	if pa["Pair"] == nil || pa["Install the pinned T3"] != nil {
		t.Fatalf("%v", pa)
	}
	args, _ = json.Marshal(pa["Pair"]["args"])
	if _, err := s.panel(ctx, f, "setup_run", args); err != nil {
		t.Fatal(err)
	}
	if got := f.setup.called(opSetupRun); len(got) != 2 || got[1]["step"] != "pair" {
		t.Fatalf("calls %v", got)
	}
	steps[4] = step("pair", "done", "Paired", "pair", nil)
	if allActions(setupView(t, s, f))["Pair again"] == nil {
		t.Error("no way to pair again")
	}
	// An upgrade of the pin is offered on a done step.
	steps[3] = step("t3", "done", "T3 0.0.8", "upgrade", nil)
	if allActions(setupView(t, s, f))["Update to the pinned T3"] == nil {
		t.Error("no way to update T3 to the pin")
	}
	// The container install is the one that asks.
	if _, err := s.panel(ctx, f, "setup_install", json.RawMessage(`{"device":"dev_mac"}`)); err != nil || len(f.submits) != 1 || f.submits[0].Kind != kindSetupInstall {
		t.Fatalf("container install: %v %+v", err, f.submits)
	}
}

// Each role has its own sign-in row; the button names the role to Home, and the page only offers roles Home lists.
func TestT3SetupSignInPerRole(t *testing.T) {
	st := allDone()
	steps := st["steps"].([]map[string]any)
	steps[6] = step("login", "todo", "1 of 2 roles signed in", "", map[string]any{"projects": []any{
		map[string]any{"id": "r1", "dir": "reviewer", "state": "todo", "reason": "Not signed in", "action": "login"},
		map[string]any{"id": "r2", "dir": "builder", "state": "done", "action": "login_again"}}})
	f, s := setupFixture(t, st)
	ctx := context.Background()
	pg := setupView(t, s, f)
	var rows []map[string]any
	for _, sec := range pg["sections"].([]map[string]any) {
		if strings.HasSuffix(sec["title"].(string), ". Claude sign-in") {
			rows = sec["items"].([]map[string]any)
		}
	}
	if len(rows) != 3 || rows[1]["title"] != "reviewer" || rows[2]["title"] != "builder" {
		t.Fatalf("rows %v", rows)
	}
	first := rows[1]["actions"].([]map[string]any)[0]
	second := rows[2]["actions"].([]map[string]any)[0]
	if first["label"] != "Sign in" || second["label"] != "Sign in again" {
		t.Fatalf("%v %v", first, second)
	}
	args, _ := json.Marshal(first["args"])
	if _, err := s.panel(ctx, f, "setup_run", args); err != nil {
		t.Fatal(err)
	}
	got := f.setup.called(opSetupRun)
	if len(got) != 1 || got[0]["step"] != "login" || got[0]["role"] != "r1" || got[0]["method"] != "claudeai" {
		t.Fatalf("sent %v", got)
	}
	// A role Home didn't list never reaches Home.
	n := len(f.setup.called(opSetupRun))
	if _, err := s.panel(ctx, f, "setup_run", json.RawMessage(`{"device":"dev_mac","step":"login","project":"r9","method":"claudeai"}`)); err == nil {
		t.Error("a role that is not listed was signed in")
	}
	if len(f.setup.called(opSetupRun)) != n {
		t.Error("reached Home")
	}
	if len(f.auditLog) == 0 || f.audits[len(f.audits)-1] != auditSetup {
		t.Errorf("audits %v", f.audits)
	}
}

// While a role signs in, the page refreshes, shows the link as text only, names the role, and has the code box and
// the open button; the code goes to t3-setup.login.code and is never kept.
func TestT3SetupSignInLinkAndCodeBox(t *testing.T) {
	st := allDone(map[string]any{"job": map[string]any{"step": "login", "project": "r1"},
		"login": map[string]any{"url": "https://claude.com/oauth?state=abc", "wants_code": false, "role": "reviewer"}})
	st["steps"].([]map[string]any)[6] = step("login", "working", "Signing in", "", nil)
	f, s := setupFixture(t, st)
	pg := setupView(t, s, f)
	if pg["refresh"] != 3 {
		t.Errorf("refresh %v", pg["refresh"])
	}
	a := allActions(pg)
	if a["Open the sign-in page"] == nil || a["Paste the code"] == nil || a["Cancel"] == nil {
		t.Fatalf("%v", a)
	}
	var waiting string
	for _, sec := range pg["sections"].([]map[string]any) {
		for _, it := range sec["items"].([]map[string]any) {
			if ti, _ := it["title"].(string); strings.HasPrefix(ti, "Sign-in waiting for the browser") {
				waiting = ti
			}
		}
	}
	if waiting != "Sign-in waiting for the browser (role reviewer)" {
		t.Errorf("title %q", waiting)
	}
	ctx := context.Background()
	if _, err := s.panel(ctx, f, "setup_open", json.RawMessage(`{"device":"dev_mac"}`)); err != nil || len(f.setup.called(opSetupOpen)) != 1 {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.panel(ctx, f, "setup_code", json.RawMessage(`{"device":"dev_mac","code":"abc-123_XYZ"}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.setup.called(opSetupCode); len(got) != 1 || got[0]["code"] != "abc-123_XYZ" {
		t.Fatalf("code calls %v", got)
	}
	for _, e := range f.auditLog {
		if strings.Contains(strings.Join(f.audits, " ")+toString(e), "abc-123_XYZ") {
			t.Fatal("the code is in the audit")
		}
	}
}

func toString(v any) string { b, _ := json.Marshal(v); return string(b) }

// Without the t3-setup capability the page is an error and the first page has no setup entry.
func TestT3SetupNeedsItsOwnCapability(t *testing.T) {
	f, s, _ := agentFixture(t)
	f.setup = nil
	if _, err := s.panel(context.Background(), f, "view", json.RawMessage(`{"page":"setup","device":"dev_mac"}`)); err == nil || !strings.Contains(err.Error(), "t3-setup") {
		t.Fatalf("%v", err)
	}
	for _, op := range f.ops {
		if strings.HasPrefix(op, "agent-setup.") || strings.HasPrefix(op, "claude") {
			t.Errorf("called %q", op)
		}
	}
}
