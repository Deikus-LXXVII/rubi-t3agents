package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// boundTo is what claude_code_sessions says the session local_a is bound to.
func boundTo(t *testing.T, f *fakeHost, s *sisters) string {
	t.Helper()
	res, err := s.sessionsTool(context.Background(), f, sessionsIn{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res)
	var out struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.Unmarshal(raw, &out)
	for _, it := range out.Sessions {
		if it["app_session_id"] == "local_a" {
			v, _ := it["bound_to"].(string)
			return v
		}
	}
	t.Fatalf("local_a not listed: %s", raw)
	return ""
}

// A binding and its code survive the plugin being restarted (an update restarts it): after a restart from the
// saved state, the session list says the session is bound to the Bot exactly when send and status accept the Bot's
// code. An old code (from before a rebind) is refused as superseded, not as "not bound", and every other refusal
// says which one it is and for which tool.
func TestBindingSurvivesARestartAndTheListMatches(t *testing.T) {
	f, s, code := boundFixture(t)
	ctx := context.Background()

	restart := func() *sisters {
		s2 := newSisters(&store{})
		if err := s2.st.load(f); err != nil {
			t.Fatal(err)
		}
		return s2
	}
	s = restart()
	if got := boundTo(t, f, s); got != "Alice" {
		t.Fatalf("after a restart the list says bound to %q", got)
	}
	if _, err := s.sendTool(ctx, f, sendIn{VerifiedAgent: "Alice", BindCode: code, Text: "hi"}); err != nil {
		t.Fatalf("send after a restart: %v", err)
	}
	if _, err := s.statusTool(ctx, f, statusIn{VerifiedAgent: "Alice", BindCode: code}); err != nil {
		t.Fatalf("status after a restart: %v", err)
	}

	// Rebind: the old code is superseded, the new one works, also after another restart.
	bindAs(t, f, s, "Alice", "local_a")
	newCode := f.events[len(f.events)-1]["bind_code"].(string)
	if newCode == code {
		t.Fatal("the rebind kept the code")
	}
	s = restart()
	_, err := s.sendTool(ctx, f, sendIn{VerifiedAgent: "Alice", BindCode: code, Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "claude_code_send: superseded bind_code") || !strings.Contains(err.Error(), "replaced") {
		t.Errorf("old code: %v", err)
	}
	if _, err := s.sendTool(ctx, f, sendIn{VerifiedAgent: "Alice", BindCode: newCode, Text: "hi"}); err != nil {
		t.Fatalf("new code: %v", err)
	}
	if got := boundTo(t, f, s); got != "Alice" {
		t.Fatalf("list says %q", got)
	}
	// The plugin keeps no old code itself, only its hash.
	if strings.Contains(string(f.state), code) {
		t.Error("the saved state holds the old bind code")
	}

	for name, c := range map[string]struct {
		in   statusIn
		want string
	}{
		"missing code":  {statusIn{VerifiedAgent: "Alice"}, "claude_code_status: missing bind_code"},
		"unknown code":  {statusIn{VerifiedAgent: "Alice", BindCode: "bc_nope"}, "claude_code_status: unknown bind_code"},
		"another Bot's": {statusIn{VerifiedAgent: "Bob", BindCode: newCode}, "claude_code_status: bind_code of another Bot"},
		"no binding":    {statusIn{VerifiedAgent: "Bob", BindCode: "bc_nope"}, "claude_code_status: not bound: Bob has no binding"},
	} {
		if _, err := s.statusTool(ctx, f, c.in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Unbound: the list no longer says bound, and the code says it was unbound.
	if _, err := s.unbindTool(ctx, f, unbindIn{VerifiedAgent: "Alice", BindCode: newCode}); err != nil {
		t.Fatal(err)
	}
	s = restart()
	if got := boundTo(t, f, s); got != "" {
		t.Errorf("unbound, the list says %q", got)
	}
	if _, err := s.sendTool(ctx, f, sendIn{VerifiedAgent: "Alice", BindCode: newCode, Text: "hi"}); err == nil || !strings.Contains(err.Error(), "was unbound") {
		t.Errorf("unbound code: %v", err)
	}
}
