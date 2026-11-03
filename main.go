// Command t3agents is the T3 Agents plugin for Rubi: Bots start and steer agent threads that run in T3 Code on the
// user's computer, each in a role VM. Rubi Home on that computer owns T3 and the VMs and offers them to this plugin
// as the "t3-agent" capability (roles, threads, the approval queue, SSH, limits) and the "t3-setup" capability (the
// setup page). The plugin keeps what is Rubi's to decide: the approvals, role changes, mandates, project trust,
// bindings and the pages. See README.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

// PublisherKey is the plugin signing key (public half, base64 SPKI). It is empty in the source: the maintainer's
// release build sets it (-ldflags "-X main.PublisherKey=..."), and a build without it has no publisher key.
var PublisherKey = ""

const (
	kindRun  = "t3agents.run"      // any command: passkey or password in the Rubi panel
	kindSafe = "t3agents.run_safe" // a read-only command on the short list (classify)
	// kindRunBatch is several run approvals of one thread asked at once, an item each (agent_batch.go).
	kindRunBatch = "t3agents.run_batch"
	// eventNeeded tells the agent to send you the approval link.
	eventNeeded = "approval_needed"
)

// manifestRunOptions are the first two options of a run approval: one command, or the session for 2 hours.
func manifestRunOptions() []rubiplugin.Option {
	return []rubiplugin.Option{{Key: optionAllow, Label: "Allow", Meaning: "The agent runs this command once"},
		{Key: optionTrust, Label: "Allow and trust this session for 2 hours",
			Meaning: "The agent runs this command. For 2 hours Rubi also allows this thread's plain, listed commands in this directory by itself, each one audited. Every option of a listed program is checked against a list, and an unknown option asks. That includes builds and tests (make, go test, npm run) and scripts: they run the project's own code with your rights, including network access and writing anywhere; Claude may have just written that code, so choose this only if you trust Claude with your rights in this project. Builds and go mod tidy may fetch declared dependencies, files outside the directory (except credential paths) can be read, and the check does not follow symlinks already in the project. Push, --force, sudo, network commands, options that run another program, credential paths and writing outside the directory or into .git still ask, and so does git rebase unless it says --no-update-refs (Rubi cannot see your git configuration). Revoke it in the panel"}}
}

