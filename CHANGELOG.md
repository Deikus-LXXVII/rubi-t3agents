## Unreleased — native sandbox setup

- Native Home setup (`backend: host-sandbox`) offers Set up everything without installing the retired container tool; older Home retains its approval gate.
- Setup and plugin descriptions explain native sandboxes and explicit provider credentials rather than claiming VM isolation.

# Changelog

## Unreleased

- One-confirmation native repository-task start/status/append/stop MCP tools and phone task cards, guarded by matching supervised Home capabilities. Linux-only repository tasks fail closed on unsupported/mixed installs.
- Original-requester-only task results, durable Home delivery acknowledgements and automatic explicit-PASS finalization/publication retry, with stable at-least-once event IDs. Cards render agent text safely and label evidence as agent-reported.
- Optional masked fine-grained GitHub publication account setup; only Core resolves the vault token at use time. No token goes into task/provider state. Existing standalone session actions remain.

- The setup page shows a computer's memory protection where Rubi Home checks it (on Linux): whether project keys can be unlocked there, whether that rests on your accepted risk, and why not. It is a note, not a setup step.

## v0.2.0

- The setup page uses Rubi Home's step titles: Claude sign-in, T3 installed, pinned and running, and T3 paired with Rubi Home.
- A setup action drops the cached status. The page redraws from a fresh read and keeps refreshing until the status changes, so Sign in and Set up everything update without a manual refresh.
- A failed Claude sign-in offers "Get a new sign-in link".
- The running box no longer says the buttons below are waiting when Cancel is on that box.
