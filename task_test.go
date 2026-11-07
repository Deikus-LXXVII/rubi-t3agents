package main

import (
	"strings"
	"testing"
	"time"
)

var taskNow = time.Date(2026, 11, 7, 20, 0, 0, 0, time.UTC)

var testSite = taskSite{Device: "d1", Project: "rubi", Isolation: isolationVM}

func TestTaskStateMoves(t *testing.T) {
	all := []taskState{taskPreparing, taskWorking, taskNeedsYou, taskIdle, taskFinishing, taskDone, taskFailed, taskStopped}
	allowed := map[[2]taskState]bool{}
	for _, p := range [][2]taskState{
		{taskPreparing, taskWorking}, {taskPreparing, taskFailed}, {taskPreparing, taskStopped},
		{taskWorking, taskNeedsYou}, {taskWorking, taskIdle}, {taskWorking, taskFinishing}, {taskWorking, taskFailed}, {taskWorking, taskStopped},
		{taskNeedsYou, taskWorking}, {taskNeedsYou, taskFailed}, {taskNeedsYou, taskStopped},
		{taskIdle, taskWorking}, {taskIdle, taskFinishing}, {taskIdle, taskFailed}, {taskIdle, taskStopped},
		{taskFinishing, taskDone}, {taskFinishing, taskIdle}, {taskFinishing, taskFailed}, {taskFinishing, taskStopped},
	} {
		allowed[p] = true
	}
	for _, from := range all {
		for _, to := range all {
			if got := from.canMove(to); got != allowed[[2]taskState{from, to}] {
				t.Errorf("%s -> %s: canMove = %v, want %v", from, to, got, !got)
			}
		}
	}
	for _, s := range []taskState{taskDone, taskFailed, taskStopped} {
		if !s.final() {
			t.Errorf("%s should be final", s)
		}
	}
}

func testMandate() taskMandate {
	return taskMandate{ID: "tk_x", Device: "d1", Project: "rubi", Base: "main", Branch: "agent/fix-login", Isolation: isolationVM, Sister: "s1",
		Issued: taskNow, Expires: taskNow.Add(8 * time.Hour)}
}

func TestTaskFinalStateEndsTheMandate(t *testing.T) {
	for _, end := range []taskState{taskDone, taskFailed, taskStopped} {
		tk := newTask(testMandate(), taskNow)
		if err := tk.move(taskWorking, "", taskNow); err != nil {
			t.Fatal(err)
		}
		if end == taskDone {
			if err := tk.move(taskFinishing, "", taskNow); err != nil {
				t.Fatal(err)
			}
		}
		if err := tk.move(end, "why", taskNow); err != nil {
			t.Fatal(err)
		}
		if !tk.Mandate.Ended || tk.Mandate.live(taskNow) {
			t.Errorf("%s: the mandate should have ended", end)
		}
		if ok, _ := tk.Mandate.allowsPush("agent/fix-login", taskNow); ok {
			t.Errorf("%s: a push is allowed after the task ended", end)
		}
		if err := tk.startTurn("bot", testSite, taskNow); err == nil {
			t.Errorf("%s: a turn started after the task ended", end)
		}
	}
}

func TestTaskMandateLife(t *testing.T) {
	m := testMandate()
	if !m.live(taskNow) || m.why(taskNow) != "" {
		t.Fatal("a fresh mandate should hold")
	}
	if m.live(m.Expires) || !strings.Contains(m.why(m.Expires), "time ran out") {
		t.Error("the mandate holds at its end time")
	}
	m.MaxTurns, m.Turns = 2, 2
	if m.live(taskNow) || !strings.Contains(m.why(taskNow), "2 turns") {
		t.Error("the mandate holds past its turn budget")
	}
	m.MaxTurns = 0
	if !m.live(taskNow) {
		t.Error("MaxTurns 0 should mean no budget")
	}
}

