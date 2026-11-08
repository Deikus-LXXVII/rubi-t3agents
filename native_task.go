package main

// Native repository tasks are owned and journalled by Home, not the legacy VM
// task model. The plugin handles approval, caller scoping and presentation only.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

const kindNativeTask = "t3agents.task_start"
const eventNativeTask = "task_result"

var nativeTaskID = regexp.MustCompile(`^task_[a-f0-9]{24}$`)

type nativeTarget struct {
	Repository string `json:"repository"`
	Base       string `json:"base"`
	Account    string `json:"account"`
}
type nativeTaskStartIn struct {
	Agent         string        `json:"rubi_agent"`
	AgentCode     string        `json:"rubi_agent_code,omitempty"`
	VerifiedAgent string        `json:"rubi_verified_agent,omitempty"`
	Device        string        `json:"device"`
	Project       string        `json:"project"`
	Role          string        `json:"role"`
	Prompt        string        `json:"prompt"`
	Provider      string        `json:"provider,omitempty"`
	Model         string        `json:"model,omitempty"`
	Publish       *nativeTarget `json:"publish,omitempty"`
}
type nativeTaskIn struct {
	Agent         string `json:"rubi_agent"`
	AgentCode     string `json:"rubi_agent_code,omitempty"`
	VerifiedAgent string `json:"rubi_verified_agent,omitempty"`
	Device        string `json:"device"`
	ID            string `json:"id"`
	Text          string `json:"text,omitempty"`
}
type nativeTaskTicket struct {
	Device string          `json:"device"`
	Args   json.RawMessage `json:"args"`
}
type nativeTaskView struct {
	ResultDelivered  bool   `json:"result_delivered,omitempty"`
	ExecutionStopped bool   `json:"execution_stopped,omitempty"`
	ID               string `json:"id"`
	Phase            string `json:"phase"`
	Thread           string `json:"thread,omitempty"`
	PR               string `json:"pr,omitempty"`
	Commit           string `json:"commit,omitempty"`
	Error            string `json:"error,omitempty"`
	PanelURL         string `json:"panel_url,omitempty"`
	Spec             struct {
		Project    string        `json:"project"`
		Role       string        `json:"role"`
		From       string        `json:"from"`
		Bot        string        `json:"bot"`
		MessageID  string        `json:"message_id"`
		AutoFinish bool          `json:"auto_finish"`
		Publish    *nativeTarget `json:"publish,omitempty"`
	} `json:"spec"`
	Workspace struct {
		Branch string `json:"branch"`
	} `json:"workspace"`
	Summary struct {
		Work   string `json:"work"`
		Tests  string `json:"tests"`
		Diff   string `json:"diff"`
		Status string `json:"status"`
	} `json:"summary"`
}

func (v nativeTaskView) terminal() bool {
	return v.Phase == "finished" || v.Phase == "failed" || v.Phase == "stopped"
}
func (v nativeTaskView) autoReady() bool {
	return v.Spec.AutoFinish && (v.Phase == "publishing" || v.Phase == "pushed" || v.Phase == "waiting" && v.Summary.Status == "PASS")
}

func requireNativeTasks(ctx context.Context, h homeCaller, device string) error {
	var c struct {
		Version     int  `json:"version"`
		Supported   bool `json:"supported"`
		Supervision int  `json:"supervision"`
	}
	if h.HomeCall(ctx, device, "t3-agent.task.capabilities", map[string]any{}, &c) != nil || c.Version != 1 || c.Supervision != 1 || !c.Supported {
		return errors.New("task: update Rubi Core, Home and rubi-agent-exec together; supervised repository task API unavailable (no legacy start fallback)")
	}
	return nil
}

const nativeTaskOrders = "\n\nRubi repository task: work only in this private copy and its assigned branch. Never push or merge; Rubi publishes the approved branch. When the task is actually complete, end your final answer with STATUS: PASS (or ISSUES/BLOCKED when incomplete), then COMMIT: <sha or none>, then one or more EVIDENCE: <test command and result> lines. A question is not completion."

