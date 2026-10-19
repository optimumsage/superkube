# Changelog

## v1.2.0 — 2026-10-16

A hardening release. Several guardrails could be bypassed or silently applied
to the wrong cluster; those are fixed, and a few behaviors are now stricter.
Read **Upgrade notes** before rolling this out.

### Upgrade notes (behavior changes)

- **A broken `config.yaml` now fails closed.** Malformed YAML or an unknown key
  (e.g. a `forbids:` typo) prints a warning on every run, and mutating commands
  refuse until the file is fixed or moved. Read-only commands keep working.
  Previously the whole policy was silently dropped.
- **`forbid` rules apply to every verb**, including passthrough verbs (`patch`,
  `replace`, `edit`, `label`, `create`, `exec`, krew plugins, …), and match
  aliases, plurals, `kind/name`, group-qualified types and `flag=value` pairs
  (`delete namespace` now also blocks `delete ns foo`).
- **Command flags must follow the verb.** `sk -l app=web delete pods` is refused
  with guidance; write `sk delete pods -l app=web`. kubectl global flags
  (`--context`, `-n`, `--as`, …) and sk's own flags can still go first.
- **`--dry-run` values need `=`** (`--dry-run=server`), matching kubectl; a bare
  `--dry-run` means `client`.
- `rollout restart|pause|resume` now ask for confirmation; `delete -f/-k/-l`
  asks you to type `DELETE`; declined prompts exit `130`.
- `sk upgrade` refuses releases without a verifiable checksum unless you pass
  `--skip-checksum` (unsafe).
- `sk web`: the browser can no longer skip confirmations; only `sk web --yes`
  can, and never for `delete --all` or forbidden operations. Saving
  `config.yaml` from the browser requires typing `EDIT CONFIG`. The token is no
  longer returned by `/api/v1/info`.

### Security and guardrail fixes

- Policy, banner and audit now resolve against the context the command really
  runs in, even when `--context`/`--kubeconfig`/`-n` come after the verb. When
  argv is ambiguous, policy is the union of every candidate context.
- kubectl global value flags (`--as`, `--token`, `--server`, …) no longer
  misroute guarded commands to unguarded passthrough or leak into the audit
  log as the verb.
- `apply`, `scale`, `rollout` and `cordon` now check `forbid`.
- `require_typed_confirm`, `ai.provider`, `ai.timeout` and `audit.enabled` in
  `config.yaml` are now honored (they were parsed but ignored).
- `sk web`: views, policy and audit follow a UI context switch; confirmation
  tokens are bound to the exact previewed operation, context and namespace;
  argv injection via names, ports and namespaces is rejected; request size
  limits and server timeouts; constant-time token check, HttpOnly cookie,
  server-issued CSRF tokens only; `kind: List` and multi-document manifests are
  policy-checked per object; the command palette is read-only and
  policy-checked; `--bind 0.0.0.0` works (token required).
- `sk secret view` masks values structurally, including multi-object lists,
  `stringData` and the `last-applied-configuration` annotation; the web UI uses
  the same masking.
- Audit log redaction covers space-separated and additional secret flags
  (`--token X`, `--from-literal k=v`, `--password`, `--docker-password`).
- AI prompt redaction covers PEM private keys, URLs with passwords, JSON and
  YAML secrets (including block scalars), AWS keys, `export X_TOKEN=…`.
- Claude `--tools` mode can no longer read Secrets (any casing) or
  `kubectl config view`.
- Toolchain go1.26.9 and `golang.org/x/net` v0.61.0 clear all `govulncheck`
  findings (38 reachable stdlib vulnerabilities in the v1.1.2 build).

### Fixes

- `sk apply -f -` applied nothing (the diff consumed stdin); stdin is now
  replayed to both diff and apply. `--dry-run=none|server|client` work.
- `sk tui` describe/YAML/events/diagnose and `sk diagnose`/`why` honor
  `--context`/`--kubeconfig`.
- TUI: watch errors and a loading state show in the status bar instead of
  painting over the screen; the log filter no longer crashes on some Unicode
  text; stale messages from a closed pane no longer leak into the next one.
- `sk get -w`: renders are serialized (no garbled frames), the watch recovers
  from expiry instead of exiting 0, a name / `--field-selector` / `-o wide` are
  honored, unsupported flags fall back to kubectl, rows are truncated to the
  terminal width.
- `sk logs --multi`: picks the default container (multi-container pods work),
  supports `-c`/`--all-containers`, follows new pods with `-f`, surfaces stream
  and permission errors.
- `sk delete` handles `-f`, `-k`, `-l`, attached short flags and comma targets;
  `--context x` is no longer counted as a resource name.
- `sk pf`: concurrent `pf start` no longer loses entries; `pf stop` verifies
  process identity before signalling a possibly reused PID.
- Antigravity: prompts go over stdin (no argv size limit, not visible in
  `ps`), and write/provider errors are reported.
- Audit rotation no longer deletes the log if the archive failed to write.

### Maintenance

- CI restored: build, vet and race tests on Linux and macOS, golangci-lint,
  govulncheck. Releases build with the toolchain pinned in `go.mod`.
- Dependencies: k8s.io/* v0.37.1, bubbles v1.0.0, go-isatty v0.0.24.
- Removed dead code and deprecated API usage; `make vuln` replaces the
  non-functional `make e2e`.

### Known limitations

- sk emulates kubectl's flag parsing to find the verb, context and namespace.
  Unusual argv that sk can't interpret confidently is refused (fail-closed)
  rather than guessed; full parity with kubectl's own parser is planned.
