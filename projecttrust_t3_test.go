package main

// The project trust and the 2-hour trust, driven through the T3 path: a Bot bound to a thread asks for the trust
// (t3_trust_project), the user approves, and the questions of the project's threads (perm.ask) are allowed or
// asked about by the same checks as ever.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// t3Fixture is the agent fixture with the clock at 2026-10-30 10:00 in Berlin (a trust granted now ends Sat 31 Oct
// 04:00 CET) and Alice bound to the thread s-1 of the project p1.
func t3Fixture(t *testing.T) (*fakeHost, *sisters, *watcher, string, *time.Time) {
	t.Helper()
	f, s, w := agentFixture(t)
	loc := berlin(t)
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, loc)
	s.loc, s.now = loc, func() time.Time { return now }
	code := agentBind(t, f, s, "Alice", "s-1")
	return f, s, w, code, &now
}

// grantT3 plays the user approving t3_trust_project, and returns the trust's id.
func grantT3(t *testing.T, f *fakeHost, s *sisters, code, until string) string {
	t.Helper()
	if _, err := s.trustTool(context.Background(), f, trustIn{VerifiedAgent: "Alice", BindCode: code, Until: until}); err != nil {
		t.Fatal(err)
	}
	r := lastSubmit(t, f)
	if r.Kind != kindTrustProject {
		t.Fatalf("kind %s", r.Kind)
	}
	raw, _ := json.Marshal(r.Payload)
	var tk trustTicket
	_ = json.Unmarshal(raw, &tk)
	res, err := s.executeTrust(f, tk)
	if err != nil {
		t.Fatal(err)
	}
	return res.(map[string]any)["mandate"].(string)
}

var askSeq int

// t3Asks plays one Bash question of a thread and says whether the user was asked (a question that is allowed
// by a trust is answered without an approval).
func t3Asks(f *fakeHost, w *watcher, project, session, cmd string) bool {
	f.mu.Lock()
	f.states = []string{"denied"}
	before := len(f.submits)
	f.mu.Unlock()
	askSeq++
	it := bashAsk("q"+strconv.Itoa(askSeq), cmd, "default")
	it.Project, it.SessionID = project, session
	w.handleAsk(context.Background(), mac, it)
	return len(f.submits) > before
}

func TestT3TrustGrantCoversTheProjectsThreads(t *testing.T) {
	f, s, w, code, now := t3Fixture(t)
	id := grantT3(t, f, s, code, "")
	if !strings.HasPrefix(id, "pt_") {
		t.Fatalf("id %q", id)
	}
	pts := s.st.projects(s.now())
	if len(pts) != 1 || pts[0].Project != "p1" || pts[0].Device != "dev_mac" || pts[0].BranchPattern != "agent/*" ||
		!pts[0].Expires.Equal(time.Date(2026, 10, 31, 4, 0, 0, 0, berlin(t))) || pts[0].MaxUses != ptMaxUses || pts[0].Unlimited {
		t.Fatalf("saved %+v", pts)
	}
	if !slices.Contains(f.audits, "project_trust_issued") {
		t.Fatalf("audits %v", f.audits)
	}
	// Two threads of the project, whichever asks: no question; the audit names the trust.
	for _, c := range []struct{ sess, cmd string }{{"s-1", "go vet ./... && go test -race ./..."}, {"s-2", "git add -A && git commit -m x"}, {"s-9", "make test"}} {
		if t3Asks(f, w, "p1", c.sess, c.cmd) {
			t.Errorf("%s %q asked", c.sess, c.cmd)
		}
		a := f.auditLog[len(f.auditLog)-1]
		if f.audits[len(f.audits)-1] != auditMandateAll || a["mandate"] != id || a["session"] != c.sess || a["command"] != c.cmd {
			t.Errorf("audit %v %v", f.audits[len(f.audits)-1], a)
		}
	}
	if got := s.st.projects(*now)[0].Uses; got != 3 {
		t.Errorf("uses %d", got)
	}
	// Another project is not covered.
	if !t3Asks(f, w, "p2", "s-1", "go test ./...") {
		t.Error("the trust covered another project")
	}
	// Nothing was trusted before the approval.
	f2, s2, _, code2, _ := t3Fixture(t)
	if _, err := s2.trustTool(context.Background(), f2, trustIn{VerifiedAgent: "Alice", BindCode: code2}); err != nil {
		t.Fatal(err)
	}
	if len(s2.st.projects(s2.now())) != 0 {
		t.Fatal("trusted before the user approved")
	}
}

