package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

// askTogether plays asks that reach the watcher within one window and waits until all are handled.
func askTogether(w *watcher, its ...agentItem) {
	var wg sync.WaitGroup
	for _, it := range its {
		wg.Add(1)
		go func() { defer wg.Done(); w.handleAsk(context.Background(), mac, it) }()
		time.Sleep(15 * time.Millisecond)
	}
	wg.Wait()
}

func batchFixture(t *testing.T) (*fakeHost, *sisters, *watcher) {
	f, s, w := agentFixture(t)
	w.batchWindow = 200 * time.Millisecond
	return f, s, w
}

func decisionOf(d []agentDecision, id string) (agentDecision, bool) {
	for _, x := range d {
		if x.AskID == id {
			return x, true
		}
	}
	return agentDecision{}, false
}

func batchPayload(t *testing.T, r rubiplugin.Request) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(r.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBatchTwoAsksOfOneSessionAreOneApproval(t *testing.T) {
	f, _, w := batchFixture(t)
	askTogether(w, bashAsk("a", "rm -rf build", "default"), bashAsk("b", "rm -rf dist", "default"))
	if len(f.submits) != 1 {
		t.Fatalf("%d approvals, want 1", len(f.submits))
	}
	r := f.submits[0]
	if r.Kind != kindRunBatch || len(r.Items) != 2 {
		t.Fatalf("%+v", r)
	}
	for i, want := range []string{"rm -rf build", "rm -rf dist"} {
		p, _ := r.Items[i].Preview.(map[string]any)
		if p["command"] != want || p["tool"] != "Bash" || p["directory"] != p1Dir || p["session"] != "s-1" || p["why_this_approval"] == nil || !strings.Contains(r.Items[i].Label, want) {
			t.Errorf("item %d: %+v", i, r.Items[i])
		}
	}
	if len(r.Options) != 2 || r.Options[1].Key != optionTrust {
		t.Errorf("options: %+v", r.Options)
	}
	if d := f.agent.decided(); len(d) != 0 {
		t.Fatalf("answered before the user: %+v", d)
	}
}

func TestBatchOnlyTickedAsksAreAllowed(t *testing.T) {
	for _, option := range []string{"items:c2", "allow|items:c2"} {
		f, s, w := batchFixture(t)
		askTogether(w, bashAsk("a", "rm -rf build", "default"), bashAsk("b", "rm -rf dist", "default"))
		payload := batchPayload(t, f.submits[0])
		if _, err := runBatchApproved(context.Background(), f, s, option, payload); err != nil {
			t.Fatal(err)
		}
		d := f.agent.decided()
		a, _ := decisionOf(d, "a")
		b, _ := decisionOf(d, "b")
		if len(d) != 2 || a.Behavior != "deny" || a.Message != msgLeftOut || b.Behavior != "allow" {
			t.Fatalf("%s: %+v", option, d)
		}
		if len(s.st.mandates(s.now())) != 0 {
			t.Fatal("a mandate appeared without the trust option")
		}
	}
}

func TestBatchWithoutItemsAllowsNothing(t *testing.T) {
	for _, option := range []string{"", "allow", optionTrust, "items:", "items:zz", "items:c1,zz"} {
		f, s, w := batchFixture(t)
		askTogether(w, bashAsk("a", "rm -rf build", "default"), bashAsk("b", "rm -rf dist", "default"))
		if _, err := runBatchApproved(context.Background(), f, s, option, batchPayload(t, f.submits[0])); err != nil {
			t.Fatal(err)
		}
		for _, x := range f.agent.decided() {
			if x.Behavior != "deny" {
				t.Errorf("option %q allowed %+v", option, x)
			}
		}
		if len(f.agent.decided()) != 2 || len(s.st.mandates(s.now())) != 0 {
			t.Errorf("option %q: %+v", option, f.agent.decided())
		}
	}
}

func TestBatchEndingsDenyEveryAsk(t *testing.T) {
	for state, msg := range map[string]string{"denied": msgDenied, "cancelled": msgCancelled, "expired": msgExpired} {
		f, _, w := batchFixture(t)
		f.states = []string{"pending", state}
		askTogether(w, bashAsk("a", "rm -rf build", "default"), bashAsk("b", "rm -rf dist", "default"))
		d := f.agent.decided()
		if len(d) != 2 || d[0].Behavior != "deny" || d[1].Behavior != "deny" || !strings.Contains(d[0].Message, msg) {
			t.Errorf("%s: %+v", state, d)
		}
	}
}

func TestBatchAsksOfOtherSessionsAreNotMerged(t *testing.T) {
	f, _, w := batchFixture(t)
	other := bashAsk("b", "rm -rf dist", "default")
	other.SessionID = "s-2"
	askTogether(w, bashAsk("a", "rm -rf build", "default"), other)
	if len(f.submits) != 2 {
		t.Fatalf("%d approvals, want 2", len(f.submits))
	}
	for _, r := range f.submits {
		if r.Kind != kindRun || len(r.Items) != 0 {
			t.Errorf("%+v", r)
		}
	}
}

func TestBatchAlwaysAskOffersAllowOnlyAndNoTrust(t *testing.T) {
	f, s, w := batchFixture(t)
	askTogether(w, bashAsk("a", "rm -rf build", "default"), bashAsk("b", "git push", "default"))
	r := f.submits[0]
	if len(r.Options) != 1 || r.Options[0].Key != optionAllow {
		t.Fatalf("%+v", r.Options)
	}
	p, _ := r.Items[1].Preview.(map[string]any)
	if p["always_asks"] == nil || p["command"] != "git push" {
		t.Fatalf("the reason is lost: %+v", p)
	}
	// A trust option that was not offered gives no mandate, even if it arrives.
	if _, err := runBatchApproved(context.Background(), f, s, optionTrust+"|items:c2", batchPayload(t, r)); err != nil {
		t.Fatal(err)
	}
	if len(s.st.mandates(s.now())) != 0 {
		t.Fatal("a mandate for an ask no trust may cover")
	}
	if a, _ := decisionOf(f.agent.decided(), "b"); a.Behavior != "allow" {
		t.Fatalf("%+v", f.agent.decided())
	}
}

func TestBatchTrustOptionGivesOneMandateForTheTickedAsks(t *testing.T) {
	f, s, w := batchFixture(t)
	askTogether(w, bashAsk("a", "rm -rf build", "default"), bashAsk("b", "rm -rf dist", "default"))
	res, err := runBatchApproved(context.Background(), f, s, optionTrust+"|items:c1", batchPayload(t, f.submits[0]))
	if err != nil {
		t.Fatal(err)
	}
	if m := s.st.mandates(s.now()); len(m) != 1 || m[0].SessionID != "s-1" || m[0].Cwd != p1Dir {
		t.Fatalf("%+v %+v", m, res)
	}
	a, _ := decisionOf(f.agent.decided(), "a")
	b, _ := decisionOf(f.agent.decided(), "b")
	if a.Behavior != "allow" || b.Behavior != "deny" {
		t.Fatalf("%+v %+v", a, b)
	}
	// Nothing ticked: no mandate.
	f2, s2, w2 := batchFixture(t)
	askTogether(w2, bashAsk("a", "rm -rf build", "default"), bashAsk("b", "rm -rf dist", "default"))
	if _, err := runBatchApproved(context.Background(), f2, s2, optionTrust+"|items:", batchPayload(t, f2.submits[0])); err != nil {
		t.Fatal(err)
	}
	if len(s2.st.mandates(s2.now())) != 0 {
		t.Fatal("a mandate without a ticked ask")
	}
}

func TestBatchSubmitFailureDeniesEveryAsk(t *testing.T) {
	f, _, w := batchFixture(t)
	f.submit = func(rubiplugin.Request) (map[string]any, error) { return nil, errTest("hourly limit reached") }
	askTogether(w, bashAsk("a", "rm -rf build", "default"), bashAsk("b", "rm -rf dist", "default"))
	d := f.agent.decided()
	if len(d) != 2 || d[0].Behavior != "deny" || !strings.Contains(d[0].Message, "hourly limit") {
		t.Fatalf("%+v", d)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

// One ask alone is the approval it always was, window or not.
func TestBatchOneAskStaysASingleApproval(t *testing.T) {
	f, _, w := batchFixture(t)
	askTogether(w, bashAsk("a", "rm -rf build", "default"))
	r := f.submits[0]
	if len(f.submits) != 1 || r.Kind != kindRun || len(r.Items) != 0 || len(r.Options) != 2 {
		t.Fatalf("%+v", f.submits)
	}
}

// The batch kind is as strong as a run approval and cannot be lowered.
func TestBatchKindIsStrongAndLocked(t *testing.T) {
	m := manifest()
	b, ok := m.Action(kindRunBatch)
	run, _ := m.Action(kindRun)
	if !ok || b.DefaultLevel != rubiplugin.Strong || !b.Locked || len(b.Options) != len(run.Options) {
		t.Fatalf("%+v", b)
	}
}

// A command the label can't show whole, or with hidden characters, is never an item: it is its own approval.
func TestBatchLongOrHiddenCommandsAreNeverItems(t *testing.T) {
	long := bashAsk("c", "rm -rf "+strings.Repeat("a", 400), "default")
	bidi := bashAsk("c", "rm -rf dist‮", "default")
	for name, odd := range map[string]agentItem{"long": long, "bidi": bidi} {
		f, _, w := batchFixture(t)
		askTogether(w, bashAsk("a", "rm -rf build", "default"), bashAsk("b", "rm -rf tmp", "default"), odd)
		var batches, singles int
		for _, r := range f.submits {
			if len(r.Items) > 0 {
				batches++
				for _, it := range r.Items {
					if strings.Contains(it.Label, "aaaa") || strings.ContainsRune(it.Label, '‮') {
						t.Errorf("%s: the odd command is an item: %q", name, it.Label)
					}
				}
			} else {
				singles++
			}
		}
		if batches != 1 || singles != 1 || len(f.submits) != 2 {
			t.Errorf("%s: %d batches, %d singles", name, batches, singles)
		}
	}
	if !batchable("Bash", strings.Repeat("a", 100)) || batchable("Bash", strings.Repeat("a", 101)) || batchable("Bash", "a\nb") {
		t.Error("batchable's limits")
	}
}

func TestBatchFailedApprovalDeniesEveryAsk(t *testing.T) {
	f, _, w := batchFixture(t)
	f.states = []string{"pending", "failed"}
	askTogether(w, bashAsk("a", "rm -rf build", "default"), bashAsk("b", "rm -rf dist", "default"))
	d := f.agent.decided()
	if len(d) != 2 || d[0].Behavior != "deny" || d[1].Behavior != "deny" || !strings.Contains(d[0].Message, "the approval failed") {
		t.Fatalf("%+v", d)
	}
}

func TestBatchPreviewNamesTheDirectoryAndTheTrustOptionSaysWhat(t *testing.T) {
	f, _, w := batchFixture(t)
	askTogether(w, bashAsk("a", "rm -rf build", "default"), bashAsk("b", "rm -rf dist", "default"))
	r := f.submits[0]
	if p, _ := r.Preview.(map[string]any); p["directory"] != p1Dir {
		t.Fatalf("%+v", r.Preview)
	}
	if r.Options[1].Label != "Allow the ticked commands, and trust this directory's plain listed commands for 2 hours" || r.Options[1].Key != optionTrust {
		t.Fatalf("%+v", r.Options)
	}
}

func TestBatchCapAndTheNextAskStartsANewGroup(t *testing.T) {
	for _, n := range []int{10, 11} {
		f, _, w := batchFixture(t)
		var its []agentItem
		for i := range n {
			its = append(its, bashAsk("a"+strings.Repeat("x", i), "rm -rf d"+strings.Repeat("y", i), "default"))
		}
		askTogether(w, its...)
		var sizes []int
		for _, r := range f.submits {
			sizes = append(sizes, len(r.Items))
		}
		want := []int{10}
		if n == 11 {
			want = []int{10, 0} // the 11th is a new group of one: an ordinary approval
		}
		if len(sizes) != len(want) || sizes[0] != want[0] || (n == 11 && sizes[1] != 0) {
			t.Errorf("%d asks: approvals with %v items", n, sizes)
		}
	}
}

// An ask that arrives after the group was submitted is a new group, not a late item.
func TestBatchAnAskAfterSubmitIsASeparateApproval(t *testing.T) {
	f, _, w := batchFixture(t)
	askTogether(w, bashAsk("a", "rm -rf build", "default"), bashAsk("b", "rm -rf dist", "default"))
	askTogether(w, bashAsk("c", "rm -rf tmp", "default"))
	if len(f.submits) != 2 || len(f.submits[0].Items) != 2 || len(f.submits[1].Items) != 0 {
		t.Fatalf("%+v", f.submits)
	}
}

func TestBatchContextCancelInTheWindowCleansUp(t *testing.T) {
	f, _, w := batchFixture(t)
	w.batchWindow = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.handleAsk(ctx, mac, bashAsk("a", "rm -rf build", "default")); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("still waiting after the cancel")
	}
	w.bmu.Lock()
	left := len(w.groups)
	w.bmu.Unlock()
	if left != 0 || len(f.submits) != 0 {
		t.Fatalf("%d groups left, %d approvals", left, len(f.submits))
	}
}