func TestTaskTurnsCountAndAreAttributed(t *testing.T) {
	m := testMandate()
	m.MaxTurns = 2
	tk := newTask(m, taskNow)
	if err := tk.startTurn("bot", testSite, taskNow); err != nil {
		t.Fatal(err)
	}
	if err := tk.startTurn("owner-t3", testSite, taskNow); err != nil { // queued behind the running turn
		t.Fatal(err)
	}
	if tk.Mandate.Turns != 2 || tk.LastBy != "owner-t3" || tk.State != taskWorking {
		t.Fatalf("turns %d, last by %q, state %s", tk.Mandate.Turns, tk.LastBy, tk.State)
	}
	if err := tk.move(taskIdle, "", taskNow); err != nil {
		t.Fatal(err)
	}
	if err := tk.startTurn("bot", testSite, taskNow); err == nil || !strings.Contains(err.Error(), "2 turns") {
		t.Errorf("a turn past the budget: %v", err)
	}
	if err := tk.startTurn("someone", testSite, taskNow); err == nil {
		t.Error("an unknown turn origin was accepted")
	}
}

func TestTaskPushesOnlyItsOwnBranch(t *testing.T) {
	m := testMandate()
	for _, c := range []struct {
		ref  string
		ok   bool
		when time.Time
	}{
		{"agent/fix-login", true, taskNow},
		{"refs/heads/agent/fix-login", true, taskNow},
		{"main", false, taskNow},
		{"refs/heads/main", false, taskNow},
		{"agent/other", false, taskNow},
		{"refs/tags/agent/fix-login", false, taskNow},
		{"refs/remotes/origin/agent/fix-login", false, taskNow},
		{"agent/fix-login", true, m.Expires},                     // finishing after the end time
		{"agent/fix-login", false, m.Expires.Add(taskPushGrace)}, // past the grace
	} {
		if ok, why := m.allowsPush(c.ref, c.when); ok != c.ok {
			t.Errorf("allowsPush(%q) = %v (%s), want %v", c.ref, ok, why, c.ok)
		}
	}
	// A mandate that somehow names a protected branch or its base still never pushes it.
	for _, b := range []string{"main", "Master", "release"} {
		bad := m
		bad.Branch = b
		if ok, _ := bad.allowsPush(b, taskNow); ok {
			t.Errorf("a push of %s was allowed", b)
		}
	}
	same := m
	same.Base = strings.ToUpper(same.Branch) // one ref on a case-insensitive disk
	if ok, _ := same.allowsPush(same.Branch, taskNow); ok {
		t.Error("a push of the base was allowed")
	}
	spent := m
	spent.MaxTurns, spent.Turns = 3, 3
	if ok, why := spent.allowsPush(spent.Branch, taskNow); !ok {
		t.Errorf("the last turn's work can't be pushed: %s", why)
	}
}

func TestTaskReasonDoesNotOutliveTheNextMove(t *testing.T) {
	tk := newTask(testMandate(), taskNow)
	for _, s := range []taskState{taskWorking, taskFinishing} {
		if err := tk.move(s, "", taskNow); err != nil {
			t.Fatal(err)
		}
	}
	if err := tk.move(taskIdle, "push refused by the remote", taskNow); err != nil {
		t.Fatal(err)
	}
	if err := tk.move(taskFinishing, "", taskNow); err != nil {
		t.Fatal(err)
	}
	if err := tk.move(taskDone, "", taskNow); err != nil {
		t.Fatal(err)
	}
	if tk.Reason != "" {
		t.Errorf("a done task still says %q", tk.Reason)
	}
}