func TestT3TrustStillAsks(t *testing.T) {
	f, s, w, code, _ := t3Fixture(t)
	grantT3(t, f, s, code, "")
	for _, cmd := range []string{
		"git push", "git push origin agent/x", "git status && git push --force", "git tag v1.0.0", "git tag -a v1 -m release", "git push origin v1",
		"gh release create v1", "sudo ls", "curl https://example.com", "npm install left-pad", "pip install x", "rm -rf ../x", "cat x > /tmp/y",
		"git switch main", "git checkout -b dev", "git checkout main && go test ./...", "cat ~/.ssh/id_rsa", "git rebase main",
	} {
		if !t3Asks(f, w, "p1", "s-1", cmd) {
			t.Errorf("%q was allowed by the trust", cmd)
		}
	}
	for _, cmd := range []string{"go test ./...", "git switch agent/x", "git checkout -b agent/y", "git commit -m x", "git rebase --no-update-refs main"} {
		if t3Asks(f, w, "p1", "s-1", cmd) {
			t.Errorf("%q asked under the trust", cmd)
		}
	}
}

func TestT3TrustEnds(t *testing.T) {
	f, s, w, code, now := t3Fixture(t)
	grantT3(t, f, s, code, "")
	if t3Asks(f, w, "p1", "s-2", "go test ./...") {
		t.Fatal("live trust asked")
	}
	later := now.Add(18 * time.Hour) // past 04:00
	s.now = func() time.Time { return later }
	if !t3Asks(f, w, "p1", "s-2", "go test ./...") {
		t.Fatal("an expired trust allowed a command")
	}
	if len(s.st.projects(later)) != 0 {
		t.Fatal("expired trust still listed")
	}
	// A trust used up asks too.
	s.now = func() time.Time { return *now }
	grantT3(t, f, s, code, "")
	s.st.mu.Lock()
	s.st.st.Projects[0].MaxUses = 2
	s.st.mu.Unlock()
	for i := range 2 {
		if t3Asks(f, w, "p1", "s-1", "ls") {
			t.Fatalf("use %d asked", i)
		}
	}
	if !t3Asks(f, w, "p1", "s-1", "ls") {
		t.Fatal("a used-up trust allowed")
	}
}

func TestT3TrustSurvivesARestartAndReplacement(t *testing.T) {
	f, s, w, code, now := t3Fixture(t)
	first := grantT3(t, f, s, code, "")
	t3Asks(f, w, "p1", "s-2", "ls") // a use, saved
	s2 := newSisters(&store{})
	if err := s2.st.load(f); err != nil {
		t.Fatal(err)
	}
	s2.loc, s2.now = s.loc, s.now
	if pts := s2.st.projects(*now); len(pts) != 1 || pts[0].ID != first || pts[0].Uses != 1 {
		t.Fatalf("after restart: %+v", pts)
	}
	if t3Asks(f, newWatcher(f, s2), "p1", "s-1", "go test ./...") {
		t.Error("the restarted plugin asks")
	}
	// An expired one is dropped on load.
	s3 := newSisters(&store{})
	_ = s3.st.load(f)
	if len(s3.st.projects(now.Add(48*time.Hour))) != 0 {
		t.Error("expired trust survived")
	}
	// A new grant for the project replaces the old and says so in the audit.
	second := grantT3(t, f, s, code, "")
	pts := s.st.projects(s.now())
	if len(pts) != 1 || pts[0].ID != second || first == second {
		t.Fatalf("%+v", pts)
	}
	var replaced map[string]any
	for i, a := range f.audits {
		if a == "project_trust_replaced" {
			replaced = f.auditLog[i]
		}
	}
	if replaced == nil || replaced["mandate"] != first || replaced["by"] != second {
		t.Fatalf("replace audit %v", replaced)
	}
}