func manifest() rubiplugin.Manifest {
	opts := []rubiplugin.Option{{Key: optionAllow, Label: "Allow", Meaning: "The agent runs this command once"}}
	trust := manifestRunOptions()
	// Version is the last release; a release build replaces it with its tag (rubiplugin.Version, set
	// with -ldflags).
	return rubiplugin.Manifest{
		ID: "t3agents", Name: "T3 Agents", Version: "v0.1.0",
		Description: "Bots start and steer agent threads that run in T3 Code on your computer, each in a role VM; you approve what matters.",
		Needs:       "Rubi Home on the computer that runs T3 Code, with the T3 Agents setup done (container tool, runner image, T3 installed and paired, a sign-in per role).",
		Publisher:   rubiplugin.Publisher{Name: "Rubi-Project", Key: PublisherKey},
		Source:      "https://github.com/Deikus-LXXVII/rubi-t3agents",
		// Needs a Rubi that knows the "t3-agent" and "t3-setup" capabilities of Rubi Home; this plugin version must not be
		// published before the Rubi release that has them.
		MinRubi: "v0.14.0-rc.2",
		Entry:   "t3agents",
		Actions: []rubiplugin.Action{
			{Kind: kindRun, Title: "Run a command in a T3 thread", DefaultLevel: rubiplugin.Strong, Locked: true, Options: trust},
			{Kind: kindRunBatch, Title: "Run several commands of one T3 thread (you tick the ones to allow)", DefaultLevel: rubiplugin.Strong, Locked: true, Options: trust},
			// Read-only commands: a tap on the passkey is enough, no Face ID. (Chat would need the agent to
			// show the buttons.) You can change it in the panel.
			{Kind: kindSafe, Title: "Run a read-only command in a T3 thread (ls, cat, pwd, git status/diff/log)",
				DefaultLevel: rubiplugin.Tap, Options: opts},
			{Kind: kindBind, Title: "Let a Bot talk to a T3 thread", DefaultLevel: rubiplugin.Strong, Locked: true,
				Options: []rubiplugin.Option{{Key: "bind", Label: "Bind", Meaning: "The Bot can send this thread short messages and gets its answers and questions; it can't approve commands"}}},
			{Kind: kindTrustProject, Title: "Let a Bot's project be trusted (until 04:00, or until revoked)", DefaultLevel: rubiplugin.Strong, Locked: true,
				Options: []rubiplugin.Option{{Key: optionTrustProject, Label: "Trust this project until 04:00",
					Meaning: "Until the next 04:00 (at most 24 hours; the approval names the exact time, date, time zone, the project directory, the computer and the branches) Rubi allows the plain, listed commands of every T3 thread of the project, and lets the Bot start and bind threads in it without a new approval. Commits go to the branch a thread is on, which may be main. Push and every remote git command, --force, sudo, network commands, installs, tags, credential paths, writing outside the project and branches outside the pattern still ask"},
					{Key: optionTrustForever, Label: "Trust this project until I revoke it",
						Meaning: "UNTIL REVOKED — no end time: this trust has no time or count limit, stays on across restarts of Rubi, of the plugin and of Rubi Home and across days and weeks, and only a revoke (the panel, or t3agents_mandates) ends it; while Rubi is locked nothing is allowed. Rubi allows the plain, listed commands of every T3 thread of this project, each one audited and counted, with the same checks as the 2-hour trust, including builds and tests that run the project's own code with your rights. Switching to or creating a git branch is allowed only for names like agent/*; commits, merges and rebases go to the branch a session is on, which may be main. A Bot bound to a thread in the project can also start and bind threads in it, without a new approval, until you revoke it. Push and every remote git command, --force, sudo, network commands, installs, git tag, credential paths, writing outside the project or into .git, branches outside the pattern, and git rebase without --no-update-refs still ask. Because nothing ends it by itself, look at the panel from time to time and revoke what you no longer need."}}},
			{Kind: kindRename, Title: "Let a Bot name a T3 thread", DefaultLevel: rubiplugin.Tap, Locked: true,
				Options: []rubiplugin.Option{{Key: "rename", Label: "Rename", Meaning: "Gives (or removes) the thread's name in Rubi. It is a label shown in thread lists and usable instead of the thread's id; it changes nothing in T3 Code"}}},
			{Kind: kindStart, Title: "Let a Bot start a new T3 thread", DefaultLevel: rubiplugin.Strong, Locked: true,
				Options: []rubiplugin.Option{{Key: "start", Label: "Start", Meaning: "Starts a new T3 thread in a role VM on your computer with the Bot's task as its first message, and binds the Bot to it. The role's permission mode applies, never looser than the role allows; in bypassPermissions Claude can run almost anything in the VM without asking"}}},
			{Kind: kindAgentQuestion, Title: "Answer a question Claude asks in a project VM", DefaultLevel: rubiplugin.Tap, Locked: true,
				Options: []rubiplugin.Option{{Key: "opt0", Label: "Answer", Meaning: "Gives Claude the option you choose as the answer to its question; approves nothing else"}}},
			{Kind: kindFork, Title: "Let a Bot fork a Claude session in a project VM", DefaultLevel: rubiplugin.Strong, Locked: true,
				Options: []rubiplugin.Option{{Key: "fork", Label: "Fork", Meaning: "Makes a branch of that session's conversation as a new, idle session in the same project"}}},
			{Kind: kindForkOwn, Title: "Let a Bot fork the Claude session it is bound to", DefaultLevel: rubiplugin.Tap, Locked: true,
				Options: []rubiplugin.Option{{Key: "fork", Label: "Fork", Meaning: "Makes a branch of the Bot's own session's conversation as a new, idle session in the same project"}}},
			{Kind: kindMode, Title: "Loosen the permission mode of a Claude session in a project VM", DefaultLevel: rubiplugin.Strong, Locked: true,
				Options: []rubiplugin.Option{{Key: "mode", Label: "Change the mode", Meaning: "Moves the session to a looser permission mode: Claude asks fewer questions; bypassPermissions lets it run almost anything in the VM without asking. Push, sudo, the network outside the allow-list, paths outside the project and edits of .rubi/.claude still ask"}}},
			{Kind: kindRoleForeign, Title: "Let a Bot change an agent role on a project it holds no trust for", DefaultLevel: rubiplugin.Strong, Locked: true,
				Options: []rubiplugin.Option{{Key: "apply", Label: "Apply", Meaning: "Makes the change to the agent role that the approval lists, as one new version of the role (the role's history keeps the older ones, so you can revert). Nothing else changes. If the role changed since the Bot asked, the request fails and the Bot has to ask again"}}},
			{Kind: kindRoleWiden, Title: "Let a Bot widen an agent role (MCP servers, plugins, network hosts, extra tools, a looser mode)", DefaultLevel: rubiplugin.Strong, Locked: true,
				Options: []rubiplugin.Option{{Key: "apply", Label: "Apply", Meaning: "Gives the agent role what the approval lists: MCP servers and Claude plugins run code, hosts open the role's VM to more of the network, other tools and a looser permission mode let Claude do more without asking. The role's history keeps the older versions, so you can revert. If the role changed since the Bot asked, the request fails and the Bot has to ask again"}}},
			{Kind: kindRoleSkillAccept, Title: "Accept a skill that Rubi's scan flagged in an agent role", DefaultLevel: rubiplugin.Tap, Locked: true,
				Options: []rubiplugin.Option{{Key: "accept", Label: "Accept", Meaning: "The role uses the skill from its next session on. A skill is only text Claude reads; it grants no tools, hosts or permissions, but flagged text may try to steer Claude (the approval shows what was flagged)"}}},
			{Kind: kindSSHTarget, Title: "Register an SSH target on your computer (a server roles may run commands on)", DefaultLevel: rubiplugin.Strong, Locked: true,
				Options: []rubiplugin.Option{{Key: "register", Label: "Register", Meaning: "Rubi Home on that computer makes a new key pair for the target and pins the server's host key shown in the approval (the fingerprint you confirmed). Roles you later grant the target can run its allowed commands on the server as that user, from your computer; their VMs never get the key or the address. Registering gives no role anything by itself. Bots with a project trust may grant a target with restricted commands, or one that asks each time, to their roles; a target that allows any command needs your approval for each grant"}}},
			{Kind: kindSetupInstall, Title: "Install or upgrade Apple's container tool for the agent path", DefaultLevel: rubiplugin.Strong, Locked: true,
				Options: []rubiplugin.Option{{Key: "install", Label: "Install", Meaning: "Rubi Home on that computer downloads Apple's signed container installer (a pinned release, checked by size, hash and signature) and installs it; macOS then shows its own administrator dialog on that computer"}}},
			{Kind: kindSetupProject, Title: "Add a folder as a project of the agent path", DefaultLevel: rubiplugin.Strong, Locked: true,
				Options: []rubiplugin.Option{{Key: "project", Label: "Choose a folder", Meaning: "A folder chooser opens on that computer; the folder you pick becomes a project: Claude in its VM can read and write everything in it"}}},
		},
		Events: []rubiplugin.EventType{
			{Type: eventNeeded, Untrusted: []string{"command", "directory", "claude_says", "computer"}},
			{Type: eventBound, Untrusted: []string{"title", "directory", "project"}},
			{Type: eventReply, Untrusted: []string{"text", "computer", "evidence"}},
			{Type: eventMessage},
			{Type: eventAsk, Untrusted: []string{"text", "kind", "title", "directory", "computer"}},
		},
		Config: append([]rubiplugin.ConfigField{{Key: "usage_warn_usd", Label: "Warn when today's Claude usage estimate is above (US dollars)", Type: "text", Default: "10",
			Help: "A yellow notice on the usage page; nothing is ever blocked."}}, roleConfigFields()...),
		Panel: "view",
		Home:  []string{"t3-agent", "t3-setup"},
	}
}

