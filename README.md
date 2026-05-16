<p align="center">
  <img src="assets/logo.svg" alt="superkube" width="160" height="160"/>
</p>

# superkube

A safer, prettier, AI-assisted wrapper around `kubectl`.

`superkube` (binary: `superkube`, alias: `sk`) is **not** a replacement for `kubectl` — it wraps the `kubectl` you already have installed and adds:

- **Guardrails** — typed confirmation for destructive operations, dry-run-by-default for writes with a colored diff preview, and a local audit log of every command you've run.
- **On-demand AI** — `sk diagnose pod/foo`, `sk why pod/bar`, `sk logs --ai`, `sk ai explain "…"`. Uses your **local** `claude` or `gemini` CLI under your account; no calls anywhere else.
- **Live UI** — refreshing tables for `sk get -w`, a full-screen TUI (`sk tui`), colored multi-pod log tails, and per-context safety banners.
- **Convenience** — fast context and namespace switching (`sk ctx`, `sk ns`) with a fuzzy picker.
- **Everything else still works** — unknown verbs (including [`krew`](https://krew.sigs.k8s.io/) plugins) pass through to `kubectl` verbatim. Your kubeconfig, contexts, plugins, and aliases all keep working.

Apache-2.0 licensed. macOS + Linux (amd64 / arm64).

---

## Install

### curl-pipe (recommended)

```sh
curl -fsSL https://raw.githubusercontent.com/optimumsage/superkube/main/scripts/install.sh | sh
```

Installs to `~/.local/bin` by default. Set `PREFIX=/usr/local/bin` to choose a different directory, or `VERSION=v0.3.0` to pin a release. Checksums are verified against the release's `checksums.txt` when present.

### From source

Requires Go 1.24+.

```sh
git clone https://github.com/optimumsage/superkube.git
cd superkube
make install   # builds superkube + sk symlink into $GOBIN
```

### Upgrade

```sh
sk upgrade                  # check latest release, confirm, install in place
sk upgrade --check          # just report whether an upgrade is available
sk upgrade --version v0.3.0 # pin a specific release
sk upgrade --yes            # skip the confirmation prompt
sk upgrade --force          # reinstall even if already up to date
```

`sk upgrade` resolves the latest release from GitHub, downloads the matching `darwin`/`linux` × `amd64`/`arm64` tarball, verifies the published `checksums.txt` when present, and atomically replaces the running binary in place (resolving any `sk → superkube` symlink). The `--yes` and `--plain` global flags apply: non-TTY callers must pass `--yes`. If the install directory isn't writable you'll get a "permission denied" error pointing you at `sudo` or a fresh `install.sh` run.

### Requirements

| Tool | Version | Required? |
|---|---|---|
| `kubectl` | v1.18+ | yes (server-side dry-run depends on it) |
| `claude` | any recent | optional — for AI commands |
| `gemini` | any recent | optional — for AI commands (fallback) |

Run `sk version` to confirm everything's wired up.

---

## Quickstart

```sh
sk version                            # binary, kubectl, AI provider, all in one place
sk ctx                                # fuzzy-pick a context
sk ctx clean                          # tick contexts to remove (manual)
sk ctx clean --auto                   # probe each context, drop unreachable ones
sk ns kube-system                     # switch namespace
sk get pods                           # styled header on a TTY, plain in a pipe
sk get pods -w                        # live-refreshing table
sk apply -f deployment.yaml           # diff preview + confirm before applying
sk delete pod foo                     # typed-name confirmation
sk diagnose pod/broken                # describe + events + logs → AI explains
sk why pod/pending-pod                # focused AI diagnosis for stuck workloads
sk logs deploy/web --ai               # AI summary of errors in the log tail
sk logs --multi=deploy/web -f         # live tail across all pods, colored prefix
sk tui                                # full-screen pod browser
```

---

## Features

### Guardrails

By default, `superkube` will:

- Ask you to **type the resource name** before `sk delete <kind> <name>`. Wrong name aborts.
- Hard-block `sk delete --all` unless you pass `--yes` **and** type the literal phrase `DELETE`.
- Run a server-side `kubectl diff` before `sk apply`, render a unified colored diff, and ask for confirmation.
- Confirm `sk scale --replicas=0`, `sk rollout undo`, `sk drain`, and `sk cordon`.

You can bypass any prompt with `--yes`. In non-TTY environments (CI pipelines, redirected stdin), destructive commands **refuse** to run without `--yes` — never silently auto-confirm.

#### Per-context policy

Drop a `~/.config/superkube/config.yaml` to mark certain kubectl contexts as dangerous. Glob patterns match anywhere in the context name:

```yaml
contexts:
  "prod-*":
    forbid:
      - "delete --all"
      - "drain"
    banner: "PRODUCTION — be careful"
  "arn:*:cluster/prod-*":
    forbid: ["delete --all"]
```

When a command runs against a matching context:

- The colored banner is printed to stderr before any other output.
- Operations listed in `forbid` are **refused unconditionally** (even with `--yes`). The policy is the override, not the flag — you must edit the config to proceed.

Run `sk config init` to write a starter file with comments.

### AI assistance

AI commands run your **local** `claude` or `gemini` CLI under your account. No data is sent to any other service. Auto-detection prefers `claude`, falls back to `gemini`. Override with `--ai claude|gemini` or `SUPERKUBE_AI=…`.

| Command | When to use |
|---|---|
| `sk ai explain "<question>"` | Free-form question with current context/namespace as light context. |
| `sk diagnose pod/<name>` | Open-ended investigation. Gathers describe + events + last 200 log lines + the workload's owner chain + sibling pods, then asks for a summary, root cause, and next steps. |
| `sk why pod/<name>` | Tighter prompt that enumerates Pending / CrashLoopBackOff / ImagePullBackOff / OOMKilled / probe-failure causes and asks the model to pick one with cited evidence. |
| `sk logs <pod> --ai` | Summarize errors in the log tail (default `--tail=200`). Incompatible with `-f`. |

**Privacy note.** Before sending any prompt, `superkube` redacts:

- `Secret.data.*` values
- environment variables whose names match `(?i)(token|key|secret|password|credential|auth)`
- JWT-shaped strings (`eyJ…` with three base64 segments)
- `Bearer` / `Basic` auth header values
- ServiceAccount tokens, image-pull secrets, TLS cert/key data

Redaction is **best-effort, not security**. If your prompt contains free-form pasted output, review before sending. Use `--no-context` to send the literal prompt with no cluster data attached.

### Live table view (`sk get -w`)

On a TTY with a default/wide table format, `sk get <res> -w` opens a client-go watch and redraws the table in-place on every change. Honors `-n`, `-A`, and `-l`/`--selector`. Works with built-in kinds and CRDs (resolved via the discovery API).

Anything that needs scripting compatibility — `-o json|yaml|name|jsonpath`, piped output, redirected stdout — still passes through to `kubectl` verbatim.

### Full-screen TUI (`sk tui`)

A bubbletea-powered Pods browser, live-updating via a client-go informer.

| Key | Action |
|---|---|
| `j` / `k` (or arrows) | move cursor |
| `g` / `G` | jump to top / bottom |
| `/` | filter pods by name (esc clears) |
| `enter` | open action menu for the selected pod |
| `a` / `l` / `d` (in menu) | describe / logs `-f` / diagnose |
| `?` | toggle help overlay |
| `q` or `ctrl+c` | quit |

Actions suspend the TUI, shell back into the matching `sk` subcommand (`sk describe`, `sk logs`, `sk diagnose`) so you get the same redaction, AI provider, and audit-log entry as if you'd typed the command directly. The TUI scope follows `-n` / `--namespace`; omit for all namespaces.

### Multi-pod log tail

`sk logs --multi=<target>` streams logs from many pods at once. The target can be:

- A workload reference: `deploy/web`, `statefulset/db`, `daemonset/log-shipper`, `replicaset/web-abc123`. The workload's selector is resolved via client-go.
- A raw label selector: `app=web`, `app=web,tier=frontend`.

Each line is prefixed with `[<pod-name>]` and colored consistently per pod (hash-based, so the same pod gets the same color across invocations). `-f` is supported. `Ctrl-C` tears down all streams cleanly.

### Context & namespace switching

```sh
sk ctx                  # fuzzy-pick a context
sk ctx my-cluster       # switch directly
sk ctx -                # previous context
sk ns kube-system       # switch namespace in the current context
sk ns -                 # previous namespace
```

### Cleaning up stale contexts (`sk ctx clean`)

Kubeconfigs accumulate cruft — old clusters that have been deleted, ephemeral kind/minikube contexts, demos you cloned and never went back to. `sk ctx clean` prunes them safely.

```sh
sk ctx clean                         # manual: huh multi-select picker
sk ctx clean --auto                  # probe every context, queue the dead ones
sk ctx clean --auto --preview        # show what would be removed without touching kubeconfig
sk ctx clean --auto --timeout 2s --concurrency 16
sk ctx clean --keep-orphans          # leave cluster/user entries behind
```

- **Manual mode** (default) opens a multi-select picker over every context in the merged kubeconfig. Tick the ones you want gone, hit enter; the rest are untouched.
- **Auto mode** issues one `/version` request per context (in parallel, up to `--concurrency`). Anything that fails — DNS error, TLS failure, connection refused, timeout — is queued for removal. The current context is shown in the probe table but never auto-pruned; you have to remove it through manual mode if you really want to.
- The planned removal list is always printed and confirmed before kubeconfig is rewritten. `--yes` skips the final confirmation; `--preview` shows the list and exits without writing.
- By default, cluster and auth-info entries that become unreferenced after the deletes are also pruned. Pass `--keep-orphans` to leave them.

### Audit log

Every executed command is appended as JSON Lines to `${XDG_STATE_HOME:-$HOME/.local/state}/superkube/audit.log` (mode `0600`). Each entry includes the verb, full argv, kubectl context, namespace, exit code, duration, and the AI provider used for AI commands. The file rotates at 10 MB.

```sh
sk audit log                  # tail recent entries
sk audit log --since 1h       # filter by time
sk audit log -f               # follow new entries
sk audit path                 # print the file path
```

Secret-shaped values in argv (`--from-literal=KEY=VALUE`) are redacted before being written.

### Krew plugin compatibility

Every plugin installed via [krew](https://krew.sigs.k8s.io/) is reachable as `sk <plugin>` with no extra wiring — superkube forwards unknown verbs to `kubectl` verbatim, and krew's plugins are kubectl subcommands.

```sh
kubectl krew install neat
sk get deployment web -o yaml | sk neat

kubectl krew install tree
sk tree deployment web
```

See [`docs/krew.md`](docs/krew.md) for more examples and caveats.

---

## Command reference

| Command | Behavior | Guardrails |
|---|---|---|
| `sk get <res>` | shells out; colored header on TTY; live table for `-w` on TTY; verbatim passthrough for `-o json/yaml/name/jsonpath`, pipes, redirects | none |
| `sk describe <res>` | verbatim passthrough | none |
| `sk apply -f …` | server-side dry-run → colored diff → confirm → apply | dry-run + confirm |
| `sk delete <res>` | typed-name confirm; `--all` requires `--yes` + typed `DELETE` phrase | typed |
| `sk patch` / `replace` / `create` | passthrough | passthrough |
| `sk logs <pod>` | passthrough; `--ai` summarizes the tail; `--multi=<target>` streams many pods with colored prefix | none |
| `sk exec` / `sk port-forward` | passthrough; TTY + signals propagated; clean teardown on Ctrl-C | passthrough |
| `sk rollout undo` | typed confirm | confirm |
| `sk scale --replicas=0` | typed confirm when scaling to 0 | confirm |
| `sk drain <node>` | typed-name confirm | typed |
| `sk cordon` | yes/no confirm | confirm |
| `sk top`, `sk edit` | passthrough | none |
| `sk <anything-else>` | verbatim passthrough — krew plugins keep working | none |
| `sk ctx [name\|-]` | list / switch / previous context (huh fuzzy picker on TTY) | n/a |
| `sk ctx clean [--auto] [--preview]` | remove stale contexts: manual picker, or auto-probe `/version` and prune the unreachable ones | confirm |
| `sk ns [name\|-]` | list / switch / previous namespace | n/a |
| `sk tui` | full-screen pod browser with describe/logs/diagnose actions | n/a |
| `sk ai explain "<q>"` | free-form AI question with current ctx/ns as light context | n/a |
| `sk diagnose pod/<name>` | describe + events + logs + owner chain + sibling pods → AI explains | n/a |
| `sk why pod/<name>` | tighter AI prompt for Pending / CrashLoop / ImagePull / OOM / probe failures | n/a |
| `sk audit log [--since 1h] [-f]` / `audit path` | view or locate the audit log | n/a |
| `sk config init [--force]` / `config path` | manage `~/.config/superkube/config.yaml` | n/a |
| `sk version` | binary, Go, platform, kubectl, AI provider versions | n/a |
| `sk upgrade [--check\|--force\|--version v…]` | self-upgrade to the latest GitHub release; verifies checksums, atomic in-place replace | confirm |
| `sk completion bash\|zsh\|fish` | shell completion (cobra built-in) | n/a |

---

## Configuration

### Files

| Path | Purpose |
|---|---|
| `~/.config/superkube/config.yaml` | per-context policy (forbid, banner). Run `sk config init` to scaffold. |
| `${XDG_STATE_HOME:-~/.local/state}/superkube/audit.log` | append-only JSONL audit log. |
| `~/.kube/config` (and `$KUBECONFIG`) | inherited from kubectl, untouched. |

### Environment variables

| Variable | Effect |
|---|---|
| `SUPERKUBE_AI` | force a specific AI provider (`claude` or `gemini`). |
| `NO_COLOR` | disable all colored output (equivalent to `--plain`). |
| `XDG_CONFIG_HOME` / `XDG_STATE_HOME` | override the config and state directories. |
| `KUBECONFIG` | standard kubectl override; superkube respects it. |

### Global flags

```text
--context <name>       kubectl context to use
-n, --namespace <ns>   namespace to use
--kubeconfig <path>    path to kubeconfig
--ai claude|gemini     force AI provider (overrides auto-detect)
--yes                  skip confirmation prompts (never bypasses policy forbids)
--dry-run auto|server|client|none
--plain                disable color/TUI output
--audit on|off         audit logging
--no-context           AI: send only the literal prompt, no cluster data
-v, --verbose
```

---

## Troubleshooting

### `sk: command not found` after install

The installer drops binaries in `~/.local/bin`. If that's not on your `PATH`, add it to your shell rc:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

The installer prints a hint when this is the case.

### "AI provider not found"

`sk diagnose` / `sk why` / `sk logs --ai` need either `claude` or `gemini` on your `PATH`. Install one:

- [Claude Code](https://docs.claude.com/en/docs/claude-code) — `npm i -g @anthropic-ai/claude-code`
- [Gemini CLI](https://github.com/google-gemini/gemini-cli)

Then run `sk version` to confirm it's detected.

### "tui requires an interactive terminal"

`sk tui` won't run when stdin or stdout is redirected/piped — there's no way to display a full-screen UI in a non-TTY context. Run it directly from your terminal.

### `sk get -w` shows a static table, not refreshing

The live refresh only kicks in on a TTY with a default/wide table format. If you piped output (`sk get pods -w | tee`), redirected (`> file`), or asked for a non-table format (`-o json`, `-o yaml`, etc.), superkube falls through to `kubectl -w` so streaming consumers still get clean line-oriented output. This is intentional.

### "context matches `<pattern>`" banner I don't want

A glob in `~/.config/superkube/config.yaml` is matching your current context. Edit the file or run `sk config path` to find it. To temporarily silence: switch context with `sk ctx <name>`.

### `sk delete --all` is blocked even with `--yes`

That's a `forbid:` entry in your config — by design, policy can't be bypassed with `--yes`. Remove the rule from `~/.config/superkube/config.yaml` to proceed, or use `kubectl delete --all` directly if you really mean it.

### Color in my terminal is wrong / pipes are getting ANSI codes

Pipes shouldn't get ANSI — superkube detects non-TTY stdout and switches to plain text. If you're seeing ANSI in a pipe, please [file an issue](https://github.com/optimumsage/superkube/issues). For a quick local override:

```sh
NO_COLOR=1 sk get pods
sk --plain get pods
```

### Audit log location

```sh
sk audit path                # prints the resolved path
sk audit log --since 1h      # show the last hour
```

Default: `~/.local/state/superkube/audit.log` (rotates at 10 MB, mode `0600`).

### Reporting bugs

Please include the output of `sk version` and a redacted excerpt of `sk audit log` around the time of the problem.

---

## Privacy

- **AI calls stay local.** AI commands shell out to `claude` or `gemini` on your machine, running under your existing account. superkube never sends data to any other service.
- **Best-effort redaction.** Secret-shaped fields are stripped from prompts before they leave the process, but redaction can't catch everything. Use `--no-context` for a pure-prompt mode.
- **No telemetry.** superkube does not phone home. The only files it writes are your config, your audit log, and (via `sk ctx` / `sk ns`) your own kubeconfig.

---

## Links

- Source: [github.com/optimumsage/superkube](https://github.com/optimumsage/superkube)
- Issues: [github.com/optimumsage/superkube/issues](https://github.com/optimumsage/superkube/issues)
- krew plugin compatibility notes: [`docs/krew.md`](docs/krew.md)

## License

[Apache-2.0](LICENSE)
