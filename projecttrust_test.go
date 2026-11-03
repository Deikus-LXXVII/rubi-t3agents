package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

const ptRoot = "/Users/alice/rubi"

var (
	ptMain = session{SessionID: "cli-1", AppSessionID: "cli-1", Title: "Rubi main", Cwd: ptRoot, ProjectRoot: ptRoot, RepoKey: "github.com/alice/rubi", Role: "main", Source: "desktop", State: "idle"}
	ptSub  = session{SessionID: "cli-2", AppSessionID: "cli-2", Title: "Rubi sub", Cwd: ptRoot + "/.claude/worktrees/x", ProjectRoot: ptRoot, RepoKey: "github.com/alice/rubi", Role: "sub", Parent: "cli-1", Source: "desktop", State: "busy"}
	ptOut  = session{SessionID: "cli-3", Title: "Other", Cwd: "/Users/alice/other", ProjectRoot: "/Users/alice/other", RepoKey: "github.com/alice/other", Role: "main", Source: "transcript", State: "idle"}
)

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no time zone database:", err)
	}
	return loc
}

// ptFixture is a Rubi with three sessions (two in one project, one elsewhere), the clock at 2026-10-30
// 10:00 in Berlin, so a trust granted now ends Sat 31 Oct 04:00 CET.
func ptFixture(t *testing.T) (*fakeHost, *sisters, *time.Time) {
	t.Helper()
	f := &fakeHost{devices: []rubiplugin.HomeDevice{mac}, sessions: []session{ptMain, ptSub, ptOut}, states: []string{"executed"}}
	s := newSisters(&store{})
	if err := s.st.load(f); err != nil {
		t.Fatal(err)
	}
	loc := berlin(t)
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, loc)
	s.loc, s.now = loc, func() time.Time { return now }
	return f, s, &now
}

func reqIn(session, cwd, command string) request {
	r := newRequest(command)
	r.SessionID, r.Cwd = session, cwd
	return r
}

func TestTrustExpiry(t *testing.T) {
	loc := berlin(t)
	at := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, loc) }
	cases := []struct {
		name      string
		now, want time.Time
	}{
		{"daytime", at(2026, 10, 30, 10, 0), at(2026, 10, 31, 4, 0)},
		{"after midnight", at(2026, 10, 30, 2, 0), at(2026, 10, 30, 4, 0)},
		{"at 04:00 exactly", at(2026, 10, 30, 4, 0), at(2026, 10, 31, 4, 0)},
		{"just before", at(2026, 10, 30, 3, 59), at(2026, 10, 30, 4, 0)},
		{"spring forward (23 h day)", at(2026, 3, 28, 22, 0), at(2026, 3, 29, 4, 0)},
		{"fall back (25 h day)", at(2026, 10, 24, 22, 0), at(2026, 10, 25, 4, 0)},
		{"month end", at(2026, 12, 31, 12, 0), at(2027, 1, 1, 4, 0)},
	}
	for _, c := range cases {
		got := trustExpiry(c.now, loc)
		if !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		if got.Hour() != 4 && got.Sub(c.now) != ptMaxTTL {
			t.Errorf("%s: %v is neither 04:00 nor 24 h", c.name, got)
		}
	}
	// 24 hours is the most: 04:00:01 before a 25 hour day would end at 04:00 the day after (24 h 59 min).
	now := time.Date(2026, 10, 24, 4, 0, 1, 0, loc)
	if got := trustExpiry(now, loc); got.Sub(now) != ptMaxTTL {
		t.Errorf("not capped: %v (%v)", got, got.Sub(now))
	}
	// Every hour of two years: after now, within 24 hours, at 04:00 or capped; in zones with and without DST.
	for _, name := range []string{"Europe/Berlin", "America/New_York", "Asia/Tokyo", "UTC", "Australia/Lord_Howe"} {
		l, err := time.LoadLocation(name)
		if err != nil {
			continue
		}
		for n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); n.Year() < 2028; n = n.Add(37 * time.Minute) {
			got := trustExpiry(n, l)
			if !got.After(n) || got.Sub(n) > ptMaxTTL {
				t.Fatalf("%s %v -> %v", name, n, got)
			}
			if lt := got.In(l); (lt.Hour() != 4 || lt.Minute() != 0) && got.Sub(n) != ptMaxTTL {
				t.Fatalf("%s %v -> %v", name, n, got)
			}
		}
	}
}

func TestReleaseBuildHasNoExpiryHook(t *testing.T) {
	if (projectTrustExpiryHook != nil || clockHook != nil) && !testBuiltWithE2E {
		t.Fatal("a test-only hook (expiry, clock) is present in a build without the rubie2e tag")
	}
}

