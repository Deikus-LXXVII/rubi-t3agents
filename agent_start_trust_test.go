package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

func startTrustAsk(t *testing.T) (*fakeHost, *sisters, rubiplugin.Request, startTicket) {
	t.Helper()
	f, s, _ := agentFixture(t)
	if _, err := s.startTool(context.Background(), f, startIn{VerifiedAgent: "Alice", Project: "p1", Task: "go", TrustProject: true}); err != nil {
		t.Fatal(err)
	}
	r := lastSubmit(t, f)
	b, _ := json.Marshal(r.Payload)
	var tk startTicket
	if err := json.Unmarshal(b, &tk); err != nil {
		t.Fatal(err)
	}
	return f, s, r, tk
}

func TestStartAndTrustTheProjectIsOneApprovalWithTwoItems(t *testing.T) {
	f, _, r, tk := startTrustAsk(t)
	if len(f.submits) != 1 || r.Kind != kindStart || len(r.Items) != 2 || r.Items[0].Key != itemStart || r.Items[1].Key != itemTrust || len(r.Options) != 1 {
		t.Fatalf("%+v", r)
	}
	sp, _ := r.Items[0].Preview.(map[string]any)
	if sp["first_message_from_bot"] != "go" || sp["mode"] != "default" || sp["what_it_does"] == nil {
		t.Errorf("the start item lost what the start approval shows: %+v", sp)
	}
	tp, _ := r.Items[1].Preview.(map[string]any)
	if tp["project_root"] != p1Dir || tp["branches"] != "agent/*" || tp["until"] == nil || !strings.Contains(tp["what_it_allows"].(string), "Still asks") {
		t.Errorf("the trust item lost what the trust approval shows: %+v", tp)
	}
	if !strings.Contains(r.Summary, "trust the project") || tk.Trust == nil || tk.Trust.Offer.Unlimited || tk.Trust.Offer.Root != p1Dir {
		t.Fatalf("%q %+v", r.Summary, tk.Trust)
	}
	if len(f.agent.called(opAgentStart)) != 0 {
		t.Fatal("started before the approval")
	}
}

func TestStartTrustOnlyTickedItemsAreDone(t *testing.T) {
	cases := []struct {
		option         string
		started, trust bool
		wantErr        bool
	}{
		{"items:start,trust", true, true, false},
		{"items:start", true, false, false},
		{"items:trust", false, false, true}, // trust only: refused, nothing done
		{"start", false, false, true},       // no items: nothing
		{"", false, false, true},
		{"items:", false, false, true},
		{"items:start,bogus", false, false, true},
	}
	for _, c := range cases {
		f, s, _, tk := startTrustAsk(t)
		_, err := s.executeStartTrust(context.Background(), f, c.option, tk)
		started := len(f.agent.called(opAgentStart)) == 1
		trusted := len(s.st.projects(s.now())) == 1
		if c.option == "items:trust" && (err == nil || !strings.Contains(err.Error(), "together with the session")) {
			t.Errorf("trust only: the error doesn't say why: %v", err)
		}
		if (err != nil) != c.wantErr || started != c.started || trusted != c.trust {
			t.Errorf("%q: err %v started %v trusted %v", c.option, err, started, trusted)
		}
	}
}

// The default tick of the trust item is said plainly: in the summary, at the top of the preview, and in the audit.
func TestStartTrustSaysItPlainlyAndIsAudited(t *testing.T) {
	f, s, r, tk := startTrustAsk(t)
	if !strings.Contains(r.Summary, "and trust the project") || !strings.Contains(r.Summary, "Start Claude in") || !strings.Contains(r.Summary, "04:00") {
		t.Errorf("summary: %q", r.Summary)
	}
	p, _ := r.Preview.(map[string]any)
	w, _ := p["also_trusts_the_project"].(string)
	if !strings.Contains(w, "This also trusts the project: the listed plain commands of every thread in it run without asking until") || !strings.Contains(w, "Untick 'Trust the project' if you only want the session.") {
		t.Errorf("warning: %v", p)
	}
	if !strings.HasPrefix(r.Items[1].Label, "Trust the project") {
		t.Errorf("label: %q", r.Items[1].Label)
	}
	if _, err := s.executeStartTrust(context.Background(), f, "items:start,trust", tk); err != nil {
		t.Fatal(err)
	}
	if a := auditsOf(f, "project_trust_issued"); len(a) != 1 || a[0]["root"] != p1Dir {
		t.Fatalf("audit: %v", a)
	}
}