func TestT3TrustRevokeAndCoexistenceWithTheTwoHourTrust(t *testing.T) {
	f, s, w, code, now := t3Fixture(t)
	md := mandate{ID: "md_1", Device: "dev_mac", SessionID: "s-2", Cwd: p1Dir, Project: "p1", Issued: *now, Expires: now.Add(2 * time.Hour), MaxUses: 300}
	if err := s.st.issue(md, *now); err != nil {
		t.Fatal(err)
	}
	pt := grantT3(t, f, s, code, "")
	// The session's own 2-hour trust is used first; the project trust covers the others.
	t3Asks(f, w, "p1", "s-2", "ls")
	if a := f.auditLog[len(f.auditLog)-1]; a["mandate"] != "md_1" {
		t.Fatalf("audit %v", a)
	}
	if t3Asks(f, w, "p1", "s-1", "ls") {
		t.Fatal("project trust didn't allow")
	}
	// Revoking the project trust leaves the 2-hour one, and the other way round.
	res, err := s.mandatesTool(f, mandatesIn{Revoke: pt})
	if err != nil || res.(map[string]any)["revoked"] != 1 {
		t.Fatalf("%v %v", res, err)
	}
	if !t3Asks(f, w, "p1", "s-1", "ls") {
		t.Error("revoked project trust still allowed")
	}
	if t3Asks(f, w, "p1", "s-2", "ls") {
		t.Error("revoking the project trust ended the 2-hour trust")
	}
	pt = grantT3(t, f, s, code, "")
	if _, err := s.mandatesTool(f, mandatesIn{Revoke: "md_1"}); err != nil {
		t.Fatal(err)
	}
	if t3Asks(f, w, "p1", "s-1", "ls") {
		t.Error("revoking the 2-hour trust ended the project trust")
	}
	// The list shows it, the panel's revoke ends it, and "all" ends everything.
	list, _ := s.mandatesTool(f, mandatesIn{})
	pl := list.(map[string]any)["project_trusts"].([]map[string]any)
	if len(pl) != 1 || pl[0]["id"] != pt || pl[0]["project_root"] != p1Dir || pl[0]["branches"] != "agent/*" {
		t.Fatalf("list %v", list)
	}
	view, err := s.panel(context.Background(), f, "view", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(view)
	for _, want := range []string{"Trusted projects", "agent/*", "\"op\":\"revoke\"", pt} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("panel lacks %q", want)
		}
	}
	if _, err := s.panel(context.Background(), f, "revoke", json.RawMessage(`{"id":"`+pt+`"}`)); err != nil || len(s.st.projects(*now)) != 0 {
		t.Fatalf("panel revoke: %v %v", err, s.st.projects(*now))
	}
	grantT3(t, f, s, code, "")
	if r, _ := s.mandatesTool(f, mandatesIn{Revoke: "all"}); r.(map[string]any)["revoked"] != 1 || len(s.st.projects(*now)) != 0 {
		t.Fatalf("revoke all: %v", r)
	}
}