func TestBranchRule(t *testing.T) {
	const pat = "agent/*"
	allowed := []string{
		"git switch agent/x", "git switch -c agent/x", "git switch --create agent/x", "git switch --create=agent/x", "git switch -c agent/x main",
		"git switch --detach agent/x",
		"git checkout agent/x", "git checkout -b agent/x", "git checkout -bagent/x", "git checkout -B agent/x main", "git checkout --detach agent/x",
		"git branch", "git branch -a", "git branch -vv", "git branch --list 'ma*'", "git branch --show-current", "git branch --merged main",
		"git branch agent/x", "git branch agent/x main", "git branch -m agent/a agent/b", "git branch -c agent/a agent/b", "git branch -d agent/a",
		"git worktree add -b agent/wt wt/x", "git worktree add --detach wt/w main", "git worktree list",
		"git stash branch agent/x", "git stash", "git rebase --no-update-refs main agent/x", "git rebase --no-update-refs --root agent/x", "git rebase --no-update-refs main",
		"git status", "git commit -m x", "git merge main", "git cherry-pick abc1234", "git -C . switch agent/x", "nohup git switch agent/x",
		"go test ./... && git switch -c agent/y && git commit -m z",
	}
	for _, c := range allowed {
		if ok, why := mandateAllowsBranch(c, root, root, pat); !ok {
			t.Errorf("%q asks: %s", c, why)
		}
	}
	asked := []string{
		"git switch main", "git switch -c main2", "git switch --create main", "git switch --create=main", "git switch -", "git switch agent/x/y", "git switch feature/x",
		"git switch --orphan dev", "git switch -t origin/agent/x", "git switch 'agent/*'", "git switch agent/../main",
		"git checkout main", "git checkout -b dev", "git checkout -B dev", "git checkout -bdev", "git checkout --orphan dev", "git checkout HEAD~1", "git checkout -",
		"git branch dev", "git branch dev agent/x", "git branch -m agent/a dev", "git branch -m dev agent/a", "git branch -d main", "git branch -c main agent/b",
		"git worktree add -b dev wt/x", "git worktree add wt/x", "git worktree add wt/x main",
		"git stash branch dev", "git rebase --no-update-refs main dev", "git rebase --no-update-refs --root dev", "git rebase --no-update-refs --onto main a dev",
		// a detached switch names a commit that may be main's tip: it asks
		"git switch --detach main", "git switch -d HEAD~1", "git checkout --detach main", "git switch --detach origin/main",
		"git -C . switch main", "nohup git switch main", "command git checkout main", "CI=1 git checkout dev",
		"git switch agent/x && git switch main", "ls | git switch main", "git status; git checkout -b dev",
		"git branch -r -d origin/main", "git branch -dr origin/main", "git branch --remotes -d origin/main", "git branch -r -m origin/main agent/x", "git branch -a -d main",
		"|", // what follows is refused by the option tables with or without the pattern
		"git rebase main agent/x", "git rebase --update-refs --onto main a", "git rebase --update-refs --no-update-refs main", "git rebase --no-update-refs --update-refs main",
		"git tag v1", "git tag -a v1 -m x", "git push", "git push origin agent/x", "git branch -D agent/x", "git switch -C agent/x", "git reset --hard",
	}
	tables := false
	for _, c := range asked {
		if c == "|" {
			tables = true
			continue
		}
		if ok, _ := mandateAllowsBranch(c, root, root, pat); ok {
			t.Errorf("%q was allowed", c)
		}
		// The pattern is what makes the branch commands ask: without it they are plain mandate commands.
		if ok, why := mandateAllows(c, root, root); ok == tables {
			t.Errorf("%q: without a pattern allowed=%v (%s); the case sits in the wrong group", c, ok, why)
		}
	}
	// A 2-hour trust has no branch rule: only the pattern makes it.
	for _, c := range []string{"git switch main", "git checkout -b dev", "git branch dev"} {
		if ok, why := mandateAllows(c, root, root); !ok {
			t.Errorf("without a pattern %q asks: %s", c, why)
		}
	}
	// Another pattern.
	if ok, _ := mandateAllowsBranch("git switch feature/x", root, root, "feature/*"); !ok {
		t.Error("feature/* refused feature/x")
	}
	if ok, _ := mandateAllowsBranch("git switch agent/x", root, root, "feature/*"); ok {
		t.Error("feature/* allowed agent/x")
	}
}