func (s *sisters) nativeTaskStart(ctx context.Context, h sisterHost, in nativeTaskStartIn) (any, error) {
	if strings.TrimSpace(in.VerifiedAgent) == "" {
		return nil, errors.New("verify your Bot identity first")
	}
	if in.Project == "" || in.Role == "" || strings.TrimSpace(in.Prompt) == "" || len([]rune(in.Prompt)) > 16000 {
		return nil, errors.New("task: explicit project, role and 1–16000 character prompt required")
	}
	if err := checkStartText(in.Prompt); err != nil {
		return nil, err
	}
	if !s.allow("task-start|"+in.VerifiedAgent, 5) || !s.allow("task-start|*", 20) {
		return nil, errors.New("task: start rate limit reached")
	}
	d, err := s.pickDevice(h, in.Device)
	if err != nil {
		return nil, err
	}
	if err := requireNativeTasks(ctx, h, d.ID); err != nil {
		return nil, err
	}
	provider := in.Provider
	if provider == "" {
		provider = "claudeAgent"
	}
	if provider != "claudeAgent" && in.Model == "" {
		return nil, errors.New("task: this provider requires an explicit model")
	}
	args := map[string]any{"project": in.Project, "role": in.Role, "prompt": in.Prompt + nativeTaskOrders, "provider": provider, "task": true, "auto_finish": true, "from": in.VerifiedAgent, "bot": botID(in.VerifiedAgent), "message_id": newID("task-request_")}
	if in.Model != "" {
		args["model"] = in.Model
	}
	if in.Publish != nil {
		args["publish"] = in.Publish
	}
	raw, _ := json.Marshal(args)
	return h.Submit(ctx, rubiplugin.Request{Kind: kindNativeTask, Summary: "Run an isolated repository task for " + oneLine(in.Project, 64),
		Preview: map[string]any{"computer": d.Name, "project": in.Project, "role": in.Role, "provider": provider, "model": in.Model, "task": in.Prompt, "standing_orders": nativeTaskOrders, "publish": in.Publish, "scope": "A separate repository copy and rubi/task_* branch; project-only srt sandbox; no main changes or merges. Final PASS seals the task and publishes only the approved branch, if configured."},
		Payload: nativeTaskTicket{Device: d.ID, Args: raw}})
}
func executeNativeTask(ctx context.Context, h homeCaller, option string, payload json.RawMessage) (any, error) {
	if option != "start" {
		return nil, errors.New("task: unsupported approval option")
	}
	var t nativeTaskTicket
	if json.Unmarshal(payload, &t) != nil || t.Device == "" || len(t.Args) == 0 {
		return nil, errors.New("task: invalid mandate")
	}
	if err := requireNativeTasks(ctx, h, t.Device); err != nil {
		return nil, err
	}
	var out json.RawMessage
	err := h.HomeCall(ctx, t.Device, "t3-agent.start", t.Args, &out)
	return out, err
}
func (s *sisters) nativeTaskAction(ctx context.Context, h sisterHost, op string, in nativeTaskIn, panel bool) (any, error) {
	if !nativeTaskID.MatchString(in.ID) || in.Device == "" {
		return nil, errors.New("task: device and task ID required")
	}
	if op != "status" && op != "append" && op != "stop" {
		return nil, errors.New("task: unsupported action")
	}
	key := in.VerifiedAgent
	if panel {
		key = "panel"
	}
	if !s.allow("task-action|"+key, 600) {
		return nil, errors.New("task: action rate limit reached")
	}
	var state nativeTaskView
	if err := h.HomeCall(ctx, in.Device, "t3-agent.task.status", map[string]any{"id": in.ID}, &state); err != nil {
		return nil, err
	}
	if state.ID != in.ID {
		return nil, errors.New("task: mismatched status receipt")
	}
	if !panel && (in.VerifiedAgent == "" || !sameBot(in.VerifiedAgent, state.Spec.From)) {
		return nil, errors.New("task: belongs to another requester")
	}
	if op == "status" {
		return state, nil
	}
	args := map[string]any{"id": in.ID}
	if op == "append" {
		if strings.TrimSpace(in.Text) == "" || len([]rune(in.Text)) > 4000 {
			return nil, errors.New("task: append needs 1–4000 characters")
		}
		who := in.VerifiedAgent
		if panel {
			who = "local:panel"
		}
		args["text"], args["from"], args["bot"], args["message_id"] = in.Text, who, botID(who), newID("task-message_")
	}
	var out json.RawMessage
	err := h.HomeCall(ctx, in.Device, "t3-agent.task."+op, args, &out)
	return out, err
}

