# Using krew plugins with superkube

`superkube` doesn't replace `kubectl` — it wraps it. Any verb superkube doesn't recognize is forwarded to `kubectl` verbatim, with exit codes and signals propagated. That means **every plugin you've installed via [krew](https://krew.sigs.k8s.io/) already works as a `sk` subcommand** with no extra wiring.

## How it works

When you run `sk foo …`, superkube's command router (`internal/cli/root.go`) checks whether `foo` matches a built-in verb (`get`, `delete`, `apply`, `diagnose`, etc.). If not, the full argv is handed to `kubectl foo …`. krew installs plugins as `kubectl-foo` binaries on your `$PATH`; kubectl resolves the verb the same way for both `kubectl foo` and our forwarded call.

Two consequences worth knowing:

- **The audit log records the plugin verb.** `sk neat -f deploy.yaml` appears as `verb: "neat"` in `~/.local/state/superkube/audit.log` — convenient for tracing which plugin a noisy session was using.
- **Plugins don't go through superkube guardrails.** If a plugin deletes objects internally, superkube doesn't intercept; the plugin is fully trusted. Confirmation, dry-run preview, and per-context `forbid:` policy apply only to the verbs superkube owns. If you want a guardrail on a plugin, file an issue.

## Examples

### `sk neat` — strip noisy server-managed fields

[`kubectl-neat`](https://github.com/itaysk/kubectl-neat) removes `creationTimestamp`, `resourceVersion`, `uid`, and other server-only fields from `kubectl get -o yaml` output, leaving a clean manifest you can edit and re-apply.

```sh
kubectl krew install neat
sk get deployment web -o yaml | sk neat
```

### `sk tree` — owner-graph visualizer

[`kubectl-tree`](https://github.com/ahmetb/kubectl-tree) prints the ownership graph for a resource — Deployment → ReplicaSet → Pod, or Service → EndpointSlice, etc. Pairs nicely with `sk diagnose`, which now ships its own owner-chain in v0.3 (see the [diagnose section in the README](../README.md#ai-integration)).

```sh
kubectl krew install tree
sk tree deployment web
```

### `sk node-shell` — debug a node interactively

[`kubectl-node-shell`](https://github.com/kvaps/kubectl-node-shell) drops you into a privileged debug pod on a node — handy when an `sk diagnose` points at a node-level problem.

```sh
kubectl krew install node-shell
sk node-shell ip-10-0-1-23.ec2.internal
```

## Pinning behavior

If a krew plugin happens to share a name with a future superkube verb (say, krew releases a `kubectl-tui` plugin and superkube gains `sk tui`), the built-in wins — that's the whole point of the routing table. Run the plugin directly as `kubectl tui` to bypass.

## Listing what krew has installed

```sh
sk krew list                  # forwards to kubectl krew list, which lists plugins
```
