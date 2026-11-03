package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

func TestCheckAliasName(t *testing.T) {
	ok := []struct{ in, want string }{
		{"api work", "api work"},
		{"  padded  ", "padded"},
		{"\tTabs outside\n", "Tabs outside"},
		{"Привет, мир", "Привет, мир"},
		{"日本語のセッション", "日本語のセッション"},
		{strings.Repeat("x", 64), strings.Repeat("x", 64)},
		{strings.Repeat("я", 64), strings.Repeat("я", 64)},
		{"fix-123", "fix-123"},
		{"localhost", "localhost"},
		{"local-notes", "local-notes"},
		{"deadbee", "deadbee"}, // seven hex characters: not id-like
		{"md_", "md_"},
		{"café", "caf\u00e9"}, // precomposed
		{"build #3 (v2) - 100%", "build #3 (v2) - 100%"},
		{"rocket \U0001F680", "rocket \U0001F680"},
		{"ß and ǅ", "ß and ǅ"},
	}
	for _, c := range ok {
		if got, err := checkAliasName(c.in); err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	bad := map[string]string{
		"empty":              "",
		"spaces only":        "   \t ",
		"65 runes":           strings.Repeat("x", 65),
		"65 cyrillic":        strings.Repeat("я", 65),
		"newline inside":     "a\nb",
		"tab inside":         "a\tb",
		"NUL":                "a\x00b",
		"escape":             "a\x1bb",
		"C1 control":         "a\u0085b",
		"zero width space":   "a\u200bb",
		"zero width joiner":  "a\u200db",
		"bidi override":      "a\u202eb",
		"bidi isolate":       "\u2066a\u2069",
		"bom":                "\ufeffname",
		"soft hyphen":        "a\u00adb",
		"word joiner":        "a\u2060b",
		"line separator":     "a\u2028b",
		"paragraph sep":      "a\u2029b",
		"tag character":      "a\U000e0041b",
		"private use":        "a\ue000b",
		"nbsp inside":        "a\u00a0b",
		"ideographic space":  "a\u3000b",
		"double space":       "a  b",
		"invalid utf8":       "a\xffb",
		"local_ prefix":      "local_notes",
		"LOCAL_ upper":       "LOCAL_abc",
		"local_ uuid":        "local_0c3f1d2e-aaaa-bbbb-cccc-1234567890ab",
		"uuid":               "0c3f1d2e-9a41-4f6e-8d3b-5b6a7c8d9e0f",
		"uuid upper":         "0C3F1D2E-9A41-4F6E-8D3B-5B6A7C8D9E0F",
		"uuid without dash":  "0c3f1d2e9a414f6e8d3b5b6a7c8d9e0f",
		"hex id prefix":      "0c3f1d2e",
		"digits only id":     "12345678",
		"mandate id":         "md_abcdefghijklmnop",
		"project trust id":   "pt_ABCDEFGHIJKLMNOP",
		"bind code":          "bc_0123456789abcdef",
		"ask id":             "ask_0123456789abcdef",
		"request id":         "cc_0123456789abcdef",
		"nbsp-only after tr": "\u200b",
		"combining acute":    "e\u0301",
		"combining only":     "\u0301",
		"variation selector": "a\ufe0fb",
		"vs15":               "a\ufe0e",
		"hangul choseong":    "a\u115fb",
		"hangul jungseong":   "a\u1160b",
		"hangul filler":      "a\u3164b",
		"halfwidth filler":   "a\uffa0b",
		"khmer vowel 1":      "a\u17b4b",
		"khmer vowel 2":      "a\u17b5b",
		"braille blank":      "a\u2800b",
		"mongolian vs":       "a\u180eb",
		"unassigned":         "a\u0378b",
		"emoji zwj sequence": "\U0001F468\u200d\U0001F469",
		"noncharacter":       "a\ufffeb",
		"surrogate":          "a\xed\xa0\x80b",
	}
	for why, in := range bad {
		if got, err := checkAliasName(in); err == nil {
			t.Errorf("%s: %q accepted as %q", why, in, got)
		}
	}
	// The 64 limit counts characters, not bytes.
	if _, err := checkAliasName(strings.Repeat("é", 64)); err != nil {
		t.Errorf("64 two-byte characters refused: %v", err)
	}
}

// renameFixture is boundFixture: Alice is bound to the main session (cli-1).
func renameFixture(t *testing.T) (*fakeHost, *sisters) {
	t.Helper()
	f, s, _ := boundFixture(t)
	f.submits = nil
	return f, s
}

func rename(s *sisters, f *fakeHost, bot string, in renameIn) (any, error) {
	in.VerifiedAgent = bot
	return s.renameTool(context.Background(), f, in)
}

func nameOf(s *sisters, d string, sess session) string {
	a, _ := s.st.aliasFor(d, sess)
	return a.Name
}

func TestRenameOwnSessionNeedsNoApproval(t *testing.T) {
	f, s := renameFixture(t)
	// With no session given, the Bot names the session it is bound to.
	res, err := rename(s, f, "Alice", renameIn{Name: "  Rubi core "})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if len(f.submits) != 0 || m["renamed"] != true || m["name"] != "Rubi core" || !strings.Contains(m["approval"].(string), "none needed") {
		t.Fatalf("submits %d, result %v", len(f.submits), m)
	}
	if nameOf(s, mac.ID, mainSess) != "Rubi core" {
		t.Fatalf("aliases %+v", s.st.aliases())
	}
	// Audited, with who did it.
	last := f.auditLog[len(f.auditLog)-1]
	if f.audits[len(f.audits)-1] != auditRenamed || last["by"] != "bot" || last["sister"] != "Alice" || last["new"] != "Rubi core" || last["approval"] != "own bound session" {
		t.Fatalf("audit %v %v", f.audits, last)
	}
	// The same session by its id or app id, and by its name: also its own, also without approval.
	for _, in := range []renameIn{{SessionID: "cli-1", Name: "Rubi main"}, {SessionID: "cli-1", Name: "Rubi again"}, {SessionName: "rubi again", Name: "Final"}} {
		if _, err := rename(s, f, "Alice", in); err != nil || len(f.submits) != 0 {
			t.Fatalf("%+v: %v (submits %d)", in, err, len(f.submits))
		}
	}
	if nameOf(s, mac.ID, mainSess) != "Final" || len(s.st.aliases()) != 1 {
		t.Fatalf("aliases %+v", s.st.aliases())
	}
	// Clearing its own name.
	if res, err := rename(s, f, "Alice", renameIn{Clear: true}); err != nil || res.(map[string]any)["cleared"] != true || len(s.st.aliases()) != 0 || len(f.submits) != 0 {
		t.Fatalf("%v %v", res, err)
	}
	if res, err := rename(s, f, "Alice", renameIn{Clear: true}); err != nil || res.(map[string]any)["cleared"] != false || len(f.submits) != 0 {
		t.Fatalf("clearing a name that isn't there: %v %v", res, err)
	}
}

func TestRenameChecksTheCaller(t *testing.T) {
	f, s := renameFixture(t)
	ctx := context.Background()
	if _, err := s.renameTool(ctx, f, renameIn{Name: "x"}); err == nil {
		t.Fatal("an unverified caller renamed")
	}
	// A Bot that is bound nowhere has no "own" session and must name one.
	if _, err := rename(s, f, "Vega", renameIn{Name: "x"}); err == nil {
		t.Fatal("an unbound Bot renamed 'its own' session")
	}
	// Another Bot's session is not its own: it asks.
	res, err := rename(s, f, "Vega", renameIn{SessionID: "cli-1", Name: "mine now"})
	if err != nil || res.(map[string]any)["status"] != "awaiting_approval" || len(f.submits) != 1 || nameOf(s, mac.ID, mainSess) != "" {
		t.Fatalf("%v %v (submits %d)", res, err, len(f.submits))
	}
	if _, err := rename(s, f, "Alice", renameIn{Name: "x", Clear: true}); err == nil {
		t.Fatal("name and clear together")
	}
	if _, err := rename(s, f, "Alice", renameIn{}); err == nil {
		t.Fatal("neither name nor clear")
	}
	if _, err := rename(s, f, "Alice", renameIn{SessionID: "nope", Name: "x"}); err == nil {
		t.Fatal("unknown session")
	}
	if _, err := rename(s, f, "Alice", renameIn{SessionID: "cli-1", SessionName: "x", Name: "x"}); err == nil {
		t.Fatal("both session_id and session_name")
	}
}

func TestRenameOtherSessionAsksAndRunsOnApproval(t *testing.T) {
	f, s := renameFixture(t)
	res, err := rename(s, f, "Alice", renameIn{SessionID: "cli-3", Name: "Other project"})
	if err != nil || res.(map[string]any)["status"] != "awaiting_approval" || len(f.submits) != 1 {
		t.Fatalf("%v %v", res, err)
	}
	sub := f.submits[0]
	if sub.Kind != kindRename || nameOf(s, mac.ID, otherSess) != "" {
		t.Fatalf("%+v / named before approval", sub)
	}
	text, _ := json.Marshal(sub.Preview)
	for _, want := range []string{"Other project", "Alice", "Mac", "cli-3", "/Users/alice/other", "changes nothing in T3"} {
		if !strings.Contains(string(text)+sub.Summary, want) {
			t.Errorf("approval lacks %q: %s %s", want, text, sub.Summary)
		}
	}
	// The action is a locked tap: not strong (a label is low-risk), and the user can't lower it.
	for _, a := range manifest().Actions {
		if a.Kind == kindRename && (a.DefaultLevel != rubiplugin.Tap || !a.Locked) {
			t.Fatalf("action %+v", a)
		}
	}
	raw, _ := json.Marshal(sub.Payload)
	var tk renameTicket
	_ = json.Unmarshal(raw, &tk)
	if _, err := s.executeRename(context.Background(), f, tk, "bot", "user approved", nil); err != nil {
		t.Fatal(err)
	}
	if nameOf(s, mac.ID, otherSess) != "Other project" {
		t.Fatalf("aliases %+v", s.st.aliases())
	}
	// Clearing it asks too.
	f.submits = nil
	if res, err := rename(s, f, "Alice", renameIn{SessionName: "other project", Clear: true}); err != nil || res.(map[string]any)["status"] != "awaiting_approval" || len(f.submits) != 1 {
		t.Fatalf("%v %v", res, err)
	}
	// A refused approval changes nothing (nothing runs).
	if nameOf(s, mac.ID, otherSess) != "Other project" {
		t.Fatal("cleared before approval")
	}
}

func TestRenameUniqueness(t *testing.T) {
	f, s := renameFixture(t)
	if _, err := rename(s, f, "Alice", renameIn{Name: "Main"}); err != nil {
		t.Fatal(err)
	}
	// The same name on another session of the computer is refused, ignoring case, at the tool ...
	pend := func(name string) error {
		f.submits = nil
		res, err := rename(s, f, "Alice", renameIn{SessionID: "cli-3", Name: name})
		if err != nil {
			return err
		}
		// ... and again when the approved rename runs (state may have changed since it was asked).
		_ = res
		raw, _ := json.Marshal(f.submits[0].Payload)
		var tk renameTicket
		_ = json.Unmarshal(raw, &tk)
		_, err = s.executeRename(context.Background(), f, tk, "bot", "user approved", nil)
		return err
	}
	for _, n := range []string{"Main", "main", "MAIN", "mAiN "} {
		if err := pend(n); err == nil || !strings.Contains(err.Error(), "already called") {
			t.Errorf("%q: %v", n, err)
		}
	}
	// A session may re-use its own name (change the case).
	if _, err := rename(s, f, "Alice", renameIn{Name: "main"}); err != nil || nameOf(s, mac.ID, mainSess) != "main" {
		t.Fatalf("%v %q", err, nameOf(s, mac.ID, mainSess))
	}
	// A different computer may use the same name.
	other := rubiplugin.HomeDevice{ID: "dev_pc", Name: "PC"}
	if _, err := s.st.setAlias(other.ID, other.Name, session{SessionID: "pc-1"}, "Main", "user", s.now()); err != nil {
		t.Fatalf("same name on another computer: %v", err)
	}
	// Taking another name frees the old one.
	if _, err := rename(s, f, "Alice", renameIn{Name: "Core"}); err != nil {
		t.Fatal(err)
	}
	if err := pend("main"); err != nil {
		t.Fatalf("a freed name: %v", err)
	}
	// A name that is a session id on the computer is refused.
	for _, id := range []string{"cli-3", "CLI-1"} {
		if _, err := rename(s, f, "Alice", renameIn{Name: id}); err == nil {
			t.Errorf("name %q equal to a session id accepted", id)
		}
	}
}

func TestBindByName(t *testing.T) {
	f, s := renameFixture(t)
	ctx := context.Background()
	if _, err := rename(s, f, "Alice", renameIn{Name: "Rubi core"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.setAlias(mac.ID, mac.Name, subSess, "worker", "user", s.now()); err != nil {
		t.Fatal(err)
	}
	// Vega binds by name (case-insensitive); the approval names the session and its real id.
	res, err := s.bindTool(ctx, f, bindIn{VerifiedAgent: "Vega", Name: "WORKER"})
	if err != nil || res.(map[string]any)["status"] != "awaiting_approval" || len(f.submits) != 1 {
		t.Fatalf("%v %v", res, err)
	}
	var tk bindTicket
	raw, _ := json.Marshal(f.submits[0].Payload)
	_ = json.Unmarshal(raw, &tk)
	if tk.SessionID != "cli-2" || tk.Sister != "Vega" {
		t.Fatalf("ticket %+v", tk)
	}
	// The session_id form is as before; both, or neither, is an error.
	f.submits = nil
	if _, err := s.bindTool(ctx, f, bindIn{VerifiedAgent: "Vega", SessionID: "cli-2"}); err != nil || len(f.submits) != 1 {
		t.Fatalf("by id: %v", err)
	}
	if _, err := s.bindTool(ctx, f, bindIn{VerifiedAgent: "Vega", SessionID: "cli-2", Name: "worker"}); err == nil {
		t.Fatal("both session_id and name")
	}
	if _, err := s.bindTool(ctx, f, bindIn{VerifiedAgent: "Vega"}); err == nil {
		t.Fatal("neither session_id nor name")
	}
	// Unknown name: the error lists the names there are.
	_, err = s.bindTool(ctx, f, bindIn{VerifiedAgent: "Vega", Name: "nothing"})
	if err == nil || !strings.Contains(err.Error(), `no session is named "nothing"`) || !strings.Contains(err.Error(), `"worker"`) {
		t.Fatalf("unknown name: %v", err)
	}
	// An id isn't a name.
	if _, err := s.bindTool(ctx, f, bindIn{VerifiedAgent: "Vega", Name: "cli-2"}); err == nil {
		t.Fatal("an id worked as a name")
	}
	// A name whose session has gone says so.
	f.mu.Lock()
	f.sessions = []session{mainSess, otherSess}
	f.mu.Unlock()
	if _, err := s.bindTool(ctx, f, bindIn{VerifiedAgent: "Vega", Name: "worker"}); err == nil || !strings.Contains(err.Error(), "isn't there any more") {
		t.Fatalf("vanished session: %v", err)
	}
}

func TestBindByNameAmbiguousAcrossComputers(t *testing.T) {
	f, s := renameFixture(t)
	pc := rubiplugin.HomeDevice{ID: "dev_pc", Name: "PC"}
	f.devices = []rubiplugin.HomeDevice{mac, pc}
	if _, err := s.st.setAlias(mac.ID, mac.Name, subSess, "worker", "user", s.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.setAlias(pc.ID, pc.Name, session{SessionID: "pc-1"}, "Worker", "user", s.now()); err != nil {
		t.Fatal(err)
	}
	_, err := s.bindTool(context.Background(), f, bindIn{VerifiedAgent: "Vega", Name: "worker"})
	if err == nil || !strings.Contains(err.Error(), "several computers") || !strings.Contains(err.Error(), "PC") {
		t.Fatalf("ambiguous: %v", err)
	}
	// Naming the computer resolves it (by name or id).
	for _, dev := range []string{"Mac", "dev_mac"} {
		if res, err := s.bindTool(context.Background(), f, bindIn{VerifiedAgent: "Vega", Name: "worker", Device: dev}); err != nil || res.(map[string]any)["status"] != "awaiting_approval" {
			t.Fatalf("%s: %v %v", dev, res, err)
		}
	}
	if _, err := s.bindTool(context.Background(), f, bindIn{VerifiedAgent: "Vega", Name: "worker", Device: "Nowhere"}); err == nil {
		t.Fatal("unknown computer")
	}
}

func TestBindResultCarriesTheName(t *testing.T) {
	f, s := renameFixture(t)
	_, _ = rename(s, f, "Alice", renameIn{Name: "Rubi core"})
	res, err := s.executeBind(f, bindTicket{Sister: "Alice", Device: mac.ID, DeviceName: mac.Name, SessionID: "cli-1", AppSessionID: "cli-1", Cwd: mainSess.Cwd})
	if err != nil || res.(map[string]any)["name"] != "Rubi core" {
		t.Fatalf("%v %v", res, err)
	}
}

func TestPanelRenameAndClear(t *testing.T) {
	f, s := renameFixture(t)
	ctx := context.Background()
	call := func(args string) (any, error) { return s.panel(ctx, f, "rename", json.RawMessage(args)) }

	// The user names any session directly: no approval, audited as the user's.
	res, err := call(`{"device":"dev_mac","project":"github-com-alice-rubi","session_id":"cli-2","name":" Sub work "}`)
	if err != nil || res.(map[string]any)["name"] != "Sub work" || len(f.submits) != 0 {
		t.Fatalf("%v %v", res, err)
	}
	last := f.auditLog[len(f.auditLog)-1]
	if f.audits[len(f.audits)-1] != auditRenamed || last["by"] != "user" || last["new"] != "Sub work" {
		t.Fatalf("audit %v %v", f.audits, last)
	}
	// The same rules apply to the user.
	for _, bad := range []string{`"a\u200bb"`, `"local_x"`, `"` + strings.Repeat("x", 65) + `"`, `"sub WORK"`, `"cli-3"`} {
		if _, err := call(`{"device":"dev_mac","project":"github-com-alice-rubi","session_id":"cli-3","name":` + bad + `}`); err == nil {
			t.Errorf("panel accepted %s", bad)
		}
	}
	// A missing name key (a panel without prompt support) must not clear anything.
	if _, err := call(`{"device":"dev_mac","project":"github-com-alice-rubi","session_id":"cli-2"}`); err == nil || nameOf(s, mac.ID, subSess) != "Sub work" {
		t.Fatalf("a call without a name: %v / %q", err, nameOf(s, mac.ID, subSess))
	}
	if _, err := call(`{"device":"dev_mac","project":"github-com-alice-rubi","session_id":"nope","name":"x"}`); err == nil {
		t.Fatal("renamed a session that isn't there")
	}
	// An empty name clears it.
	if res, err := call(`{"device":"dev_mac","project":"github-com-alice-rubi","session_id":"cli-2","name":""}`); err != nil || res.(map[string]any)["cleared"] != true || nameOf(s, mac.ID, subSess) != "" {
		t.Fatalf("%v %v", res, err)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestRenameIsRateLimited(t *testing.T) {
	f, s := renameFixture(t)
	for i := range renamesPerHour {
		if _, err := rename(s, f, "Alice", renameIn{Name: "n" + string(rune('a'+i))}); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if _, err := rename(s, f, "Alice", renameIn{Name: "one more"}); err == nil {
		t.Fatal("no rate limit")
	}
}

func TestTooManyNames(t *testing.T) {
	f, s := renameFixture(t)
	for i := range maxAliases {
		if _, err := s.st.setAlias(mac.ID, mac.Name, session{SessionID: "x-" + string(rune('A'+i%26)) + string(rune('a'+i/26%26)) + string(rune('0'+i/676))}, "name "+string(rune('A'+i%26))+string(rune('a'+i/26%26))+string(rune('0'+i/676)), "user", s.now()); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if _, err := rename(s, f, "Alice", renameIn{Name: "one more"}); err == nil {
		t.Fatal("no cap")
	}
}

func mustTicket(t *testing.T, f *fakeHost, s *sisters, name string) renameTicket {
	t.Helper()
	return renameTicket{Sister: "Alice", Device: mac.ID, DeviceName: mac.Name, SessionID: "cli-1", AppSessionID: "cli-1", Name: name}
}

func TestRenameRateLimitCountsOnlyRealSessions(t *testing.T) {
	f, s := renameFixture(t)
	for range renamesPerHour + 5 {
		if _, err := rename(s, f, "Alice", renameIn{SessionID: "no-such", Name: "x"}); err == nil {
			t.Fatal("unknown session accepted")
		}
	}
	if _, err := rename(s, f, "Alice", renameIn{Name: "still fine"}); err != nil {
		t.Fatalf("failed lookups used up the hour: %v", err)
	}
}

func TestApprovedRenameChecksTheNameAgainstSessionIds(t *testing.T) {
	f, s := renameFixture(t)
	ctx := context.Background()
	// The name passed the tool, then a session with exactly that id appeared before the approval was answered.
	if _, err := rename(s, f, "Alice", renameIn{SessionID: "cli-3", Name: "newcomer"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.submits[0].Payload)
	var tk renameTicket
	_ = json.Unmarshal(raw, &tk)
	f.mu.Lock()
	f.sessions = append(f.sessions, session{SessionID: "NewComer", Cwd: "/w", Role: "main", State: "idle"})
	f.mu.Unlock()
	if _, err := s.executeRename(ctx, f, tk, "bot", "user approved", nil); err == nil || !strings.Contains(err.Error(), "id of a session") {
		t.Fatalf("the approved rename stored an id-like name: %v", err)
	}
	if nameOf(s, mac.ID, otherSess) != "" {
		t.Fatal("stored")
	}
	// Home unreachable at that moment: refused, not stored unchecked.
	f.mu.Lock()
	f.sessions = []session{mainSess, subSess, otherSess}
	f.derived.failOp = map[string]error{opAgentProjects: errors.New("Rubi Home is not answering")}
	f.mu.Unlock()
	if _, err := s.executeRename(ctx, f, tk, "bot", "user approved", nil); err == nil {
		t.Fatal("stored without being able to check")
	}
}
