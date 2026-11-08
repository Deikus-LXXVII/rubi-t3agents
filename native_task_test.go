package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

const nativeFixtureID = "task_0123456789abcdef01234567"

type nativeHost struct {
	noAutonomy bool
	*fakeHost
	state                             nativeTaskView
	starts, publishes, appends, stops int
	failEmit, failAck                 bool
	unsupported                       bool
	failPublish                       bool
	launch                            json.RawMessage
}

func (h *nativeHost) HomeCall(ctx context.Context, device, op string, args, out any) error {
	var value any
	switch op {
	case "t3-agent.task.capabilities":
		if h.unsupported {
			return errors.New("unknown operation")
		}
		value = map[string]any{"version": 1, "supervision": 1, "supported": true, "autonomous": !h.noAutonomy}
	case "t3-agent.task.list":
		value = map[string]any{"tasks": []nativeTaskView{h.state}}
	case "t3-agent.task.ack":
		if h.failAck {
			h.failAck = false
			return errors.New("fixture acknowledgement unavailable")
		}
		h.state.ResultDelivered = true
		value = map[string]any{"ok": true}
	case "t3-agent.task.status":
		value = h.state
	case "t3-agent.start":
		h.starts++
		h.launch, _ = json.Marshal(args)
		value = map[string]any{"task": nativeFixtureID}
	case "t3-agent.task.publish":
		h.publishes++
		h.state.Phase = "publishing"
		if h.failPublish {
			h.failPublish = false
			return errors.New("retry publication")
		}
		h.state.Phase = "finished"
		h.state.ExecutionStopped = true
		value = h.state
	case "t3-agent.task.append":
		h.appends++
		value = map[string]any{"ok": true}
	case "t3-agent.task.stop":
		h.stops++
		value = map[string]any{"ok": true}
	default:
		return errors.New("unexpected Home operation")
	}
	b, _ := json.Marshal(value)
	return json.Unmarshal(b, out)
}
func nativeFixture(t *testing.T) (*nativeHost, *sisters) {
	t.Helper()
	h := &nativeHost{fakeHost: &fakeHost{devices: []rubiplugin.HomeDevice{{ID: "box", Name: "Box"}}}}
	st := &store{}
	if err := st.load(h); err != nil {
		t.Fatal(err)
	}
	h.state.ID = nativeFixtureID
	h.state.Phase = "waiting"
	h.state.Spec.From = "Citri"
	h.state.Spec.MessageID = "original-request"
	h.state.Spec.AutoFinish = true
	h.state.Workspace.Branch = "rubi/" + nativeFixtureID
	h.state.PanelURL = "https://rubi-panel.com/#fixture"
	return h, newSisters(st)
}
func TestNativeTaskOneExactConfirmationBeforeLaunch(t *testing.T) {
	h, s := nativeFixture(t)
	in := nativeTaskStartIn{VerifiedAgent: "Citri", Device: "box", Project: "project-a", Role: "role-a", Prompt: "fix tests", Provider: "codex", Model: "fixture-model", Publish: &nativeTarget{Repository: "owner/repo", Base: "main", Account: "github-account-id"}}
	if _, err := s.nativeTaskStart(context.Background(), h, in); err != nil {
		t.Fatal(err)
	}
	if h.starts != 0 || len(h.submits) != 1 {
		t.Fatal("launch before one confirmation")
	}
	req := h.submits[0]
	if req.Kind != "t3agents.task_start" {
		t.Fatal("wrong approval kind")
	}
	payload, _ := json.Marshal(req.Payload)
	if _, err := executeNativeTask(context.Background(), h, "start", payload); err != nil {
		t.Fatal(err)
	}
	if h.starts != 1 {
		t.Fatal("not exactly one launch")
	}
	var ticket nativeTaskTicket
	json.Unmarshal(payload, &ticket)
	if string(h.launch) != string(ticket.Args) {
		t.Fatal("launched arguments differ from approved mandate")
	}
	var args map[string]any
	json.Unmarshal(h.launch, &args)
	if args["task"] != true || args["auto_finish"] != true || args["mode"] != "bypassPermissions" || args["from"] != "Citri" || !strings.Contains(args["prompt"].(string), nativeTaskOrders) {
		t.Fatal("incomplete mandate")
	}
	if _, err := s.nativeTaskStart(context.Background(), h, nativeTaskStartIn{Prompt: "spoof"}); err == nil {
		t.Fatal("unverified requester allowed")
	}
}
func TestNativeTaskOnlyRequesterCanReadAppendOrStop(t *testing.T) {
	h, s := nativeFixture(t)
	for _, op := range []string{"status", "append", "stop"} {
		if _, err := s.nativeTaskAction(context.Background(), h, op, nativeTaskIn{VerifiedAgent: "Other Bot", Device: "box", ID: nativeFixtureID, Text: "steer"}, false); err == nil {
			t.Fatal("foreign requester allowed", op)
		}
	}
	if h.appends != 0 || h.stops != 0 {
		t.Fatal("foreign mutation reached Home")
	}
	for _, op := range []string{"status", "append", "stop"} {
		if _, err := s.nativeTaskAction(context.Background(), h, op, nativeTaskIn{VerifiedAgent: "Citri", Device: "box", ID: nativeFixtureID, Text: "steer"}, false); err != nil {
			t.Fatal(err)
		}
	}
	if h.appends != 1 || h.stops != 1 {
		t.Fatal("requester actions not routed")
	}
}
func TestNativeTaskAutomaticFinishRetriesAndDeliverySurvivesRestart(t *testing.T) {
	h, s := nativeFixture(t)
	ctx := context.Background()
	s.nativeTaskTick(ctx, h, "box")
	if h.publishes != 0 {
		t.Fatal("ordinary idle turn auto-published")
	}
	h.state.Summary.Status = "PASS"
	h.state.Summary.Tests = "go test ./...: pass"
	h.failPublish = true
	s.nativeTaskTick(ctx, h, "box")
	if h.publishes != 1 || len(h.events) != 0 {
		t.Fatal("failed publication claimed done")
	}
	s.nativeTaskTick(ctx, h, "box")
	if h.publishes != 2 || len(h.events) != 1 {
		t.Fatal("publication not retried/delivered")
	}
	b, _ := json.Marshal(h.events[0])
	if !strings.Contains(string(b), "Citri") || !strings.Contains(string(b), "original-request") || !strings.Contains(string(b), "https://rubi-panel.com/") {
		t.Fatal("misaddressed/incomplete result")
	}
	st := &store{}
	if err := st.load(h); err != nil {
		t.Fatal(err)
	}
	newSisters(st).nativeTaskTick(ctx, h, "box")
	if h.publishes != 2 || len(h.events) != 1 {
		t.Fatal("restart duplicated publication/result")
	}
}
func TestNativeTaskStopMessageRequiresExecutionReceipt(t *testing.T) {
	h, s := nativeFixture(t)
	h.state.Phase = "stopped"
	s.nativeTaskTick(context.Background(), h, "box")
	if len(h.events) != 0 {
		t.Fatal("unknown execution reported stopped")
	}
	h.state.ExecutionStopped = true
	s.nativeTaskTick(context.Background(), h, "box")
	if len(h.events) != 1 {
		t.Fatal("confirmed stop not delivered")
	}
}
func TestNativeTaskCardTreatsAgentTextAsDataAndRejectsForeignPR(t *testing.T) {
	h, s := nativeFixture(t)
	h.state.Summary.Work = `</script><img src=x onerror=alert(1)>`
	h.state.PR = "javascript:alert(1)"
	h.state.Spec.Publish = &nativeTarget{Repository: "owner/repo"}
	page, err := s.nativeTaskPage(context.Background(), h, "box")
	if err != nil {
		t.Fatal(err)
	}
	pg := page.(map[string]any)
	items := pg["sections"].([]map[string]any)[0]["items"].([]map[string]any)
	if !strings.Contains(items[0]["detail"].(string), h.state.Summary.Work) {
		t.Fatal("text lost")
	}
	for _, action := range items[0]["actions"].([]map[string]any) {
		if action["href"] != nil {
			t.Fatal("untrusted PR became link")
		}
	}
	if items[0]["html"] != nil || items[0]["input"] == nil {
		t.Fatal("unsafe or missing card actions")
	}
	h.state.PR = "https://github.com/owner/repo/pull/1"
	page, err = s.nativeTaskPage(context.Background(), h, "box")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(page)
	if !strings.Contains(string(b), h.state.PR) {
		t.Fatal("canonical PR link missing")
	}
}
func TestNativeTaskStartRateLimitCannotBecomeApprovalFlood(t *testing.T) {
	h, s := nativeFixture(t)
	in := nativeTaskStartIn{VerifiedAgent: "Citri", Device: "box", Project: "a", Role: "r", Prompt: "work"}
	for i := 0; i < 5; i++ {
		if _, err := s.nativeTaskStart(context.Background(), h, in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.nativeTaskStart(context.Background(), h, in); err == nil {
		t.Fatal("rate limit absent")
	}
	if len(h.submits) != 5 {
		t.Fatal("approval flood")
	}
}

func TestNativeTaskNeverFallsBackToAnOldHome(t *testing.T) {
	h, s := nativeFixture(t)
	h.unsupported = true
	in := nativeTaskStartIn{VerifiedAgent: "Citri", Device: "box", Project: "a", Role: "r", Prompt: "work"}
	if _, err := s.nativeTaskStart(context.Background(), h, in); err == nil {
		t.Fatal("old Home accepted")
	}
	payload, _ := json.Marshal(nativeTaskTicket{Device: "box", Args: json.RawMessage(`{"task":true}`)})
	if _, err := executeNativeTask(context.Background(), h, "start", payload); err == nil {
		t.Fatal("old Home start fallback")
	}
	if h.starts != 0 || len(h.submits) != 0 {
		t.Fatal("old Home created an unsafe task/approval")
	}
}
func TestNativeGitHubSetupNeverCopiesTokenToSettingsOrErrors(t *testing.T) {
	token := "fixture-not-a-real-token"
	settings, label, err := validateNativeAccount(context.Background(), map[string]string{"github_label": "Publishing"}, map[string]string{"github_token": token})
	if err != nil || label != "Publishing" {
		t.Fatal("account metadata not validated")
	}
	b, _ := json.Marshal(settings)
	if strings.Contains(string(b), token) {
		t.Fatal("token leaked into public settings")
	}
	_, _, err = validateNativeAccount(context.Background(), nil, map[string]string{"github_token": token + "\n"})
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatal("invalid token leaked or accepted")
	}
	if _, _, err = validateNativeAccount(context.Background(), nil, nil); err != nil {
		t.Fatal("local task setup requires a GitHub token")
	}
}

func (h *nativeHost) EmitTo(to, kind string, data map[string]any) (string, error) {
	if h.failEmit {
		h.failEmit = false
		return "", errors.New("fixture delivery unavailable")
	}
	return h.fakeHost.EmitTo(to, kind, data)
}
func TestNativeResultDeliveryFailureNeverAcknowledgesOrLosesResult(t *testing.T) {
	h, s := nativeFixture(t)
	h.state.Phase = "finished"
	h.state.ExecutionStopped = true
	h.failEmit = true
	s.nativeTaskTick(context.Background(), h, "box")
	if h.state.ResultDelivered || len(h.events) != 0 {
		t.Fatal("failed delivery acknowledged")
	}
	s.nativeTaskTick(context.Background(), h, "box")
	if !h.state.ResultDelivered || len(h.events) != 1 {
		t.Fatal("delivery not retried")
	}
	newSisters(s.st).nativeTaskTick(context.Background(), h, "box")
	if len(h.events) != 1 {
		t.Fatal("receipt lost after plugin restart")
	}
}
func TestNativeLostAckRetainsStableAtLeastOnceEvent(t *testing.T) {
	h, s := nativeFixture(t)
	h.state.Phase = "finished"
	h.state.ExecutionStopped = true
	h.failAck = true
	s.nativeTaskTick(context.Background(), h, "box")
	newSisters(s.st).nativeTaskTick(context.Background(), h, "box")
	if len(h.events) != 2 || h.events[0]["event_id"] != h.events[1]["event_id"] || !h.state.ResultDelivered {
		t.Fatal("lost acknowledgement changed deduplication identity")
	}
}
func TestNativeLegacyFinishedDoesNotClaimCleanup(t *testing.T) {
	h, s := nativeFixture(t)
	h.state.Phase = "finished"
	h.state.ExecutionStopped = false
	s.nativeTaskTick(context.Background(), h, "box")
	if len(h.events) != 0 || h.state.ResultDelivered {
		t.Fatal("legacy outcome falsely confirmed")
	}
	page, _ := s.nativeTaskPage(context.Background(), h, "box")
	raw, _ := json.Marshal(page)
	if !strings.Contains(string(raw), "UNCONFIRMED") {
		t.Fatal("legacy cleanup uncertainty hidden")
	}
}

func TestNativeTaskRefusesBeforeApprovalWhenHomeCeilingDisallowsAutonomy(t *testing.T) {
	h, s := nativeFixture(t)
	h.noAutonomy = true
	if _, err := s.nativeTaskStart(context.Background(), h, nativeTaskStartIn{VerifiedAgent: "Citri", Device: "box", Project: "a", Role: "r", Prompt: "work"}); err == nil {
		t.Fatal("interactive task accepted as unattended")
	}
	if h.starts != 0 || len(h.submits) != 0 {
		t.Fatal("policy refusal created work or an unusable approval")
	}
}