// Home is the durable source of truth. No plugin-side running-task cache, and no
// replayed start after cancellation. An error keeps publication retryable.
func (s *sisters) nativeTaskTick(ctx context.Context, h sisterHost, device string) {
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	var out struct {
		Tasks []nativeTaskView `json:"tasks"`
	}
	if h.HomeCall(ctx, device, "t3-agent.task.list", map[string]any{}, &out) != nil {
		return
	}
	for _, state := range out.Tasks {
		if ctx.Err() != nil {
			return
		}
		if !nativeTaskID.MatchString(state.ID) {
			continue
		}
		if state.autoReady() {
			var done nativeTaskView
			if h.HomeCall(ctx, device, "t3-agent.task.publish", map[string]any{"id": state.ID}, &done) == nil {
				state = done
			}
		}
		if state.terminal() && !state.ExecutionStopped {
			continue
		}
		if !state.terminal() || state.Spec.From == "" || strings.HasPrefix(strings.ToLower(state.Spec.From), "local:") {
			continue
		}
		key := device + "/" + state.ID + ":terminal"
		if state.ResultDelivered {
			continue
		}

		// Mint an authenticated full panel link through Core's status broker.
		var linked nativeTaskView
		if h.HomeCall(ctx, device, "t3-agent.task.status", map[string]any{"id": state.ID}, &linked) == nil && linked.ID == state.ID {
			state = linked
		}
		data := map[string]any{"event_id": key, "task_id": state.ID, "phase": state.Phase, "execution_stopped": state.ExecutionStopped, "project": state.Spec.Project, "branch": state.Workspace.Branch, "commit": state.Commit, "pr": state.PR, "work": state.Summary.Work, "diff": state.Summary.Diff, "tests_reported_by_agent": state.Summary.Tests, "error": state.Error, "panel_url": state.PanelURL, "in_reply_to": []string{state.Spec.MessageID}}
		if _, err := h.EmitTo(state.Spec.From, eventNativeTask, data); err != nil {
			continue
		}
		var ack json.RawMessage
		_ = h.HomeCall(ctx, device, "t3-agent.task.ack", map[string]any{"id": state.ID, "event_id": state.ID + ":terminal"}, &ack)
	}
}
func (s *sisters) nativeTaskPage(ctx context.Context, h sisterHost, device string) (any, error) {
	if device == "" {
		d, err := s.pickDevice(h, "")
		if err != nil {
			return nil, err
		}
		device = d.ID
	}
	var out struct {
		Tasks []nativeTaskView `json:"tasks"`
	}
	if err := h.HomeCall(ctx, device, "t3-agent.task.list", map[string]any{}, &out); err != nil {
		return nil, err
	}
	items := []map[string]any{}
	for _, v := range out.Tasks {
		if !nativeTaskID.MatchString(v.ID) {
			continue
		}
		args := map[string]any{"device": device, "id": v.ID}
		actions := []map[string]any{}
		if (!v.terminal() || (v.Phase == "stopped" || v.Phase == "failed") && !v.ExecutionStopped) && v.Phase != "publishing" && v.Phase != "pushed" {
			actions = append(actions, map[string]any{"label": "Stop", "op": "task_stop", "args": args})
		}
		if v.Spec.Publish != nil && regexp.MustCompile(`^https://github\.com/`+regexp.QuoteMeta(v.Spec.Publish.Repository)+`/pull/[1-9][0-9]*$`).MatchString(v.PR) {
			actions = append(actions, map[string]any{"label": "Open pull request", "href": v.PR})
		}
		note := "Running/idle in a private sandboxed task copy."
		if v.autoReady() {
			note = "Automatic finalization/publication pending; retried automatically. Check the approved GitHub account if this persists."
		}
		if v.terminal() {
			note = "Execution cleanup confirmed."
			if !v.ExecutionStopped {
				note = "Mandate ended; execution cleanup is UNCONFIRMED. Retry Stop; do not claim the worker has exited."
			}
		}
		item := map[string]any{"title": v.ID + " · " + v.Phase, "detail": fmt.Sprintf("%s\nBranch: %s\nCommit: %s\n\nWork (agent report):\n%s\n\nDiff (frozen Git snapshot):\n%s\n\nTests (agent-reported, not independently verified):\n%s\n%s", v.Spec.Project, v.Workspace.Branch, v.Commit, v.Summary.Work, v.Summary.Diff, v.Summary.Tests, v.Error) + "\n" + note, "actions": actions, "note": map[bool]string{true: "Execution cleanup confirmed.", false: "A stopped/failed mandate is not proof of process exit; cleanup may be pending."}[v.ExecutionStopped]}
		if !v.terminal() && v.Phase != "publishing" && v.Phase != "pushed" {
			item["input"] = map[string]any{"op": "task_append", "args": args, "arg": "text", "label": "Append to this task", "send": "Send"}
		}
		items = append(items, item)
	}
	return map[string]any{"title": "Repository tasks", "refresh": 5, "up": map[string]any{}, "sections": []map[string]any{{"title": "Tasks", "items": items, "empty": "No repository tasks yet."}}}, nil
}