func TestT3FailedSaveChangesNothing(t *testing.T) {
	f, s, _, code, now := t3Fixture(t)
	id := grantT3(t, f, s, code, "")
	f.saveErr = errors.New("disk full")
	if err := s.st.issue(mandate{ID: "md_2", Device: "dev_mac", SessionID: "s-1", Cwd: p1Dir, Project: "p1", Expires: now.Add(time.Hour), MaxUses: 300}, *now); err == nil {
		t.Fatal("save error ignored")
	}
	if _, err := s.st.issueProject(projectTrust{ID: "pt_new", Device: "dev_mac", Root: p1Dir, Project: "p1", BranchPattern: "agent/*", Expires: now.Add(time.Hour), MaxUses: 5}, *now); err == nil {
		t.Fatal("save error ignored")
	}
	if len(s.st.mandates(*now)) != 0 {
		t.Fatalf("mandates %+v", s.st.mandates(*now))
	}
	if ps := s.st.projects(*now); len(ps) != 1 || ps[0].ID != id {
		t.Fatalf("the old trust was replaced by one that wasn't saved: %+v", ps)
	}
	// Through the approval: the grant fails and says so.
	if _, err := s.trustTool(context.Background(), f, trustIn{VerifiedAgent: "Alice", BindCode: code, Until: "revoked"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(lastSubmit(t, f).Payload)
	var tk trustTicket
	_ = json.Unmarshal(raw, &tk)
	if _, err := s.executeTrust(f, tk); err == nil {
		t.Fatal("a failed save was reported as a grant")
	}
	if ps := s.st.projects(*now); len(ps) != 1 || ps[0].ID != id || ps[0].Unlimited {
		t.Fatalf("%+v", ps)
	}
}

func TestT3UnlimitedTrustLastsAndEndsOnRevoke(t *testing.T) {
	f, s, w, code, now := t3Fixture(t)
	id := grantT3(t, f, s, code, "revoked")
	r := lastSubmit(t, f)
	if len(r.Options) != 1 || r.Options[0].Key != optionTrustForever || !strings.Contains(r.Options[0].Meaning, "UNTIL REVOKED") || !strings.Contains(r.Summary, "UNTIL REVOKED") {
		t.Fatalf("approval %+v", r)
	}
	for _, d := range []time.Duration{time.Hour, 25 * time.Hour, 8 * 24 * time.Hour, 400 * 24 * time.Hour} {
		at := now.Add(d)
		s.now = func() time.Time { return at }
		if t3Asks(f, w, "p1", "s-2", "go test ./...") {
			t.Fatalf("asked after %v", d)
		}
		s2 := newSisters(&store{})
		if err := s2.st.load(f); err != nil {
			t.Fatal(err)
		}
		s2.loc, s2.now = s.loc, s.now
		if pts := s2.st.projects(at); len(pts) != 1 || pts[0].ID != id || !pts[0].Unlimited {
			t.Fatalf("after %v and a restart: %+v", d, pts)
		}
		if t3Asks(f, newWatcher(f, s2), "p1", "s-1", "ls") {
			t.Fatalf("the restarted plugin asks after %v", d)
		}
	}
	// No use cap, and the dangerous commands still ask.
	s.st.mu.Lock()
	s.st.st.Projects[0].Uses = ptMaxUses * 5
	s.st.mu.Unlock()
	if t3Asks(f, w, "p1", "s-1", "ls") {
		t.Fatal("an unlimited trust ended by count")
	}
	for _, cmd := range []string{"git push", "sudo ls", "curl https://example.com", "git tag v1"} {
		if !t3Asks(f, w, "p1", "s-1", cmd) {
			t.Errorf("%q allowed", cmd)
		}
	}
	for _, how := range []string{"tool", "panel", "all"} {
		f, s, w, code, _ := t3Fixture(t)
		id := grantT3(t, f, s, code, "revoked")
		var err error
		switch how {
		case "tool":
			_, err = s.mandatesTool(f, mandatesIn{Revoke: id})
		case "panel":
			_, err = s.panel(context.Background(), f, "revoke", json.RawMessage(`{"id":"`+id+`"}`))
		case "all":
			_, err = s.mandatesTool(f, mandatesIn{Revoke: "all"})
		}
		if err != nil {
			t.Fatal(err)
		}
		if !t3Asks(f, w, "p1", "s-2", "go test ./...") || len(s.st.projects(s.now())) != 0 {
			t.Fatalf("%s: the revoked trust still allows", how)
		}
		s2 := newSisters(&store{})
		_ = s2.st.load(f)
		if len(s2.st.projects(s2.now())) != 0 {
			t.Fatalf("%s: the revoked trust came back", how)
		}
	}
}

// The trust tool asks the strong approval, names the project, the branches and the end; an until it doesn't know
// is refused before anything is asked.
func TestT3TrustToolApproval(t *testing.T) {
	f, s, _, code, _ := t3Fixture(t)
	if _, err := s.trustTool(context.Background(), f, trustIn{VerifiedAgent: "Alice", BindCode: code, Until: "forever"}); err == nil {
		t.Fatal("an unknown until was accepted")
	}
	n := len(f.submits)
	if _, err := s.trustTool(context.Background(), f, trustIn{VerifiedAgent: "Alice", BindCode: code}); err != nil || len(f.submits) != n+1 {
		t.Fatalf("%v", err)
	}
	r := lastSubmit(t, f)
	pv := r.Preview.(map[string]any)
	if r.Kind != kindTrustProject || pv["project_id"] != "p1" || pv["branches"] != "agent/*" || !strings.Contains(fmt.Sprint(pv["until"]), "04:00") || len(r.Options) != 1 || r.Options[0].Key != optionTrustProject {
		t.Fatalf("%+v", r)
	}
	for _, want := range []string{"T3 thread", "Still asks", "git push", "t3_mandates"} {
		if !strings.Contains(r.Options[0].Meaning, want) {
			t.Errorf("meaning lacks %q:\n%s", want, r.Options[0].Meaning)
		}
	}
	for _, bad := range []string{"desktop", "Claude Code session"} {
		if strings.Contains(r.Options[0].Meaning, bad) {
			t.Errorf("meaning still says %q", bad)
		}
	}
}