func TestCheckPattern(t *testing.T) {
	for in, want := range map[string]string{"": "agent/*", "  ": "agent/*", "feature/*": "feature/*", "agent/*-x": "agent/*-x", "r[0-9]": "r[0-9]"} {
		if got, err := checkPattern(in); err != nil || got != want {
			t.Errorf("%q -> %q %v", in, got, err)
		}
	}
	for _, in := range []string{"*", "ma*", "m?in", "[a-z]*", "dev*", "master", "trunk", "[", "a b", "-x", "/x", "a/../b", "x\ny", "a;b", "$x", strings.Repeat("a", 81), "é/*"} {
		if _, err := checkPattern(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

// ---- the Bot ----

// ptBot is the fixture with Alice bound to the project's main session.
func ptBot(t *testing.T) (*fakeHost, *sisters, string) {
	t.Helper()
	f, s, _ := ptFixture(t)
	bindAs(t, f, s, "Alice", "cli-1")
	code := f.events[len(f.events)-1]["bind_code"].(string)
	f.submits = nil
	return f, s, code
}

func TestTrustProjectToolRefusesWithoutAProject(t *testing.T) {
	f, s, code := ptBot(t)
	// The Bot's thread is still there but its project is gone from Rubi Home's list: nothing to trust.
	f.mu.Lock()
	f.derived.projects = nil
	f.agent = &fakeAgent{projects: nil, sessions: map[string][]agentSess{}}
	f.sessions = nil
	f.mu.Unlock()
	if _, err := s.trustTool(context.Background(), f, trustIn{VerifiedAgent: "Alice", BindCode: code}); err == nil {
		t.Fatal("trusted a project that Rubi Home doesn't list")
	}
	if len(f.submits) != 0 {
		t.Fatal("asked")
	}
	// A binding that isn't to a T3 thread (an old store) gets a plain refusal, not a desktop lookup.
	if err := s.st.bind(binding{Sister: "Vega", Device: "dev_mac", SessionID: "cli-9", Code: "bc_x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.trustTool(context.Background(), f, trustIn{VerifiedAgent: "Vega", BindCode: "bc_x"}); err != errNotThread {
		t.Fatalf("%v", err)
	}
}

func TestProjectTrustTextIsHonest(t *testing.T) {
	m := manifest()
	a, ok := m.Action(kindTrustProject)
	if !ok || len(a.Options) != 2 || a.Options[0].Key != optionTrustProject || a.Options[1].Key != optionTrustForever || !a.Locked || a.DefaultLevel != rubiplugin.Strong {
		t.Fatalf("trust action %+v", a)
	}
	for _, want := range []string{"04:00", "24 hours", "still ask", "tags", "start and bind threads"} {
		if !strings.Contains(a.Options[0].Meaning, want) {
			t.Errorf("manifest meaning lacks %q", want)
		}
	}
	for _, want := range []string{"UNTIL REVOKED", "no end time", "t3agents_mandates", "revoke"} {
		if !strings.Contains(a.Options[1].Meaning, want) {
			t.Errorf("until-revoked meaning lacks %q", want)
		}
	}
	for _, o := range a.Options {
		if strings.Contains(o.Meaning, "desktop") || strings.Contains(o.Meaning, "Claude Code") {
			t.Errorf("meaning still talks about the desktop path: %s", o.Meaning)
		}
	}
}

// ---- review fixes ----

func TestRebaseNeedsNoUpdateRefs(t *testing.T) {
	// git rebase --update-refs (or rebase.updateRefs in the config) moves other branches, main included.
	bad := []string{"git rebase --update-refs --onto main a", "git rebase --update-refs --onto main a agent/x", "git rebase main", "git rebase --onto main a",
		"git rebase --no-update-refs --update-refs main", "git rebase --update-refs --no-update-refs main", "git rebase --update-refs", "git rebase"}
	for _, c := range bad {
		if ok, _ := mandateAllows(c, root, root); ok {
			t.Errorf("md: %q allowed", c)
		}
		if ok, _ := mandateAllowsBranch(c, root, root, "agent/*"); ok {
			t.Errorf("pt: %q allowed", c)
		}
	}
	for _, c := range []string{"git rebase --no-update-refs main", "git rebase --no-update-refs --onto main a", "git rebase --continue", "git rebase --abort", "git rebase --skip"} {
		if ok, why := mandateAllows(c, root, root); !ok {
			t.Errorf("md: %q asks: %s", c, why)
		}
	}
	if ok, why := mandateAllowsBranch("git rebase --no-update-refs main agent/x", root, root, "agent/*"); !ok {
		t.Errorf("%s", why)
	}
}

func TestBranchRemoteDeleteIsChecked(t *testing.T) {
	for _, c := range []string{"git branch -r -d origin/main", "git branch -dr origin/main", "git branch --remotes -d origin/main", "git branch -d -r origin/main",
		"git branch -a -d main", "git branch -r -m origin/main x", "git branch --all -c main agent/x"} {
		if ok, _ := mandateAllowsBranch(c, root, root, "agent/*"); ok {
			t.Errorf("%q allowed", c)
		}
	}
	// Listing remote branches is still a listing.
	for _, c := range []string{"git branch -r", "git branch -a", "git branch -r --list 'origin/*'"} {
		if ok, why := mandateAllowsBranch(c, root, root, "agent/*"); !ok {
			t.Errorf("%q asks: %s", c, why)
		}
	}
}

func TestRootBreadth(t *testing.T) {
	cases := []struct {
		root, key string
		ok        bool
	}{
		{"/Users/alice/rubi", "github.com/alice/rubi", true},
		{"/Users/alice/Documents/stuff", "local:/Users/alice/Documents/stuff", false}, // no remote, 4 parts is fine, but...
		{"/Users/alice/Documents", "github.com/alice/docs", false},
		{"/Users/alice/Downloads", "local:/Users/alice/Downloads", false},
		{"/Users/alice/Dev", "local:/Users/alice/Dev", false},
		{"/Users/alice/Dev/proj", "local:/Users/alice/Dev/proj", true},
		{"/Volumes/External", "local:/Volumes/External", false},
		{"/Volumes/External/proj", "local:/Volumes/External/proj", false},
		{"/Volumes/External/Projects/x", "local:/Volumes/External/Projects/x", true},
		{"/Users/alice", "github.com/alice/home", false},
		{"/", "local:/", false},
		{"/Users/alice/rubi", "", false},
	}
	for _, c := range cases {
		why := rootProblem(c.root, c.key)
		want := c.ok
		if c.root == "/Users/alice/Documents/stuff" {
			want = true // deeper than a documents folder: fine
		}
		if (why == "") != want {
			t.Errorf("%s (%s): %q", c.root, c.key, why)
		}
	}
}

func TestExpiryBeyond24HoursIsRefused(t *testing.T) {
	f, s, _ := ptFixture(t)
	o, err := s.offerFor(ptRoot, "github.com/alice/rubi", "", "Mac")
	if err != nil {
		t.Fatal(err)
	}
	o.Expires = s.now().Add(25 * time.Hour)
	if _, err := s.grantProject(f, "dev_mac", "Mac", "", o); err == nil {
		t.Fatal("a 25 hour trust was granted")
	}
	o.Expires = s.now().Add(24 * time.Hour)
	if _, err := s.grantProject(f, "dev_mac", "Mac", "", o); err != nil {
		t.Fatal(err)
	}
}

func TestCapEvictionIsAudited(t *testing.T) {
	f, s, _ := ptFixture(t)
	for i := range maxProjectTrusts + 1 {
		o, _ := s.offerFor(fmt.Sprintf("/Users/alice/p%d", i), "github.com/alice/p", "", "Mac")
		if _, err := s.grantProject(f, "dev_mac", "Mac", "", o); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.st.projects(s.now())) != maxProjectTrusts || !slices.Contains(f.audits, "project_trust_replaced") {
		t.Fatalf("%d trusts, audits %v", len(s.st.projects(s.now())), f.audits)
	}
}

func TestLabelNamesTheRootAndShortensIt(t *testing.T) {
	_, s, _ := ptFixture(t)
	long := "/Users/alice/" + strings.Repeat("deep/", 12) + "project"
	o, err := s.offerFor(long, "github.com/alice/p", "", "Mac")
	if err != nil {
		t.Fatal(err)
	}
	l := s.trustLabel(o)
	if !strings.Contains(l, "…") || !strings.HasSuffix(l, "project until Sat 31 Oct 2026 04:00 CET") || len([]rune(l)) > 100 {
		t.Fatalf("%q", l)
	}
	if m := s.trustMeaning(o, true); !strings.Contains(m, long) || strings.Contains(m, "changes nothing") {
		t.Fatalf("meaning: %s", m)
	}
	// A local zone is shown by its abbreviation and offset, never as "Local".
	s.loc = nil
	o2, _ := s.offerFor(ptRoot, "github.com/alice/rubi", "", "Mac")
	if n := s.zoneNote(o2); strings.Contains(n, "Local") || !strings.Contains(n, "UTC") {
		t.Fatalf("%s", n)
	}
}
