package main

// Several run approvals of one session as one approval. A session often asks for more than one command at a
// time (parallel tool calls); each ask is a hook waiting in Rubi Home, so the plugin gets them one by one. The first
// ask of a session waits a short window (askBatchWindow) for the others of the same session; what has arrived then
// becomes one approval with an item for each ask. One ask alone is the approval it always was.
//
// A plugin can't cancel an approval it has submitted, so joining a still-pending approval (as Rubi's own bot
// bundles do) is not possible here; the window is the price of grouping.
//
// Security: an item's preview is the one the single approval had, so the user reads the same tool, directory, args
// and reason for each. Only the ticked items are allowed; every other ask is denied, as is every ask of an approval
// that ends in anything but an approval. Asks of different sessions, projects or computers are never grouped.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

const (
	askBatchWindow = 500 * time.Millisecond
	maxBatchAsks   = 10
	msgLeftOut     = "you left this command out of the approval in Rubi."
)

// pendingRun is a run approval that is waiting for its window to end.
type pendingRun struct {
	it      agentItem
	req     rubiplugin.Request // the single approval, as it would be asked alone
	detail  string
	tool    string
	always  bool
	ticket  ticket
	preview map[string]any
	deny    func(string)
}

// askGroup is the asks of one session collected in the window.
type askGroup struct {
	asks  []*pendingRun
	flush chan struct{}
}

// batchable says whether an ask may be an item: its label shows the whole command (at most 100 characters, no control,
// format or bidi character), or the ask is its own approval where the full text is shown.
func batchable(tool, detail string) bool {
	return utf8.RuneCountInString(detail) <= 100 && utf8.RuneCountInString(tool) <= 40 && !hiddenChars(detail) && !hiddenChars(tool)
}

// collectRun adds an ask to its session's group. The first ask of a group waits for the window and then submits what
// the group has; the others return at once (their answer comes through the group's approval).
func (w *watcher) collectRun(ctx context.Context, d rubiplugin.HomeDevice, pr *pendingRun) {
	key := strings.Join([]string{d.ID, pr.it.Project, normRole(pr.it.Project, pr.it.Role), pr.it.SessionID}, "|")
	w.bmu.Lock()
	if g := w.groups[key]; g != nil {
		g.asks = append(g.asks, pr)
		if len(g.asks) >= maxBatchAsks {
			delete(w.groups, key)
			close(g.flush)
		}
		w.bmu.Unlock()
		return
	}
	if w.groups == nil {
		w.groups = map[string]*askGroup{}
	}
	g := &askGroup{asks: []*pendingRun{pr}, flush: make(chan struct{})}
	w.groups[key] = g
	w.bmu.Unlock()
	timer := time.NewTimer(w.batchWindow)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	case <-g.flush:
	}
	w.bmu.Lock()
	if w.groups[key] == g {
		delete(w.groups, key)
	}
	asks := append([]*pendingRun(nil), g.asks...)
	w.bmu.Unlock()
	if ctx.Err() != nil {
		return // shutting down: the group is gone, and Rubi Home denies what nobody answers
	}
	if len(asks) == 1 {
		w.submitAgent(ctx, d, pr.it, pr.req, pr.detail, pr.deny)
		return
	}
	w.submitRunBatch(ctx, d, asks)
}

// batchTicket is the payload of a run_batch approval: what each item answers.
type batchTicket struct {
	Device       string     `json:"device"`
	DeviceName   string     `json:"device_name,omitempty"`
	SessionID    string     `json:"session_id"`
	AgentProject string     `json:"agent_project,omitempty"`
	Cwd          string     `json:"cwd,omitempty"` // the scope of a mandate given with this approval
	TrustOffered bool       `json:"trust_offered,omitempty"`
	Asks         []batchAsk `json:"asks"`
}

type batchAsk struct {
	Key   string `json:"key"`
	AskID string `json:"ask_id"`
}

