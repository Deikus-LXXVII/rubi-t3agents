# Batch approvals (use `Request.Items`)

Branch `citri/t3agents-batch`. Core's gap list (rule 5, `docs/project-rules.md` of the core repo) says none of this
plugin's flows is a batch. SDK `github.com/Deikus-LXXVII/rubi v0.14.0-rc.2`.

## SDK finding
- The plugin asks with `Host.Submit` (`approval.submit`) and follows with `Host.WaitApproval` (`approval.wait`).
- v0.14.0-rc.2 supports batches: `Request.Items []Item{Key, Label, Preview}`; the executor gets the option
  `items:<key>,<key>` (one option on the request) or `<option>|items:<key>,...` (several options), and
  `ChosenItems` reads only the first form (this plugin parses both: `splitBatchOption`).
- Not in the SDK: `Item.DefaultOff` (core internal), so every item starts ticked, and an option key alone (a chat
  button) ticks all of them. No `approval.cancel` for plugins either, so a plugin cannot cancel a pending approval and
  move its items into a new one (core's botBundle can); P1 uses a short collection window instead.
- An unticked item is not reported to the executor, so the executor must deny/skip every item that is not named.

## Status
- P1 agent_perm.go tool permission asks: DONE. `agent_batch.go`: asks of one (computer, project, role, session) that
  arrive within `askBatchWindow` (500 ms, set in `Start`; 0 in tests) are one `t3agents.run_batch` approval, an item
  per ask carrying the single approval's preview; ticked items are allowed, all others denied, a denied, cancelled,
  expired or failed-to-submit approval denies all. 2-hour trust is offered only if no item is an always-ask and all
  share the directory. Max 10 per batch; label shows the whole command. One ask alone is unchanged. Not done: joining a still-pending approval.
- P2 roles_change.go change + skill acceptance: NOT DONE, stopped. A flagged (held) skill exists only after the role
  version is written (Rubi Home's scan runs in `role.put` and sets state "held"); `skillAcceptTicket` is checked against
  that held skill's SHA and rules, and accepting is a panel-only action. At the time a change is asked, there is no
  skill acceptance to put beside it, and putting `Accepted` rules into the same `role.put` is not something Rubi
  Home is known to honour. Needs a Rubi Home contract (accepted rules on put, checked against its own scan) first.
  Possible later, separate: several held skills of one role accepted in one approval.
- P3 roles_ssh.go several targets: DONE. Panel op `ssh_confirm_all` ("Check all host keys", shown with two or more
  drafts): scans each, one `t3agents.ssh_target` approval, an item per target with the single approval's preview
  (fingerprint included); nothing is asked if one server can't be read; only ticked targets are registered.
- P4 start + trust project: DONE. `t3agents_start` / `t3agents_role_start` take `trust_project`: one `t3agents.start`
  approval with items `start` and `trust` (trust until the next 04:00 only, never "until revoked"). Start already
  includes the bind. Each ticked item is done on its own; an option without items does neither.
- Not touched: agent_fork.go, agent_mode.go, sessionname.go (renames and mode changes as items), project_add.go,
  agent_setup.go. They are separate Bot or panel calls that do not follow each other in one flow.

## Review fixes (status)
1. Trust item ticked by default: DONE. Summary names both ("... and trust the project X until <time>"); the preview's
   first key `also_trusts_the_project` carries the warning; the grant is audited (`project_trust_issued`) as the single trust is.
2. Long or hidden commands never batched: DONE. `batchable`: at most 100 runes, tool at most 40, no control, format or bidi
   character; anything else is its own approval.
3. Failed execute denies everything: DONE (`request.denyFailed`, "the approval failed").
4. Trust limit or offer error: DONE. The approval is the start alone with `trust_note` in the preview; the start counters
   are used once (the fallback is in place, the tool is not called again).
5. Trust needs the start: DONE. Trust only is an error that says why; a failed start grants no trust.
6. Batch preview has `directory`; the trust option reads "Allow the ticked commands, and trust this directory's plain
   listed commands for 2 hours": DONE.
7. SSH register-all: DONE. I found no execute limit in core to measure against (the context comes from the panel
   request), so I capped the batch at 4 targets and run the registrations side by side: worst case is about one
   registration's 60 s, not 8 x 60 s. A draft is deleted after registering only if it still equals what was approved
   (this also fixes the single path).
8. `kindRunBatch` moved above the read-only comment. `batchAsk.Digest` removed: Rubi Home has no operation to read the
   ask it still holds, so there was nothing to verify it against.
9. Tests for the cap (10; 11 with the 11th in a new group), an ask after submit, a context cancel in the window (the
   group is removed and nothing is asked; Rubi Home denies what nobody answers): DONE.
