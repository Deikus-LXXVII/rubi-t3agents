package main

// Session names (t3_rename). A name is an alias Rubi keeps for a T3 thread on a computer (the runner's own title is
// set beside it): it shows in t3_sessions next to the title, and t3_bind accepts it instead of a thread id.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

const (
	kindRename       = "t3agents.rename"
	maxNameRunes     = 64
	maxAliases       = 500
	renamesPerHour   = 20
	panelListTimeout = 10 * time.Second
	auditRenamed     = "session_renamed"
)

// alias is a user- or Bot-chosen name for one thread on one computer, known by its thread id.
type alias struct {
	Device       string `json:"device"`
	DeviceName   string `json:"device_name,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	AppSessionID string `json:"app_session_id,omitempty"`
	Name         string `json:"name"`
	SetBy        string `json:"set_by,omitempty"` // "user" (the panel) or the Bot's name; empty for older records
	// What the session looked like when last listed, so a name can still be resolved when Rubi Home's
	// list is cut short and doesn't hold the session.
	Cwd     string    `json:"cwd,omitempty"`
	RepoKey string    `json:"repo_key,omitempty"`
	Title   string    `json:"title,omitempty"`
	Set     time.Time `json:"set"`
	Seen    time.Time `json:"seen"`
	// Project is the agent project of the session (t3-agent); such a name is not swept by the hook
	// session lists.
	Project string `json:"project,omitempty"`
}

func (a alias) is(device string, s session) bool {
	return a.Device == device && (a.SessionID != "" && a.SessionID == s.SessionID ||
		a.AppSessionID != "" && a.AppSessionID == s.AppSessionID)
}

var (
	uuidLike  = regexp.MustCompile(`^[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}$`)
	hexOnly   = regexp.MustCompile(`^[0-9a-f]{8,}$`)
	rubiIDLik = regexp.MustCompile(`^(md|pt|bc|ask|cc)_[0-9a-z]{6,}$`)
)

// foldName is the form names are compared in (uniqueness, lookup, the panel): case-insensitive through a round
// trip that also folds characters like the final sigma or the Turkish dotless i.
func foldName(s string) string { return strings.ToLower(strings.ToUpper(s)) }

func sameName(a, b string) bool { return foldName(a) == foldName(b) }

// nameRuneOK allows letters, numbers, punctuation, symbols and the ASCII space. Marks (combining characters,
// variation selectors), format and control characters, separators, private-use and unassigned code points
// are out, and so are the look-alike blanks that are letters or symbols by category. There is no Unicode
// normalization in the standard library, so decomposed text (a letter plus a combining mark) is refused rather
// than normalized: type precomposed characters.
func nameRuneOK(r rune) bool {
	switch r {
	case 0x115F, 0x1160, 0x3164, 0xFFA0, // Hangul fillers (letters that draw nothing)
		0x2800,         // braille blank
		0x17B4, 0x17B5, // Khmer inherent vowels (invisible)
		0x180E:
		return false
	}
	return r == ' ' || unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
}

// checkAliasName returns the trimmed name, or says why it is refused. It rejects instead of repairing: the
// user approves what the name says, so what is stored must be what was shown.
func checkAliasName(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", errors.New("the name is not valid text")
	}
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	switch {
	case n == 0:
		return "", errors.New("the name is empty (to remove a name, clear it)")
	case n > maxNameRunes:
		return "", fmt.Errorf("the name has %d characters, at most %d", n, maxNameRunes)
	}
	prev := rune(0)
	for _, r := range name {
		switch {
		case r == ' ' && prev == ' ':
			return "", errors.New("the name has two spaces in a row")
		case !nameRuneOK(r):
			return "", fmt.Errorf("the name contains a character that isn't allowed (U+%04X): use letters, digits, punctuation, symbols and ordinary spaces, with precomposed accents", r)
		}
		prev = r
	}
	low := strings.ToLower(name)
	switch {
	case strings.HasPrefix(low, "local_"):
		return "", errors.New(`a name can't start with "local_" (that is how desktop session ids look)`)
	case uuidLike.MatchString(low), hexOnly.MatchString(low), rubiIDLik.MatchString(low):
		return "", errors.New("the name looks like an id; choose a descriptive name")
	}
	return name, nil
}

// ---- store ----