func TestTaskOfferDefaults(t *testing.T) {
	o, err := newTaskOffer(taskStartIn{Project: " rubi ", Prompt: "Fix the login page: buttons overlap!"}, isolationVM, taskNow)
	if err != nil {
		t.Fatal(err)
	}
	if o.Project != "rubi" || o.Branch != "agent/fix-the-login-page-buttons-overlap" || !o.Expires.Equal(taskNow.Add(8*time.Hour)) || o.MaxTurns != 0 {
		t.Fatalf("offer %+v", o)
	}
	o, err = newTaskOffer(taskStartIn{Project: "rubi", Prompt: "x", Branch: "agent/ui-pass", Base: "release/1.2", Expires: "90m", MaxTurns: 40}, isolationVM, taskNow)
	if err != nil {
		t.Fatal(err)
	}
	if o.Branch != "agent/ui-pass" || o.Base != "release/1.2" || !o.Expires.Equal(taskNow.Add(90*time.Minute)) || o.MaxTurns != 40 {
		t.Fatalf("offer %+v", o)
	}
	m := grantTask(o, "d1", "Mac", "s1", taskNow)
	if !strings.HasPrefix(m.ID, "tk_") || m.Branch != o.Branch || m.Sister != "s1" || !m.Expires.Equal(o.Expires) || !m.live(taskNow) {
		t.Fatalf("mandate %+v", m)
	}
}

func TestTaskOfferRefused(t *testing.T) {
	long := strings.Repeat("a", taskMaxPromptRunes+1)
	for _, c := range []struct {
		name string
		in   taskStartIn
		want string
	}{
		{"no project", taskStartIn{Prompt: "x"}, "which project"},
		{"no prompt", taskStartIn{Project: "p", Prompt: "  "}, "empty"},
		{"long prompt", taskStartIn{Project: "p", Prompt: long}, "longer"},
		{"main", taskStartIn{Project: "p", Prompt: "x", Branch: "main"}, "must match agent/*"},
		{"other prefix", taskStartIn{Project: "p", Prompt: "x", Branch: "feature/x"}, "must match agent/*"},
		{"nested", taskStartIn{Project: "p", Prompt: "x", Branch: "agent/a/b"}, "must match agent/*"},
		{"agent/main", taskStartIn{Project: "p", Prompt: "x", Branch: "agent/main"}, "can't be a task's branch"},
		{"dotdot", taskStartIn{Project: "p", Prompt: "x", Branch: "agent/..x"}, "valid branch"},
		{"lock", taskStartIn{Project: "p", Prompt: "x", Branch: "agent/x.lock"}, "valid branch"},
		{"dot-led", taskStartIn{Project: "p", Prompt: "x", Branch: "agent/.x"}, "valid branch"},
		{"trailing dot", taskStartIn{Project: "p", Prompt: "x", Branch: "agent/x."}, "valid branch"},
		{"glob", taskStartIn{Project: "p", Prompt: "x", Branch: "agent/*"}, "isn't allowed"},
		{"space", taskStartIn{Project: "p", Prompt: "x", Branch: "agent/a b"}, "isn't allowed"},
		{"at-brace", taskStartIn{Project: "p", Prompt: "x", Branch: "agent/a@{1}"}, "isn't allowed"},
		{"base bad", taskStartIn{Project: "p", Prompt: "x", Base: "-main"}, "base:"},
		{"base is branch", taskStartIn{Project: "p", Prompt: "x", Branch: "agent/x", Base: "agent/x"}, "must be new"},
		{"base is branch, case", taskStartIn{Project: "p", Prompt: "x", Branch: "agent/foo", Base: "agent/Foo"}, "must be new"},
		{"LOCK", taskStartIn{Project: "p", Prompt: "x", Branch: "agent/x.LOCK"}, "valid branch"},
		{"expires bad", taskStartIn{Project: "p", Prompt: "x", Expires: "tomorrow"}, "duration"},
		{"expires short", taskStartIn{Project: "p", Prompt: "x", Expires: "1m"}, "between"},
		{"expires long", taskStartIn{Project: "p", Prompt: "x", Expires: "25h"}, "between"},
		{"turns", taskStartIn{Project: "p", Prompt: "x", MaxTurns: taskMaxTurns + 1}, "max_turns"},
		{"turns negative", taskStartIn{Project: "p", Prompt: "x", MaxTurns: -1}, "max_turns"},
	} {
		if _, err := newTaskOffer(c.in, isolationVM, taskNow); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
}

func TestTaskSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Fix the login page":            "fix-the-login-page",
		"  --Hello,   World!!  ":        "hello-world",
		"Привет мир":                    "task",
		"":                              "task",
		"Main":                          "main-task",
		"release":                       "release-task",
		"Make v2 API faster (p99<50ms)": "make-v2-api-faster-p99-50ms",
		strings.Repeat("abc ", 30):      "abc-abc-abc-abc-abc-abc-abc-abc-abc-abc",
	} {
		got := taskSlug(in)
		if got != want {
			t.Errorf("taskSlug(%q) = %q, want %q", in, got, want)
		}
		if len(got) > taskMaxSlug || checkTaskBranch("agent/"+got) != nil {
			t.Errorf("taskSlug(%q) = %q isn't a usable branch suffix", in, got)
		}
	}
}

