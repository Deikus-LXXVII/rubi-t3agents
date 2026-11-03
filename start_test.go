package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

func startFixture(t *testing.T) (*fakeHost, *sisters) {
	t.Helper()
	f := &fakeHost{devices: []rubiplugin.HomeDevice{mac}, sessions: []session{mainSess}}
	s := newSisters(&store{})
	if err := s.st.load(f); err != nil {
		t.Fatal(err)
	}
	return f, s
}

func ticketOf(t *testing.T, f *fakeHost) startTicket {
	t.Helper()
	last := f.submits[len(f.submits)-1]
	if last.Kind != kindStart {
		t.Fatalf("kind %q", last.Kind)
	}
	raw, _ := json.Marshal(last.Payload)
	var tk startTicket
	if err := json.Unmarshal(raw, &tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestStartChecksItsInput(t *testing.T) {
	f, s := startFixture(t)
	ctx := context.Background()
	ok := startIn{VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: "x"}
	for name, in := range map[string]startIn{
		"unverified": {Cwd: "/Users/alice/rubi", Task: "x"},
		"no task":    {VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: "  "},
		"long task":  {VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: strings.Repeat("x", maxStartTask+1)},
		"relative":   {VerifiedAgent: "Alice", Cwd: "w", Task: "x"},
		"no cwd":     {VerifiedAgent: "Alice", Task: "x"},
		"control":    {VerifiedAgent: "Alice", Cwd: "/w\n", Task: "x"},
		"no device":  {VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: "x", Device: "Nope"},
		"zero width": {VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: "run\u200B the tests"},
		"bidi":       {VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: "a\u202Eb"},
		"tag chars":  {VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: "a\U000E0041b"},
		"private":    {VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: "a\ue000b"},
		"C1":         {VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: "a\u0085b"},
		"escape":     {VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: "a\x1b[0mb"},
		"blank run":  {VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: "a\n\n\n\nb"},
		"many lines": {VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: strings.Repeat("x\n", maxStartLines+1) + "x"},
	} {
		if _, err := s.startTool(ctx, f, in); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if len(f.submits) != 0 {
		t.Fatalf("asked for approval anyway: %d", len(f.submits))
	}
	long := ok
	long.Task = strings.Repeat("é", maxStartTask)
	if _, err := s.startTool(ctx, f, long); err != nil {
		t.Fatalf("2000 characters: %v", err)
	}
	// Two computers need a choice.
	f.devices = []rubiplugin.HomeDevice{mac, {ID: "dev_pc", Name: "PC"}}
	if _, err := s.startTool(ctx, f, ok); err == nil || !strings.Contains(err.Error(), "which computer") {
		t.Fatalf("%v", err)
	}
	ok.Device = "PC"
	if _, err := s.startTool(ctx, f, ok); err != nil || ticketOf(t, f).Device != "dev_pc" {
		t.Fatalf("by name: %v", err)
	}
}

func TestStartIsRateLimitedPerBot(t *testing.T) {
	f, s := startFixture(t)
	for i := 0; i < startsPerHour; i++ {
		if _, err := s.startTool(context.Background(), f, startIn{VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.startTool(context.Background(), f, startIn{VerifiedAgent: "alice", Cwd: "/Users/alice/rubi", Task: "x"}); err == nil {
		t.Fatal("no limit")
	}
	if _, err := s.startTool(context.Background(), f, startIn{VerifiedAgent: "Vega", Cwd: "/Users/alice/rubi", Task: "x"}); err != nil {
		t.Fatalf("another Bot: %v", err)
	}
	// Across Bots: 5 + 1 so far, 4 more fit, the next is refused whoever asks.
	for i := 0; i < startsGlobal-startsPerHour-1; i++ {
		if _, err := s.startTool(context.Background(), f, startIn{VerifiedAgent: "Bot" + string(rune('A'+i)), Cwd: "/Users/alice/rubi", Task: "x"}); err != nil {
			t.Fatalf("bot %d: %v", i, err)
		}
	}
	if _, err := s.startTool(context.Background(), f, startIn{VerifiedAgent: "Zed", Cwd: "/Users/alice/rubi", Task: "x"}); err == nil || !strings.Contains(err.Error(), "in all") {
		t.Fatalf("no global limit: %v", err)
	}
}

func approvedStart(t *testing.T, f *fakeHost, s *sisters) startTicket {
	t.Helper()
	if _, err := s.startTool(context.Background(), f, startIn{VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: "run the tests"}); err != nil {
		t.Fatal(err)
	}
	return ticketOf(t, f)
}

func TestStartKeepsTheSessionWhenTheCodeCantBeDelivered(t *testing.T) {
	f, s := startFixture(t)
	tk := approvedStart(t, f, s)
	f.emitToErr = errors.New("no Bot named Alice is connected")
	res, err := s.executeStart(context.Background(), f, tk)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["started"] != true || m["bound"] != false || !strings.Contains(m["note"].(string), "started, but not bound") || len(s.st.bindings()) != 0 {
		t.Fatalf("%v %+v", m, s.st.bindings())
	}
}

// A computer whose Rubi Home has no t3-agent capability gets a plain refusal; nothing is asked and nothing is
// sent to a desktop operation.
func TestStartNeedsTheT3Capability(t *testing.T) {
	f, s := startFixture(t)
	f.sessions = nil // no agent: Rubi Home answers "doesn't do" to every t3-agent op
	_, err := s.startTool(context.Background(), f, startIn{VerifiedAgent: "Alice", Cwd: "/Users/alice/rubi", Task: "x"})
	if err == nil || !strings.Contains(err.Error(), "t3-agent") {
		t.Fatalf("%v", err)
	}
	if len(f.submits) != 0 {
		t.Fatal("asked for an approval")
	}
	for _, op := range f.ops {
		if !strings.HasPrefix(op, "t3-agent.") {
			t.Errorf("called %q", op)
		}
	}
	// A payload without a project (a desktop-era approval) is refused when it is executed.
	if _, err := s.executeStart(context.Background(), f, startTicket{Sister: "Alice", Device: "dev_mac", Cwd: "/w", Task: "x"}); err == nil {
		t.Fatal("a desktop start payload was executed")
	}
}

// The approved start goes to t3-agent.start in the project and binds the Bot to the new thread.
func TestStartBindsTheNewThread(t *testing.T) {
	f, s := startFixture(t)
	if _, err := s.startTool(context.Background(), f, startIn{VerifiedAgent: "Alice", Project: "github-com-alice-rubi", Task: "run the tests"}); err != nil {
		t.Fatal(err)
	}
	tk := ticketOf(t, f)
	if tk.Project != "github-com-alice-rubi" || tk.Sister != "Alice" {
		t.Fatalf("ticket %+v", tk)
	}
	if _, err := s.executeStart(context.Background(), f, tk); err != nil {
		t.Fatal(err)
	}
	bs := s.st.bindings()
	if len(bs) != 1 || bs[0].SessionID != "s-new" || bs[0].Project != "github-com-alice-rubi" {
		t.Fatalf("%+v", bs)
	}
	if len(f.derived.called(opAgentStart)) != 1 {
		t.Fatalf("calls %+v", f.derived.calls)
	}
}