// aliasFor is the alias of a session on a computer.
func (s *store) aliasFor(device string, sess session) (alias, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.st.Aliases {
		if a.is(device, sess) {
			return a, true
		}
	}
	return alias{}, false
}

func (s *store) aliases() []alias {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]alias(nil), s.st.Aliases...)
}

// aliasesNamed finds the aliases called name (case-insensitive), on one computer or, with device "", on any.
func (s *store) aliasesNamed(device, name string) []alias {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []alias
	for _, a := range s.st.Aliases {
		if (device == "" || a.Device == device) && sameName(a.Name, name) {
			out = append(out, a)
		}
	}
	return out
}

// setAlias names a session (replacing its old name) and returns the old alias (zero when it had none). by is
// who sets it: "user" or a Bot's name. The name must be free on the computer; if saving fails nothing changed.
func (s *store) setAlias(device, deviceName string, sess session, name, by string, now time.Time) (alias, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := append([]alias(nil), s.st.Aliases...)
	var old alias
	found := -1
	for i, a := range s.st.Aliases {
		if a.is(device, sess) {
			old, found = a, i
			continue
		}
		if a.Device == device && sameName(a.Name, name) {
			return alias{}, fmt.Errorf("another session on %s is already called %q; names are unique per computer, ignoring case", firstNonEmpty(deviceName, device), oneLine(a.Name, maxNameRunes))
		}
	}
	na := alias{Device: device, DeviceName: deviceName, SessionID: sess.SessionID, AppSessionID: sess.AppSessionID, Name: name, SetBy: by,
		Cwd: sess.Cwd, RepoKey: sess.RepoKey, Title: oneLine(sess.Title, 200), Set: now, Seen: now}
	if sess.Source == "agent" { // an agent session (agent.go asSession): its project is in the RepoKey
		na.Project = strings.TrimPrefix(sess.RepoKey, "agent:")
	}
	if found >= 0 {
		s.st.Aliases[found] = na
	} else {
		if len(s.st.Aliases) >= maxAliases {
			return alias{}, fmt.Errorf("there are already %d session names; clear some in the Rubi panel first", maxAliases)
		}
		s.st.Aliases = append(s.st.Aliases, na)
	}
	if err := s.saveLocked(); err != nil {
		s.st.Aliases = before
		return alias{}, err
	}
	return old, nil
}

// clearAlias removes a session's name and returns it ("" when it had none).
func (s *store) clearAlias(device string, sess session) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := append([]alias(nil), s.st.Aliases...)
	old := ""
	kept := make([]alias, 0, len(s.st.Aliases))
	for _, a := range s.st.Aliases {
		if a.is(device, sess) {
			old = a.Name
			continue
		}
		kept = append(kept, a)
	}
	if old == "" {
		return "", nil
	}
	s.st.Aliases = kept
	if err := s.saveLocked(); err != nil {
		s.st.Aliases = before
		return "", err
	}
	return old, nil
}