func TestTaskOfferCarriesTheProjectsIsolation(t *testing.T) {
	in := taskStartIn{Project: "rubi", Prompt: "x"}
	vm, err := newTaskOffer(in, isolationVM, taskNow)
	if err != nil || vm.Isolation != isolationVM || vm.warning() != "" {
		t.Fatalf("vm offer %+v, warning %q, err %v", vm, vm.warning(), err)
	}
	host, err := newTaskOffer(in, isolationHost, taskNow)
	if err != nil || host.Isolation != isolationHost {
		t.Fatalf("host offer %+v, err %v", host, err)
	}
	if w := host.warning(); !strings.HasPrefix(w, "NO CONTAINER") || !strings.Contains(w, "rubi") || !strings.Contains(w, "provider sign-ins") {
		t.Errorf("host warning %q", w)
	}
	if m := grantTask(host, "d1", "box", "s1", taskNow); m.Isolation != isolationHost {
		t.Errorf("the mandate lost the isolation: %+v", m)
	}
	for _, iso := range []taskIsolation{"", "container", "VM"} {
		if _, err := newTaskOffer(in, iso, taskNow); err == nil || !strings.Contains(err.Error(), "isolated") {
			t.Errorf("isolation %q: err = %v", iso, err)
		}
	}
}

func TestTaskTurnOnlyWhereTheMandateHolds(t *testing.T) {
	for _, c := range []struct {
		site taskSite
		want string
	}{
		{taskSite{Device: "d2", Project: "rubi", Isolation: isolationVM}, "another computer"},
		{taskSite{Device: "d1", Project: "other", Isolation: isolationVM}, "project rubi"},
		{taskSite{Device: "d1", Project: "rubi", Isolation: isolationHost}, "without a container"},
	} {
		tk := newTask(testMandate(), taskNow)
		err := tk.startTurn("bot", c.site, taskNow)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("site %+v: err = %v, want %q", c.site, err, c.want)
		}
		if tk.State != taskPreparing || tk.Mandate.Turns != 0 || tk.LastBy != "" {
			t.Errorf("site %+v: a refused turn changed the task: %+v", c.site, tk)
		}
	}
	// A task approved without a container doesn't continue once the project is back in its VM either.
	m := testMandate()
	m.Isolation = isolationHost
	tk := newTask(m, taskNow)
	if err := tk.startTurn("bot", testSite, taskNow); err == nil || !strings.Contains(err.Error(), "in its VM") {
		t.Errorf("host task in a vm project: %v", err)
	}
}

func TestTaskRecordWithoutIsolationFailsClosed(t *testing.T) {
	m := testMandate()
	m.Isolation = ""
	if m.live(taskNow) || !strings.Contains(m.why(taskNow), "isolation") {
		t.Error("a record without isolation holds")
	}
	if ok, _ := m.allowsPush(m.Branch, taskNow); ok {
		t.Error("a record without isolation may push")
	}
}
