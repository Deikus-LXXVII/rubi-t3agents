package main

// Most binding, naming and trust tests describe their world as a list of sessions (fakeHost.sessions). With no
// fakeAgent set, that list is played as T3 threads: one agent project per project (repo key), one thread per
// session, so the tests exercise the T3 path with the same fixtures.

import "strings"

// derivedAgent returns the fake t3-agent operations for f.sessions (nil when there are no sessions or an explicit
// fakeAgent is set). It is refreshed from f.sessions on every call, so a test can change the list.
func (f *fakeHost) derivedAgent() *fakeAgent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agent != nil || len(f.sessions) == 0 {
		return nil
	}
	if f.derived == nil {
		f.derived = &fakeAgent{slot: map[string]any{"busy": false}}
	}
	a := f.derived
	a.mu.Lock()
	defer a.mu.Unlock()
	a.projects = nil
	a.sessions = map[string][]agentSess{}
	seen := map[string]bool{}
	for _, x := range f.sessions {
		id := derivedProject(x)
		if !seen[id] {
			seen[id] = true
			a.projects = append(a.projects, agentProject{ID: id, Dir: firstNonEmpty(x.ProjectRoot, x.Cwd), VMState: "running"})
		}
		mode := x.PermissionMode
		if mode == "" {
			mode = "default"
		}
		a.sessions[id] = append(a.sessions[id], agentSess{ID: x.SessionID, Name: x.Title, Mode: mode, Busy: x.State == "busy", LastActivity: x.LastActivity})
	}
	return a
}

func derivedProject(x session) string {
	id := firstNonEmpty(x.RepoKey, x.ProjectRoot, x.Cwd, x.SessionID)
	r := strings.NewReplacer("/", "-", ".", "-", ":", "-", " ", "-")
	return strings.Trim(r.Replace(id), "-")
}