// clearAliasExact removes an alias record itself (a name whose session is gone).
func (s *store) clearAliasExact(device, name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := append([]alias(nil), s.st.Aliases...)
	kept := make([]alias, 0, len(s.st.Aliases))
	found := false
	for _, a := range s.st.Aliases {
		if a.Device == device && sameName(a.Name, name) {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		return false, nil
	}
	s.st.Aliases = kept
	if err := s.saveLocked(); err != nil {
		s.st.Aliases = before
		return false, err
	}
	return true, nil
}

// ---- resolving a name ----

// ---- the tool ----

type renameIn struct {
	Agent         string `json:"rubi_agent" jsonschema:"your Bot name as connected to Rubi"`
	AgentCode     string `json:"rubi_agent_code,omitempty" jsonschema:"the agent_code of your latest Rubi webhook (or call rubi_verify with your name to get one); proves you are that Bot"`
	VerifiedAgent string `json:"rubi_verified_agent,omitempty" jsonschema:"set by Rubi; don't pass it"`
	SessionID     string `json:"session_id,omitempty" jsonschema:"the session to name: the thread's session_id from t3_sessions. Leave out (with session_name) for the session you are bound to"`
	SessionName   string `json:"session_name,omitempty" jsonschema:"the session's current name, instead of session_id"`
	Device        string `json:"device,omitempty" jsonschema:"the computer, when there are several"`
	Project       string `json:"project,omitempty" jsonschema:"for a Claude session in a project VM: its project (id or directory)"`
	Name          string `json:"name,omitempty" jsonschema:"the new name: 1 to 64 plain characters, unique on that computer (ignoring case), not like an id"`
	Clear         bool   `json:"clear,omitempty" jsonschema:"remove the session's name instead of setting one"`
}

type renameTicket struct {
	Sister       string `json:"sister"`
	Device       string `json:"device"`
	DeviceName   string `json:"device_name"`
	SessionID    string `json:"session_id"`
	AppSessionID string `json:"app_session_id,omitempty"`
	Name         string `json:"name,omitempty"`
	Clear        bool   `json:"clear,omitempty"`
	Project      string `json:"project,omitempty"` // an agent project (t3-agent)
	Role         string `json:"role,omitempty"`    // the isolated role of that session
}

// freeName checks a name against the session ids the list showed (a name that equals an id is refused).
func freeName(name string, all []deviceSessions, device string) error {
	for _, ds := range all {
		if ds.device.ID != device {
			continue
		}
		for _, x := range ds.list {
			if sameName(name, x.SessionID) || x.AppSessionID != "" && sameName(name, x.AppSessionID) {
				return errors.New("the name is the id of a session; choose a descriptive name")
			}
		}
	}
	return nil
}

func (s *sisters) renameTool(ctx context.Context, h sisterHost, in renameIn) (any, error) {
	sister := strings.TrimSpace(in.VerifiedAgent)
	if sister == "" || len(sister) > 64 {
		return nil, errors.New("Rubi didn't confirm which Bot you are: pass rubi_agent (your Bot name) and rubi_agent_code (the agent_code of your latest Rubi webhook, or call rubi_verify to get one)")
	}
	name := ""
	if in.Clear {
		if strings.TrimSpace(in.Name) != "" {
			return nil, errors.New("pass name or clear, not both")
		}
	} else {
		var err error
		if name, err = checkAliasName(in.Name); err != nil {
			return nil, err
		}
	}
	var (
		d    rubiplugin.HomeDevice
		sess session
		all  []deviceSessions
		// project is set when the session is an agent session (t3-agent).
		project string
		role    string // and its isolated role, if any
	)
	agentTarget := func(ref agentRef) {
		d, sess, all, project = ref.ad.device, ref.sess.asSession(ref.proj.Dir), asDeviceSessions([]agentDevice{ref.ad}), ref.proj.ID
		role = ref.sess.Role
	}
	if in.SessionID == "" && in.SessionName == "" {
		// The Bot's own session.
		var mine *binding
		for _, b := range s.st.bindings() {
			if sameBot(b.Sister, sister) && (in.Device == "" || b.Device == in.Device || b.DeviceName == in.Device) {
				b := b
				mine = &b
			}
		}
		if mine == nil {
			return nil, errors.New("you are not bound to a session: pass session_id (or session_name) of the session to name")
		}
		if mine.Project != "" {
			ref, found, err := s.resolveAgent(ctx, h, mine.Device, mine.Project, mine.SessionID, "")
			if err != nil || !found {
				return nil, errors.New("the session you are bound to isn't there any more")
			}
			if ref.sess.Role != mine.Role {
				return nil, errors.New("the session you are bound to isn't there any more")
			}
			agentTarget(ref)
		} else {
			return nil, errNotThread
		}
	} else {
		id, nm := strings.TrimSpace(in.SessionID), strings.TrimSpace(in.SessionName)
		if id != "" && nm != "" {
			return nil, errors.New("pass session_id or session_name, not both")
		}
		if err := s.nameOnSeveralComputers(in.Device, nm); err != nil {
			return nil, err
		}
		if ref, found, err := s.resolveAgent(ctx, h, in.Device, in.Project, id, nm); err != nil {
			return nil, err
		} else if found {
			agentTarget(ref)
		} else {
			return nil, s.noThread(h, in.Device, id, nm)
		}
	}
	// Counted only now, for a real session: a typo in an id doesn't use up the hour.
	if !s.allow("rename|"+strings.ToLower(sister), renamesPerHour) {
		return nil, fmt.Errorf("at most %d renames per hour", renamesPerHour)
	}
	if !in.Clear {
		if err := freeName(name, all, d.ID); err != nil {
			return nil, err
		}
	}
	t := renameTicket{Sister: sister, Device: d.ID, DeviceName: d.Name, SessionID: sess.SessionID, AppSessionID: sess.AppSessionID, Name: name, Clear: in.Clear, Project: project, Role: role}
	old, _ := s.st.aliasFor(d.ID, sess)
	if in.Clear && old.Name == "" {
		return map[string]any{"cleared": false, "session_id": sess.SessionID, "note": "that session has no name"}, nil
	}
	// A name the user (or another Bot, with the user's tap) gave is not the Bot's to replace or remove on its
	// own, even on its own session or under a project trust: that is the user's call.
	mayAlone := old.Name == "" || sameBot(old.SetBy, sister)

	// 1. The session the Bot is bound to: no approval.
	for _, b := range s.st.bindings() {
		if mayAlone && sameBot(b.Sister, sister) && b.Device == d.ID && b.Project == project && (b.SessionID == sess.SessionID || b.AppSessionID != "" && b.AppSessionID == sess.AppSessionID) {
			res, err := s.executeRename(ctx, h, t, "bot", "own bound session", all)
			if m, ok := res.(map[string]any); ok && err == nil {
				m["approval"] = "none needed: you are bound to this session"
			}
			return res, err
		}
	}
	// 2. A session inside a project the user trusts, for a Bot bound inside it.
	if mayAlone {
		if pt := s.agentProjectCovering(d.ID, sister, project); pt != nil {
			h.Audit(auditMandateAll, map[string]any{"mandate": pt.ID, "project": pt.Root, "device": d.ID, "sister": sister, "action": "t3_rename",
				"session": sess.SessionID, "directory": sess.Cwd, "uses": pt.Uses})
			res, err := s.executeRename(ctx, h, t, "bot", "project trust "+pt.ID, all)
			if m, ok := res.(map[string]any); ok && err == nil {
				m["approval"] = "none needed: " + pt.ID + ", the user's trust of " + pt.Root
			}
			return res, err
		}
	}
	// 3. Anything else is the user's call.
	what := fmt.Sprintf("Names this session %q in Rubi", name)
	summary := fmt.Sprintf("Name a Claude Code session on %s %q for %s", d.Name, oneLine(name, 40), sister)
	if in.Clear {
		what = "Removes this session's name in Rubi"
		summary = fmt.Sprintf("Remove the name of a Claude Code session on %s for %s", d.Name, sister)
	}
	preview := map[string]any{"bot": sister, "computer": d.Name, "session": sess.SessionID, "title": sess.Title, "directory": sess.Cwd,
		"current_name": old.Name, "new_name": name,
		"what_it_does": what + ". It is a label kept in Rubi, shown in session lists and usable instead of the session's id; it changes nothing in T3."}
	if old.Name != "" {
		preview["current_name_set_by"] = firstNonEmpty(old.SetBy, "unknown")
	}
	return h.Submit(ctx, rubiplugin.Request{Kind: kindRename, Summary: summary, Preview: preview, Payload: t})
}

// executeRename sets or clears the name once it is allowed (by being the Bot's own session, a trust, an
// approval or the user in the panel). by and why go to the audit log. all is the computer's session list when
// the caller has one; without it (an approval that was answered later) the list is read again, because the
// name must still not equal a session's id by the time it is stored.
func (s *sisters) executeRename(ctx context.Context, h sisterHost, t renameTicket, by, why string, all []deviceSessions) (any, error) {
	if t.Device == "" || t.SessionID == "" && t.AppSessionID == "" {
		return nil, errors.New("bad payload")
	}
	sess := session{SessionID: t.SessionID, AppSessionID: t.AppSessionID}
	if t.Project != "" {
		sess.Source, sess.RepoKey = "agent", "agent:"+t.Project
	}
	if t.Clear {
		old, err := s.st.clearAlias(t.Device, sess)
		if err != nil {
			return nil, err
		}
		if old != "" {
			h.Audit(auditRenamed, map[string]any{"device": t.Device, "session": t.SessionID, "old": oneLine(old, maxNameRunes), "new": "", "by": by, "sister": t.Sister, "approval": why})
		}
		return map[string]any{"cleared": old != "", "session_id": t.SessionID, "previous_name": old}, nil
	}
	name, err := checkAliasName(t.Name)
	if err != nil {
		return nil, err
	}
	if t.Project != "" {
		// An agent session: the runner keeps the name (renameSession), Rubi keeps the alias beside it.
		if all == nil {
			lctx, cancel := context.WithTimeout(ctx, panelListTimeout)
			defer cancel()
			ads := s.listAgent(lctx, h, t.Device)
			if len(ads) == 0 || ads[0].err != nil {
				return nil, errors.New("couldn't check the name against the computer's sessions; try again")
			}
			all = asDeviceSessions(ads)
		}
		if err := freeName(name, all, t.Device); err != nil {
			return nil, err
		}
		rctx, cancel := context.WithTimeout(ctx, panelListTimeout)
		defer cancel()
		if err := h.HomeCall(rctx, t.Device, opAgentRename, withRole(map[string]any{"project": t.Project, "id": t.SessionID, "name": name}, t.Role), nil); err != nil {
			return nil, fmt.Errorf("Rubi Home didn't rename the session (%v); nothing changed", err)
		}
		all = nil
	}
	if all != nil {
		if err := freeName(name, all, t.Device); err != nil {
			return nil, err
		}
	}
	setBy := t.Sister
	if by == "user" {
		setBy = "user"
	}
	old, err := s.st.setAlias(t.Device, t.DeviceName, sess, name, setBy, s.now().UTC())
	if err != nil {
		return nil, err
	}
	h.Audit(auditRenamed, map[string]any{"device": t.Device, "session": t.SessionID, "old": oneLine(old.Name, maxNameRunes), "new": name, "by": by, "sister": t.Sister, "approval": why})
	out := map[string]any{"renamed": true, "session_id": t.SessionID, "name": name,
		"next_step": "t3_sessions now shows this name, and t3_bind accepts it as name."}
	if old.Name != "" {
		out["previous_name"] = old.Name
	}
	return out, nil
}

// noThread is the answer when no T3 thread matches what a Bot named: by id, or by a name, with the reason a name
// failed. device narrows a name to one computer (without one, a name used on several computers is ambiguous).
func (s *sisters) noThread(h sisterHost, device, id, name string) error {
	id, name = strings.TrimSpace(id), strings.TrimSpace(name)
	switch {
	case id != "" && name != "":
		return errors.New("pass session_id or name, not both")
	case id == "" && name == "":
		return errors.New("pass session_id, or the name of a named session (t3_sessions shows both)")
	case id != "":
		return errors.New("no such T3 thread on your computers (list them with t3_sessions)")
	}
	devID := ""
	if device != "" {
		d, err := s.pickDevice(h, device)
		if err != nil {
			return err
		}
		devID = d.ID
	}
	found := s.st.aliasesNamed(devID, name)
	if len(found) == 0 {
		return fmt.Errorf("no session is named %q%s", oneLine(name, maxNameRunes), s.knownNames(devID))
	}
	return fmt.Errorf("the session named %q isn't there any more (list sessions with t3_sessions)", oneLine(name, maxNameRunes))
}

// nameOnSeveralComputers is the refusal for a name used on several computers when no computer was named.
func (s *sisters) nameOnSeveralComputers(device, name string) error {
	name = strings.TrimSpace(name)
	if device != "" || name == "" {
		return nil
	}
	found := s.st.aliasesNamed("", name)
	devs := map[string]bool{}
	var where []string
	for _, a := range found {
		if !devs[a.Device] {
			devs[a.Device] = true
			where = append(where, firstNonEmpty(a.DeviceName, a.Device))
		}
	}
	if len(devs) > 1 {
		return fmt.Errorf("%q names sessions on several computers (%s): pass device", oneLine(name, maxNameRunes), strings.Join(where, ", "))
	}
	return nil
}

func (s *sisters) knownNames(device string) string {
	var names []string
	for _, a := range s.st.aliases() {
		if device == "" || a.Device == device {
			names = append(names, fmt.Sprintf("%q", oneLine(a.Name, maxNameRunes)))
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return " (no session has a name yet; use t3_rename or the Rubi panel)"
	}
	if len(names) > 10 {
		names = append(names[:10], "…")
	}
	return " (named sessions: " + strings.Join(names, ", ") + ")"
}
