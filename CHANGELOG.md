# Changelog

## Unreleased

- The setup page shows a computer's memory protection where Rubi Home checks it (on Linux): whether project keys can be unlocked there, whether that rests on your accepted risk, and why not. It is a note, not a setup step.

## v0.2.0

- The setup page uses Rubi Home's step titles: Claude sign-in, T3 installed, pinned and running, and T3 paired with Rubi Home.
- A setup action drops the cached status. The page redraws from a fresh read and keeps refreshing until the status changes, so Sign in and Set up everything update without a manual refresh.
- A failed Claude sign-in offers "Get a new sign-in link".
- The running box no longer says the buttons below are waiting when Cancel is on that box.