func (w *watcher) submitRunBatch(ctx context.Context, d rubiplugin.HomeDevice, asks []*pendingRun) {
	denyAll := func(msg string) {
		for _, a := range asks {
			a.deny(msg)
		}
	}
	first := asks[0].it
	t := batchTicket{Device: d.ID, DeviceName: d.Name, SessionID: first.SessionID, AgentProject: first.Project, Cwd: first.root}
	var items []rubiplugin.Item
	var labels []string
	always, sameRoot := false, true
	var life time.Time
	for i, a := range asks {
		key := "c" + strconv.Itoa(i+1)
		t.Asks = append(t.Asks, batchAsk{Key: key, AskID: a.it.AskID})
		label := a.tool + ": " + a.detail
		items = append(items, rubiplugin.Item{Key: key, Label: label, Preview: a.preview})
		labels = append(labels, label)
		always = always || a.always
		sameRoot = sameRoot && a.it.root == first.root
		if a.it.ExpiresAt.After(life) {
			life = a.it.ExpiresAt
		}
	}
	if life.IsZero() {
		life = w.s.now().Add(askLife)
	}
	opts := manifestRunOptions()
	opts[0].Label = "Allow the ticked commands"
	opts[1].Label = "Allow the ticked commands, and trust this directory's plain listed commands for 2 hours"
	t.TrustOffered = !always && sameRoot
	if !t.TrustOffered {
		// An ask that no trust may cover (or asks from different directories): allow only, once each.
		opts = []rubiplugin.Option{{Key: optionAllow, Label: "Allow", Meaning: "Claude runs each ticked command once"}}
	}
	preview := map[string]any{"computer": d.Name, "session": first.SessionID, "project": first.Project, "directory": first.root, "commands": len(asks),
		"what_it_does": "Claude's session asks to run the commands listed as items, each with its own details. Tick the ones to allow: each ticked one runs once. Every one you leave unticked is denied, and so is everything if you deny or let this expire."}
	if always {
		preview["always_asks"] = "At least one of these asks in every mode, bypassPermissions included, and no trust covers it: its item says why."
	}
	req := rubiplugin.Request{Kind: kindRunBatch, Summary: fmt.Sprintf("Claude asks to run %d things in one session: %s", len(asks), oneLine(strings.Join(labels, " | "), 150)),
		Preview: preview, Options: opts, Items: items, Payload: t}
	out, err := w.h.Submit(ctx, req)
	if err != nil {
		w.h.Logf("%s: asking about %d commands: %v", d.Name, len(asks), err)
		denyAll("Rubi couldn't ask you (" + oneLine(err.Error(), 200) + "), so this was denied.")
		return
	}
	id, _ := out["approval_id"].(string)
	if out["status"] != "awaiting_approval" || id == "" {
		return // no approval needed: execute already answered
	}
	ev := map[string]any{"approval_id": id, "level": out["level"], "command": oneLine(strings.Join(labels, " | "), 300), "directory": first.root,
		"computer": d.Name, "expires_at": life, "project": first.Project}
	for _, k := range []string{"approval_url", "buttons", "after_sending_the_link"} {
		if v, ok := out[k]; ok {
			ev[k] = v
		}
	}
	if _, err := w.h.Emit(eventNeeded, ev); err != nil {
		w.h.Logf("%s: telling the agent about %s: %v", d.Name, id, err)
	}
	w.follow(ctx, request{ExpiresAt: life, denyFailed: true}, id, denyAll)
}

// splitBatchOption takes apart the option of a batch ("allow|items:c1,c2", or "items:c1" with one option): the
// option key and the chosen item keys.
func splitBatchOption(option string) (string, []string) {
	key := ""
	if k, rest, ok := strings.Cut(option, "|items:"); ok {
		key, option = k, "items:"+rest
	}
	rest, ok := strings.CutPrefix(option, "items:")
	if !ok || rest == "" {
		return key, nil
	}
	return key, strings.Split(rest, ",")
}

// runBatchApproved runs once the user decided a batch: the ticked asks are allowed, every other is denied. Nothing
// is allowed unless the option names its items.
func runBatchApproved(ctx context.Context, h sisterHost, s *sisters, option string, payload json.RawMessage) (any, error) {
	var t batchTicket
	if err := json.Unmarshal(payload, &t); err != nil || t.Device == "" || len(t.Asks) == 0 {
		return nil, errors.New("bad payload")
	}
	key, chosen := splitBatchOption(option)
	known := map[string]bool{}
	for _, a := range t.Asks {
		if a.AskID == "" || a.Key == "" {
			return nil, errors.New("bad payload")
		}
		known[a.Key] = true
	}
	pick := map[string]bool{}
	for _, k := range chosen {
		if !known[k] {
			chosen, pick = nil, map[string]bool{} // an item that isn't there: allow nothing
			break
		}
		pick[k] = true
	}
	var allowed, denied, failed []string
	for _, a := range t.Asks {
		d := agentDecision{AskID: a.AskID, Behavior: "deny", Message: msgLeftOut}
		if pick[a.Key] {
			d = agentDecision{AskID: a.AskID, Behavior: "allow"}
		}
		if err := h.HomeCall(ctx, t.Device, opAgentDecide, d, nil); err != nil {
			failed = append(failed, a.Key)
			continue
		}
		if d.Behavior == "allow" {
			allowed = append(allowed, a.Key)
		} else {
			denied = append(denied, a.Key)
		}
	}
	h.Audit("run_batch_decided", map[string]any{"device": t.Device, "session": t.SessionID, "project": t.AgentProject, "allowed": len(allowed), "denied": len(denied), "failed": len(failed)})
	out := map[string]any{"allowed": allowed, "denied": denied}
	if len(failed) > 0 {
		out["no_longer_waiting"] = failed
	}
	if key == optionTrust && t.TrustOffered && len(allowed) > 0 {
		out["trust"] = sessionTrust(h, s, ticket{Device: t.Device, DeviceName: t.DeviceName, SessionID: t.SessionID, Cwd: t.Cwd, AgentProject: t.AgentProject})
	}
	return out, nil
}
