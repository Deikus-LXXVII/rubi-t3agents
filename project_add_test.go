package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A Bot adds the folder the user named: one strong approval that shows the folder, and only then the call to Rubi
// Home, with exactly that folder (core matches it against the approval being executed).
func TestProjectAddByABot(t *testing.T) {
	f, s := setupFixture(t, allDone())
	ctx := context.Background()
	for name, in := range map[string]projectAddIn{
		"no verified Bot": {Dir: "/Users/alice/work/app"},
		"relative dir":    {VerifiedAgent: "Alice", Dir: "work/app"},
		"empty dir":       {VerifiedAgent: "Alice"},
		"control char":    {VerifiedAgent: "Alice", Dir: "/Users/alice/work\napp"},
	} {
		n := len(f.submits)
		if _, err := s.projectAddTool(ctx, f, in); err == nil || len(f.submits) != n {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	if _, err := s.projectAddTool(ctx, f, projectAddIn{VerifiedAgent: "Alice", Dir: "/Users/alice/work/app"}); err != nil {
		t.Fatal(err)
	}
	r := lastSubmit(t, f)
	if r.Kind != kindSetupProject {
		t.Fatalf("kind %s", r.Kind)
	}
	pv, _ := json.Marshal(r.Preview)
	if !strings.Contains(string(pv), "/Users/alice/work/app") || !strings.Contains(string(pv), "Alice") || !strings.Contains(r.Summary, "/Users/alice/work/app") {
		t.Errorf("the approval doesn't show the folder and the Bot: %s %s", r.Summary, pv)
	}
	if len(f.setup.called(opSetupAdd)) != 0 {
		t.Fatal("Home called before the approval")
	}
	b, _ := json.Marshal(r.Payload)
	var tk setupTicket
	if err := json.Unmarshal(b, &tk); err != nil || tk.Dir != "/Users/alice/work/app" {
		t.Fatalf("payload %s", b)
	}
	if _, err := s.executeSetupProject(ctx, f, tk); err != nil {
		t.Fatal(err)
	}
	if got := f.setup.called(opSetupAdd); len(got) != 1 || got[0]["dir"] != "/Users/alice/work/app" {
		t.Errorf("Home got %v", got)
	}
}
