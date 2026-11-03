# T3 Agents for Rubi

A [Rubi](https://github.com/Deikus-LXXVII/rubi) plugin. Your Bots start and steer agent threads that run in
T3 Code on your computer, each in a role VM, and you stay in charge: every
step that widens what an agent can do is an approval in the Rubi panel, with your passkey.

How it works: Rubi Home on your computer runs T3 Code and the role VMs and offers them to this plugin as two
capabilities. The plugin keeps what is Rubi's to decide (approvals, role changes, trusts, bindings, the pages);
Rubi Home does the work on the computer.

- **`t3-agent`**: projects, threads (start, send, messages, status, close, interrupt, fork, mode, rename), the
  queue of questions the agents ask, roles, SSH targets and the usage limits.
- **`t3-setup`**: the setup page's steps (container tool, runner image, T3 installed and pinned, T3 paired,
  each role's provider sign-in, a self-test).

The plugin has no network access, no files and no secrets of its own.

## Concepts

- **Roles.** A role is a named profile on one project (its own skills, subagents, built-in tools, MCP servers,
  Claude plugins, default permission mode and SSH grants) that runs in its own VM. Every change is a new
  immutable version you can revert. A Bot may make small changes itself (tier 1, audited); anything that widens a
  role (MCP servers, plugins, network hosts, a tool outside the safe set, a looser mode, an SSH target that allows
  any command) is a strong approval (tier 2) that names the exact difference.
- **Modes.** `plan`, `default`, `acceptEdits`, `bypassPermissions`, strictest first. A stricter mode applies at
  once; a looser one is a strong approval, and no mode goes beyond what the role (and the bridge) allows.
- **Real paths.** The role VMs mount a project at its real path on the computer. Mandates, project trusts and the
  checks on a question's directory and file paths are keyed on that path as `t3-agent.projects` reports it (spaces
  are fine), and a question is matched by its `cwd` being under it. A project Home doesn't list, or lists with a path
  that is not a clean absolute one, makes every question ask.
- **Mandates.** "Allow and trust this thread for 2 hours" on a command approval lets Rubi allow that thread's
  plain, listed commands by itself (at most 300, each audited). Every option of a listed program is checked
  against a list; push, `--force`, `sudo`, network commands, installs, credential paths and writes outside the
  project always ask.
- **Project trust.** A Bot bound to a thread can ask (one strong approval) to trust its whole project until the
  next 04:00 (at most 24 hours), or until you revoke it (no end time, said in capitals in the approval). While it
  lasts, plain listed commands of every thread of that project are allowed and the Bot may start and bind threads
  in it without a new approval, never one that skips permission prompts. Commits go to the branch a thread is on;
  new branches only match `agent/*`. Revoke in the panel or with `t3agents_mandates`.
- **Skill library.** Shared skills admitted by the library's curator can be attached to roles, following the
  latest admitted version or pinned to one. Withdrawn versions are dropped at the next check.
- **SSH.** Rubi Home holds the SSH targets, the keys and the pinned host keys; the VMs never see them. You
  register a target with a strong approval that carries the host key fingerprint you confirmed; Bots can grant a
  registered target to a role. A command on a target registered "ask each time" is an approval of its own.
- **Briefs and reports.** `t3agents_start` and `t3agents_role_start` take an optional typed `brief`: `goal` (required), `scope`,
  `acceptance[]`, `verify[]`, `timebox`, `forbidden[]` and `report` (what the report should cover). Rubi checks the
  lengths (goal and scope 600 characters, an item 300, at most 8 items a list) and refuses control and invisible
  characters, then renders it the same way every time under a `## BRIEF br_xxxxxxxx` heading after the task, with
  fixed standing orders (stay in scope, never push to a protected branch, end with the report below). The whole first
  message is shown in the approval. The thread is bound with that `brief_id`; passing it to `t3agents_send` appends the
  standing orders to the message, so a resumed thread gets them again. The agent is asked to end with
  `STATUS: PASS|ISSUES|BLOCKED`, `COMMIT: <sha or none>` and one or more `EVIDENCE: ...` lines. When a thread's
  final text (`t3_reply`) ends in a well-formed report, the event also carries `status`, `commit` and `evidence`
  (untrusted text); a malformed or cut report leaves the raw `text` only.
- **Approvals.** Tool questions of a thread (commands, edits, a git credential, a host outside the allow-list, an
  SSH command) reach you as approvals; Rubi Home answers the thread only after your approval. A tap approves a
  read-only command (`ls`, `cat`, `git status`); everything else is a strong approval you can't lower.
  Anything that isn't an approval (deny, cancel, expiry, Rubi locked) is a deny with a reason the agent sees.

