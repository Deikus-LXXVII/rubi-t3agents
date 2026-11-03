package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestUnlimitedTrustViaTool(t *testing.T) {
	f, s, code := ptBot(t)
	ctx := context.Background()
	if _, err := s.trustTool(ctx, f, trustIn{VerifiedAgent: "Alice", BindCode: code, Until: "forever"}); err == nil {
		t.Fatal("a made-up until accepted")
	}
	if len(f.submits) != 0 {
		t.Fatal("asked for a refused call")
	}
	// The default and "0400" stay as before.
	res, err := s.trustTool(ctx, f, trustIn{VerifiedAgent: "Alice", BindCode: code, Until: "0400"})
	if err != nil || res.(map[string]any)["status"] != "awaiting_approval" || f.submits[0].Options[0].Key != optionTrustProject {
		t.Fatalf("%v %v", res, err)
	}
	f.submits = nil
	res, err = s.trustTool(ctx, f, trustIn{VerifiedAgent: "Alice", BindCode: code, Until: " Revoked "})
	if err != nil || res.(map[string]any)["status"] != "awaiting_approval" || len(f.submits) != 1 {
		t.Fatalf("%v %v", res, err)
	}
	sub := f.submits[0]
	if sub.Kind != kindTrustProject || len(sub.Options) != 1 || sub.Options[0].Key != optionTrustForever {
		t.Fatalf("%+v", sub)
	}
	text, _ := json.Marshal(sub.Preview)
	for _, want := range []string{"UNTIL REVOKED — no end time", ptRoot, "Alice", "Still asks"} {
		if !strings.Contains(string(text)+sub.Summary+sub.Options[0].Label+sub.Options[0].Meaning, want) {
			t.Errorf("approval lacks %q: %s", want, text)
		}
	}
	if strings.Contains(string(text)+sub.Summary, "04:00") {
		t.Errorf("an end time in the approval: %s %s", text, sub.Summary)
	}
	if len(s.st.projects(s.now())) != 0 {
		t.Fatal("granted before the approval")
	}
	var tk trustTicket
	raw, _ := json.Marshal(sub.Payload)
	_ = json.Unmarshal(raw, &tk)
	out, err := s.executeTrust(f, tk)
	if err != nil {
		t.Fatal(err)
	}
	pts := s.st.projects(s.now())
	if len(pts) != 1 || !pts[0].Unlimited || pts[0].Sister != "Alice" || out.(map[string]any)["until"] != "revoked" {
		t.Fatalf("%+v %v", pts, out)
	}
	// A late answer is no problem for a trust without an end, and a Bot's own start under it needs no approval.
	late := s.now().Add(90 * 24 * time.Hour)
	s.now = func() time.Time { return late }
	if _, err := s.executeTrust(f, tk); err != nil {
		t.Errorf("a regrant of the same offer: %v", err)
	}
}

func TestUnlimitedTrustIsNeverPruned(t *testing.T) {
	f, s, now := ptFixture(t)
	// 19 unlimited and one timed trust fill the list; a new grant evicts the timed one, not an unlimited one.
	for i := range maxProjectTrusts - 1 {
		o, _ := s.offerFor(fmt.Sprintf("/Users/alice/p%d", i), "github.com/alice/p", "", "Mac")
		if _, err := s.grantProject(f, "dev_mac", "Mac", "", o.forever()); err != nil {
			t.Fatal(err)
		}
	}
	o, _ := s.offerFor("/Users/alice/timed", "github.com/alice/p", "", "Mac")
	if _, err := s.grantProject(f, "dev_mac", "Mac", "", o); err != nil {
		t.Fatal(err)
	}
	o, _ = s.offerFor("/Users/alice/new", "github.com/alice/p", "", "Mac")
	if _, err := s.grantProject(f, "dev_mac", "Mac", "", o.forever()); err != nil {
		t.Fatal(err)
	}
	pts := s.st.projects(*now)
	if len(pts) != maxProjectTrusts {
		t.Fatalf("%d trusts", len(pts))
	}
	for _, p := range pts {
		if p.Root == "/Users/alice/timed" {
			t.Fatal("the timed trust stayed while the list was full")
		}
	}
	i := slices.Index(f.audits, "project_trust_replaced")
	if i < 0 || f.auditLog[i]["root"] != "/Users/alice/timed" {
		t.Fatalf("eviction audit %v", f.auditLog)
	}
	// All 20 are unlimited now: a 21st project is refused and nothing changes.
	before, _ := json.Marshal(s.st.projects(*now))
	o, _ = s.offerFor("/Users/alice/twentyfirst", "github.com/alice/p", "", "Mac")
	if _, err := s.grantProject(f, "dev_mac", "Mac", "", o); err == nil {
		t.Fatal("a 21st trust evicted an unlimited one")
	}
	if after, _ := json.Marshal(s.st.projects(*now)); string(after) != string(before) {
		t.Fatal("a refused grant changed the list")
	}
	// Replacing one of the same root still works at the cap.
	o, _ = s.offerFor("/Users/alice/p3", "github.com/alice/p", "", "Mac")
	if _, err := s.grantProject(f, "dev_mac", "Mac", "", o); err != nil {
		t.Fatalf("replacing at the cap: %v", err)
	}
	// Reading the lists (which prunes) years later drops no unlimited trust.
	later := now.Add(5 * 365 * 24 * time.Hour)
	n := 0
	for _, p := range s.st.projects(later) {
		if p.Unlimited {
			n++
		}
	}
	if n != maxProjectTrusts-1 {
		t.Fatalf("%d unlimited trusts left, want %d", n, maxProjectTrusts-1)
	}
	// (The timed trust that replaced p3 ended in those years, as it should.)
	if n2 := len(s.st.projects(later)); n2 != maxProjectTrusts-1 {
		t.Fatalf("%d", n2)
	}
}