func registerNativeTasks(p *rubiplugin.Plugin, s *sisters) {
	p.Validate = validateNativeAccount
	rubiplugin.AddTool(p, "t3agents_task_accounts", "List the stable publication account IDs and labels you may use in publish.account; never returns GitHub tokens. An optional fine-grained GitHub token is entered only in the Rubi panel, stored in the vault and resolved by Core at publication time.", func(ctx context.Context, h *rubiplugin.Host, in struct{}) (any, error) { return h.Accounts() })

	p.OnExecute(kindNativeTask, func(ctx context.Context, h *rubiplugin.Host, opt string, payload json.RawMessage) (any, error) {
		return executeNativeTask(ctx, h, opt, payload)
	})
	rubiplugin.AddTool(p, "t3agents_task_start", "Start an isolated repository task with ONE strong confirmation: private copy and branch, srt confinement, automatic finalization after an explicit final PASS report; optional exact-branch GitHub push and PR, never merge. Include project, role, provider/model and optional publish {repository,base,account}. No token in tool arguments.", func(ctx context.Context, h *rubiplugin.Host, in nativeTaskStartIn) (any, error) {
		return s.nativeTaskStart(ctx, h, in)
	})
	for _, op := range []string{"status", "append", "stop"} {
		rubiplugin.AddTool(p, "t3agents_task_"+op, "Repository task "+op+" for its original verified requester. Append never widens the original project/publish mandate; stop is confirmed only after all admitted launchers drain.", func(ctx context.Context, h *rubiplugin.Host, in nativeTaskIn) (any, error) {
			return s.nativeTaskAction(ctx, h, op, in, false)
		})
	}
}

func validateNativeAccount(_ context.Context, fields, secrets map[string]string) (any, string, error) {
	label := strings.TrimSpace(fields["github_label"])
	if label == "" {
		label = "Task publishing"
	}
	if len([]rune(label)) > 64 {
		return nil, "", errors.New("account label is too long")
	}
	token := secrets["github_token"]
	if len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, "", errors.New("invalid GitHub token format; enter it only in the masked panel field")
	}
	// No network call or token echo. Actual repository/scope authorization is
	// verified by Core's exact-target publication broker when the token is used.
	return map[string]string{"github_label": label}, label, nil
}