// newPlugin is the plugin with every tool and handler registered; main runs it and the manifest test inspects it.
func newPlugin() *rubiplugin.Plugin {
	p := rubiplugin.New(manifest())
	st := &store{}
	sis := newSisters(st)
	var stop context.CancelFunc
	var done chan struct{}
	p.Start = func(h *rubiplugin.Host) error {
		if err := st.load(h); err != nil {
			h.Logf("reading the saved mandates and bindings: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		stop, done = cancel, make(chan struct{})
		w := newWatcher(h, sis)
		w.batchWindow = askBatchWindow
		go func() {
			defer close(done)
			w.run(ctx)
		}()
		return nil
	}
	p.Stop = func() {
		if stop != nil {
			stop()
			<-done
		}
	}
	p.OnExecute(kindRun, func(ctx context.Context, h *rubiplugin.Host, option string, payload json.RawMessage) (any, error) {
		return runApproved(ctx, h, sis, option, payload)
	})
	p.OnExecute(kindRunBatch, func(ctx context.Context, h *rubiplugin.Host, option string, payload json.RawMessage) (any, error) {
		return runBatchApproved(ctx, h, sis, option, payload)
	})
	p.OnExecute(kindSafe, func(ctx context.Context, h *rubiplugin.Host, _ string, payload json.RawMessage) (any, error) {
		return allow(ctx, h, payload)
	})
	p.OnExecute(kindBind, func(_ context.Context, h *rubiplugin.Host, _ string, payload json.RawMessage) (any, error) {
		var t bindTicket
		if err := json.Unmarshal(payload, &t); err != nil {
			return nil, fmt.Errorf("bad payload")
		}
		return sis.executeBind(h, t)
	})
	p.OnExecute(kindTrustProject, func(_ context.Context, h *rubiplugin.Host, _ string, payload json.RawMessage) (any, error) {
		var t trustTicket
		if err := json.Unmarshal(payload, &t); err != nil {
			return nil, fmt.Errorf("bad payload")
		}
		return sis.executeTrust(h, t)
	})
	p.OnExecute(kindStart, func(ctx context.Context, h *rubiplugin.Host, option string, payload json.RawMessage) (any, error) {
		var t startTicket
		if err := json.Unmarshal(payload, &t); err != nil {
			return nil, fmt.Errorf("bad payload")
		}
		if t.Trust != nil {
			return sis.executeStartTrust(ctx, h, option, t)
		}
		return sis.executeStart(ctx, h, t)
	})
	p.OnExecute(kindRename, func(ctx context.Context, h *rubiplugin.Host, _ string, payload json.RawMessage) (any, error) {
		var t renameTicket
		if err := json.Unmarshal(payload, &t); err != nil {
			return nil, fmt.Errorf("bad payload")
		}
		return sis.executeRename(ctx, h, t, "bot", "user approved", nil)
	})
	p.OnExecute(kindAgentQuestion, func(ctx context.Context, h *rubiplugin.Host, option string, payload json.RawMessage) (any, error) {
		return answerQuestion(ctx, h, option, payload)
	})
	p.OnExecute(kindFork, func(ctx context.Context, h *rubiplugin.Host, _ string, payload json.RawMessage) (any, error) {
		var t forkTicket
		if err := json.Unmarshal(payload, &t); err != nil {
			return nil, fmt.Errorf("bad payload")
		}
		return sis.executeFork(ctx, h, t)
	})
	p.OnExecute(kindForkOwn, func(ctx context.Context, h *rubiplugin.Host, _ string, payload json.RawMessage) (any, error) {
		var t forkTicket
		if err := json.Unmarshal(payload, &t); err != nil {
			return nil, fmt.Errorf("bad payload")
		}
		return sis.executeFork(ctx, h, t)
	})
	p.OnExecute(kindMode, func(ctx context.Context, h *rubiplugin.Host, _ string, payload json.RawMessage) (any, error) {
		var t modeTicket
		if err := json.Unmarshal(payload, &t); err != nil {
			return nil, fmt.Errorf("bad payload")
		}
		return sis.executeMode(ctx, h, t, "user approved")
	})
	for kind, run := range map[string]func(context.Context, sisterHost, setupTicket) (any, error){
		kindSetupInstall: sis.executeSetupInstall, kindSetupProject: sis.executeSetupProject} {
		p.OnExecute(kind, func(ctx context.Context, h *rubiplugin.Host, _ string, payload json.RawMessage) (any, error) {
			var t setupTicket
			if err := json.Unmarshal(payload, &t); err != nil {
				return nil, fmt.Errorf("bad payload")
			}
			return run(ctx, h, t)
		})
	}
	p.OnExecute(kindRoleForeign, func(ctx context.Context, h *rubiplugin.Host, _ string, payload json.RawMessage) (any, error) {
		var t roleTicket
		if err := json.Unmarshal(payload, &t); err != nil {
			return nil, fmt.Errorf("bad payload")
		}
		return sis.executeRoleChange(ctx, h, t)
	})
	p.OnExecute(kindRoleWiden, func(ctx context.Context, h *rubiplugin.Host, _ string, payload json.RawMessage) (any, error) {
		var t roleTicket
		if err := json.Unmarshal(payload, &t); err != nil {
			return nil, fmt.Errorf("bad payload")
		}
		return sis.executeRoleChange(ctx, h, t)
	})
	p.OnExecute(kindSSHTarget, func(ctx context.Context, h *rubiplugin.Host, option string, payload json.RawMessage) (any, error) {
		var bt sshBatchTicket
		if json.Unmarshal(payload, &bt) == nil && len(bt.Targets) > 0 {
			return sis.executeSSHRegisterAll(ctx, h, option, bt)
		}
		var t sshTicket
		if err := json.Unmarshal(payload, &t); err != nil {
			return nil, fmt.Errorf("bad payload")
		}
		return sis.executeSSHRegister(ctx, h, t)
	})
	p.OnExecute(kindRoleSkillAccept, func(ctx context.Context, h *rubiplugin.Host, _ string, payload json.RawMessage) (any, error) {
		var t skillAcceptTicket
		if err := json.Unmarshal(payload, &t); err != nil {
			return nil, fmt.Errorf("bad payload")
		}
		return sis.executeSkillAccept(ctx, h, t)
	})
	p.OnPanel = func(ctx context.Context, h *rubiplugin.Host, op string, args json.RawMessage) (any, error) {
		return sis.panel(ctx, h, op, args)
	}
	rubiplugin.AddTool(p, "t3agents_sessions", "List the T3 threads of the user's agent projects on their computers, with project, state, mode and which Bot each is bound to. Use it to find the thread responsible for your project before t3agents_bind.",
		func(ctx context.Context, h *rubiplugin.Host, in sessionsIn) (any, error) {
			return sis.sessionsTool(ctx, h, in)
		})
	rubiplugin.AddTool(p, "t3agents_bind", "Ask the user (one strong approval) to bind you to one T3 thread (session_id, or the name of a named thread). After that you can message it with t3agents_send and you get its answers (t3_reply) and questions (t3_ask). Binding again replaces your binding.",
		func(ctx context.Context, h *rubiplugin.Host, in bindIn) (any, error) { return sis.bindTool(ctx, h, in) })
	rubiplugin.AddTool(p, "t3agents_start", "Ask the user (one strong approval) to start a NEW T3 thread in one of the user's agent projects (project: its id or directory), with your task as its first message, when no thread exists for your project. On approval you are bound to the new thread automatically (t3_bound event with your bind_code) and can use t3agents_send. The project must be one the user added (see t3agents_sessions, or t3agents_project_add). The thread's mode is never looser than its role allows; keep the task small and specific. At most 5 requests an hour.",
		func(ctx context.Context, h *rubiplugin.Host, in startIn) (any, error) {
			return sis.startTool(ctx, h, in)
		})
	rubiplugin.AddTool(p, "t3agents_project_add", "Ask the user (one strong approval) to add a folder on their computer as a project for T3 Agents, when the user tells you which folder the agents should work in. Pass dir, the folder's absolute path. Rubi Home refuses system folders, the home folder itself and private folders in it. Then the folder is a project: t3agents_start and t3agents_role_create can use it. At most 5 requests an hour.",
		func(ctx context.Context, h *rubiplugin.Host, in projectAddIn) (any, error) {
			return sis.projectAddTool(ctx, h, in)
		})
	rubiplugin.AddTool(p, "t3agents_rename", "Give a T3 thread a name in Rubi (an alias: T3 itself is not changed), or remove it (clear). The name shows in t3agents_sessions and t3agents_bind accepts it instead of session_id. 1 to 64 plain characters, unique on the computer ignoring case, not like an id. Without session_id or session_name it names the thread you are bound to, which needs no approval; any other thread needs a tap from the user (none inside a project the user trusts if you are bound inside it).",
		func(ctx context.Context, h *rubiplugin.Host, in renameIn) (any, error) {
			return sis.renameTool(ctx, h, in)
		})
	rubiplugin.AddTool(p, "t3agents_trust_project", "Ask the user (one strong approval) to trust the project of the T3 thread you are bound to until the next 04:00 (at most 24 hours; pass until=revoked for no end time: it then lasts until the user revokes it, with no time or count limit, and the approval says so in capitals - ask for that only when the user wants it; default until=0400). While it lasts Rubi allows the plain, listed commands of every T3 thread of the project without asking, and you can t3agents_start threads in it and t3agents_bind to its other threads without a new approval (never one that skips permission prompts; commits, merges and rebases go to the branch the thread is on, maybe main). Push and every remote operation, --force, sudo, the network, installs, tags, writes outside the project and branches outside branch_pattern (default agent/*) still ask. Needs your bind_code.",
		func(ctx context.Context, h *rubiplugin.Host, in trustIn) (any, error) {
			return sis.trustTool(ctx, h, in)
		})
	rubiplugin.AddTool(p, "t3agents_fork", "Fork a T3 thread: a branch of its conversation as a new idle thread, optionally named. A tap from the user for the thread you are bound to, one strong approval for any other. Then t3agents_bind to the fork and send it a message to start it.",
		func(ctx context.Context, h *rubiplugin.Host, in forkIn) (any, error) { return sis.forkTool(ctx, h, in) })
	rubiplugin.AddTool(p, "t3agents_mode", "Change the permission mode (plan, default, acceptEdits, bypassPermissions; strictest first) of a T3 thread. A stricter mode applies at once if you are bound in that project; a looser one asks the user (one strong approval), and never goes beyond what the thread's role allows.",
		func(ctx context.Context, h *rubiplugin.Host, in modeIn) (any, error) { return sis.modeTool(ctx, h, in) })
	rubiplugin.AddTool(p, "t3agents_unbind", "End your binding to a T3 thread.",
		func(ctx context.Context, h *rubiplugin.Host, in unbindIn) (any, error) {
			return sis.unbindTool(ctx, h, in)
		})
	rubiplugin.AddTool(p, "t3agents_send", "Send a short message (at most 4000 characters) to the T3 thread you are bound to; it starts or continues the thread's work, or waits in the queue when the project's limit is reached. Its answer comes as a t3_reply event; its delivery states come as t3_message events. For findings, answers to its t3_ask and small asks, not bulky tasks.",
		func(ctx context.Context, h *rubiplugin.Host, in sendIn) (any, error) { return sis.sendTool(ctx, h, in) })
	rubiplugin.AddTool(p, "t3agents_status", "How the T3 thread you are bound to stands (busy, idle, queued) and when a message to it would be read. Messages' own states arrive as t3_message events.",
		func(ctx context.Context, h *rubiplugin.Host, in statusIn) (any, error) {
			return sis.statusTool(ctx, h, in)
		})
	rubiplugin.AddTool(p, "t3agents_mandates", "List the 2-hour trusts (md_…) and the project trusts (pt_…, until 04:00 or until revoked, with when each was granted and how often it was used) the user gave the agents (what Rubi allows by itself), or revoke one (revoke: its id, or \"all\"). Revoking needs no approval.",
		func(_ context.Context, h *rubiplugin.Host, in mandatesIn) (any, error) {
			return sis.mandatesTool(h, in)
		})
	registerRoleTools(p, sis)
	return p
}

func main() { newPlugin().Main() }

// runApproved answers the helper for an approved command and, with the trust option, gives the session
// a mandate.
func runApproved(ctx context.Context, h sisterHost, s *sisters, option string, payload json.RawMessage) (any, error) {
	res, err := allow(ctx, h, payload)
	if err != nil || option != optionTrust {
		return res, err
	}
	var t ticket
	_ = json.Unmarshal(payload, &t)
	return sessionTrust(h, s, t), nil
}

// sessionTrust gives the session a 2-hour mandate for an approval that chose the trust option.
func sessionTrust(h sisterHost, s *sisters, t ticket) map[string]any {
	if t.SessionID == "" || !within(t.Cwd, t.Cwd) {
		return map[string]any{"allowed": true, "trusted": false, "why": "the request had no session or directory"}
	}
	now := s.now().UTC()
	m := mandate{ID: newID("md_"), Device: t.Device, DeviceName: t.DeviceName, SessionID: t.SessionID, Cwd: t.Cwd,
		Issued: now, Expires: now.Add(mandateTTL), MaxUses: mandateMaxUses, Project: t.AgentProject}
	if err := s.st.issue(m, now); err != nil {
		h.Logf("saving a mandate: %v", err)
		return map[string]any{"allowed": true, "trusted": false, "why": "couldn't save it"}
	}
	h.Audit("mandate_issued", map[string]any{"mandate": m.ID, "device": m.Device, "session": m.SessionID, "directory": m.Cwd, "expires": m.Expires})
	return map[string]any{"allowed": true, "trusted": true, "mandate": m.ID, "expires": m.Expires}
}

// allow answers Rubi Home once you approved: the agent runs the command its question named.
func allow(ctx context.Context, h homeCaller, payload json.RawMessage) (any, error) {
	var t ticket
	if err := json.Unmarshal(payload, &t); err != nil || t.AskID == "" {
		return nil, fmt.Errorf("bad payload")
	}
	if err := h.HomeCall(ctx, t.Device, opAgentDecide, agentDecision{AskID: t.AskID, Behavior: "allow"}, nil); err != nil {
		return nil, fmt.Errorf("the agent is no longer waiting for this (%v)", err)
	}
	return map[string]any{"allowed": true}, nil
}