## Permissions

- **Rubi Home: `t3-agent`** and **Rubi Home: `t3-setup`**, on your paired computers. Nothing else.
- Actions (all under the plugin id `t3agents`): `run` (any command; strong, can't be lowered), `run_safe` (tap),
  `bind`, `trust_project`, `rename`, `start`, `fork`, `fork_own`, `mode`, `agent_question`, `role_foreign`,
  `role_widen`, `role_skill_accept`, `ssh_target`, `setup.install` and `setup.project`.

## Bot tools

| Tool | What |
|---|---|
| `t3agents_sessions` | The threads of your agent projects, with state, mode, name and which Bot each is bound to |
| `t3agents_bind` / `t3agents_unbind` | Bind a Bot to one thread (one strong approval), or end the binding |
| `t3agents_start` | Start a thread in a project with a task and an optional `brief` (one strong approval, or none inside a trusted project; at most 5 an hour) |
| `t3agents_send` / `t3agents_status` | Message the bound thread (at most 4000 characters, 30 an hour; `brief_id` repeats the standing orders) and see how it stands |
| `t3agents_fork` / `t3agents_mode` / `t3agents_rename` | Fork a thread, change its mode, give it a name |
| `t3agents_project_add` | Ask the user to add a folder as a project (one strong approval) |
| `t3agents_trust_project` / `t3agents_mandates` | Ask for a project trust; list and revoke trusts |
| `t3agents_role_create`, `_update`, `_archive`, `_unarchive`, `_skills_set`, `_skills_add`, `_skills_remove`, `_tools_set`, `_agents_set`, `_mcp_set`, `_plugins_set`, `_hosts_set`, `_ssh_grant`, `_ssh_revoke`, `_start`, `_list`, `_get`, `_history` | Roles |

The Bot is always the one Rubi verified (`rubi_agent` with the `agent_code` of its own webhook), never a name it
typed. A binding gives the Bot a bind code (sent to that Bot alone, in a `t3_bound` event); `t3agents_send`,
`t3agents_status`, `t3agents_unbind` and `t3agents_trust_project` need it. A refusal says which of "no binding", "unknown code",
"superseded code" and "another Bot's code" it is. Bindings and codes are kept across restarts of the plugin.
What a thread says comes back to its Bot as `t3_reply` (answers), `t3_ask` (questions) and
`t3_message` (delivery states) events.

## Rubi Home operations used

`t3-agent.`: `projects`, `sessions`, `start`, `send`, `messages`, `next`, `decide`, `status`, `close`, `interrupt`,
`fork`, `mode`, `rename`, `limits`, `usage_mode`, `role.list`, `role.get`, `role.history`, `role.diff`,
`role.put`, `ssh.scan`, `ssh.register`, `ssh.list`, `ssh.remove`, `ssh.audit`.
`t3-setup.`: `status`, `run`, `login.open`, `login.code`, `project.add`, `project.remove`, `cancel`.
And the host method `library.get`.

## The setup page

One page per computer, opened from the plugin's first page, with buttons only. It shows the steps Rubi Home
lists in `t3-setup.status`, in order, and the one button Home names for each:

1. **Apple container**: install (a strong approval first; macOS shows its own administrator dialog).
2. **Container service**: start.
3. **Runner image**: build or update.
4. **T3 installed and pinned**: install or update to the pinned version.
5. **T3 paired with Rubi**: pair.
6. **Provider sign-in per role**: each role has its own row. Sign-in relays Claude's `claude auth login`:
   open the link (shown as text, opened by a button), sign in, paste the code into the box that appears with the
   link. The code goes to Rubi Home and is never kept or audited.
7. **Self-test**, per project.

The project folders are listed apart, with Remove; a Bot adds one when you ask it (`t3agents_project_add`). The page
refreshes every 3 seconds while a step runs or a sign-in waits, and not otherwise.

## Limits

- The boundary is your macOS user account and the role VMs.
- Titles, names and everything an agent or a thread says are untrusted text: clipped, shown as text only.
- `git diff` and `git log` honour the repository's configuration; an approval for a read-only command still shows
  the exact command and directory.

## Development

```sh
go build -o t3agents . && go vet ./... && go test -race ./...
./t3agents --manifest
```

The plugin builds on the Rubi SDK (`github.com/Deikus-LXXVII/rubi/sdk/rubiplugin`) and needs a Rubi that knows the
`t3-agent` and `t3-setup` capabilities of Rubi Home. `PublisherKey` is empty in the source; the maintainer's
release build sets it with `-ldflags "-X main.PublisherKey=..."`.

## License

[MIT](LICENSE)