// A used-up trust limit, or an offer that can't be made, leaves the start alone, with a note, and counts the start once.
func TestStartTrustFallsBackToTheStartAlone(t *testing.T) {
	f, s, _ := agentFixture(t)
	for range trustsPerHour {
		s.allow("trust|alice", trustsPerHour)
	}
	if _, err := s.startTool(context.Background(), f, startIn{VerifiedAgent: "Alice", Project: "p1", Task: "go", TrustProject: true}); err != nil {
		t.Fatal(err)
	}
	r := lastSubmit(t, f)
	p, _ := r.Preview.(map[string]any)
	b, _ := json.Marshal(r.Payload)
	var tk startTicket
	_ = json.Unmarshal(b, &tk)
	if len(r.Items) != 0 || tk.Trust != nil || !strings.Contains(p["trust_note"].(string), "project trust requests an hour") || p["first_message_from_bot"] != "go" {
		t.Fatalf("%+v", r)
	}
	left := 0
	for s.allow("start|alice", startsPerHour) {
		left++
	}
	if left != startsPerHour-1 {
		t.Fatalf("the start counted %d times", startsPerHour-left)
	}
	// An offer that can't be made (a project that is not a clean path) is the same.
	f2, s2, _ := agentFixture(t)
	f2.agent.projects[0].Dir = "/"
	if _, err := s2.startTool(context.Background(), f2, startIn{VerifiedAgent: "Alice", Project: "p1", Task: "go", TrustProject: true}); err != nil {
		t.Fatal(err)
	}
	if r := lastSubmit(t, f2); len(r.Items) != 0 || r.Preview.(map[string]any)["trust_note"] == nil {
		t.Fatalf("%+v", r)
	}
}

// A start that fails grants no trust.
func TestStartTrustNeedsTheStartToSucceed(t *testing.T) {
	f, s, _, tk := startTrustAsk(t)
	f.agent.startFn = func(map[string]any) (map[string]any, error) { return nil, homeRefusal("no room") }
	if _, err := s.executeStartTrust(context.Background(), f, "items:start,trust", tk); err == nil {
		t.Fatal("no error")
	}
	if len(s.st.projects(s.now())) != 0 {
		t.Fatal("trusted after a failed start")
	}
}

func TestStartTrustIsNotAskedUnlessRequested(t *testing.T) {
	f, s, _ := agentFixture(t)
	if _, err := s.startTool(context.Background(), f, startIn{VerifiedAgent: "Alice", Project: "p1", Task: "go"}); err != nil {
		t.Fatal(err)
	}
	r := lastSubmit(t, f)
	b, _ := json.Marshal(r.Payload)
	var tk startTicket
	_ = json.Unmarshal(b, &tk)
	if len(r.Items) != 0 || tk.Trust != nil {
		t.Fatalf("%+v", r)
	}
}

func TestRoleStartCanAskToTrustTheProjectInTheSameApproval(t *testing.T) {
	f, s, _ := roleFixture(t)
	trustBot(t, s, botA, "p1")
	id := mustCreate(t, s, f, botA, "design")
	if _, err := s.roleStartTool(context.Background(), f, roleStartIn{roleWho: who("bot-c"), Role: id, Prompt: "hello", TrustProject: true}); err != nil {
		t.Fatal(err)
	}
	r := lastSubmit(t, f)
	b, _ := json.Marshal(r.Payload)
	var tk startTicket
	_ = json.Unmarshal(b, &tk)
	if len(r.Items) != 2 || tk.Trust == nil || tk.Role != id || tk.Trust.Sister != "bot-c" {
		t.Fatalf("%+v %+v", r, tk)
	}
	if _, err := s.executeStartTrust(context.Background(), f, "items:start", tk); err != nil {
		t.Fatal(err)
	}
	for _, p := range s.st.projects(s.now()) {
		if p.Sister == "bot-c" {
			t.Fatal("the unticked trust was granted")
		}
	}
	if len(f.agent.called(opAgentStart)) != 1 {
		t.Fatal("the unticked trust was granted, or the start did not happen")
	}
}
