# renovate-auto-review

[![CI](https://github.com/Cassin01/renovate-auto-review/actions/workflows/ci.yml/badge.svg)](https://github.com/Cassin01/renovate-auto-review/actions/workflows/ci.yml)

A Go script that enumerates open Renovate PRs from a GitHub repository
specified on the command line. For each PR, it launches
`claude --permission-mode dontAsk` so that Claude decides whether the PR is
safe to merge and writes a review body; the tool then adds the
duplicate-detection marker and footer and posts the comment on GitHub.

Every `claude` subprocess runs in `dontAsk` mode: only the tools in its
`--allowedTools` list run and everything else is refused (no classifier
fallback). A shared `--disallowedTools` list — GitHub writes (`gh api`,
`gh pr comment|edit|merge`, `gh gist`, …), `git push|commit|remote|config`,
package publishing, `curl`/`wget`, web tools — is denied on top, and deny rules
win over any allow rule merged in from your or the target repo's settings. The
review subprocess may only run `gh pr view|diff|checks` and write its comment
file.

## Prerequisites

- `gh` v2.x installed, with the `repo` scope (verify via `gh auth status`)
- `claude` CLI installed (`which claude`)
- Push permission or higher on the target repository (required to post comments)

## Setup

Place the config file at the standard XDG path.

```sh
mkdir -p ~/.config/renovate-auto-review
cp config.example.toml ~/.config/renovate-auto-review/config.toml
$EDITOR ~/.config/renovate-auto-review/config.toml
```

If the `XDG_CONFIG_HOME` environment variable is set, that path takes
precedence (`$XDG_CONFIG_HOME/renovate-auto-review/config.toml`).

### Example `config.toml`

```toml
limit = 50
```

The target repository is specified on the command line — see [Usage](#usage).

| Key               | Type     | Default             | Description                                                                |
|-------------------|----------|---------------------|----------------------------------------------------------------------------|
| `limit`           | int      | `50`                | Maximum number of PRs fetched by `gh pr list`                              |
| `auto_fix_ci`     | bool     | `false`             | When true, Claude attempts to auto-fix and push on CI failure              |
| `auto_fix_label`  | string   | `"claude-auto-fix"` | Per-PR opt-in label. A PR with this label is treated as if `auto_fix_ci = true` |
| `auto_rebase_conflicts` | bool | `false`           | When true, a `CONFLICTING` Renovate PR gets the rebase label so Renovate rebases it (see below). Independent of `auto_fix_ci` |
| `rebase_label`    | string   | `"rebase"`          | Label that triggers a Renovate rebase; must match Renovate's `rebaseLabel`  |
| `work_dir`        | string   | `os.TempDir()`      | Parent directory for temporary work dirs used during CI auto-fix           |
| `custom_prompt`   | string   | `""`                | Extra instructions appended to every Claude prompt (see below)             |
| `allowed_owners`  | []string | `[]`                | Domain guard: allowed repository owners (see below). Empty = disabled       |

Unknown keys produce a warning on stderr (helps catch typos).

### `allowed_owners`

A safety guard against running the tool on an unintended repository. The
`owner` part of the `<owner/name>` argument is matched case-insensitively
against this list; if it is not present, the tool exits with an error before
any `gh` or `claude` invocation (including `-dry-run`). An empty or omitted
list disables the guard and permits any owner.

```toml
allowed_owners = ["Cassin01", "my-org"]
```

### `custom_prompt`

When set, the value is injected near the end of both the review and the
auto-fix prompts inside an `[Additional instructions]` section. Because it
appears after the built-in directives, it is well-suited to overriding
defaults (output language, tone, etc.) and adding project-specific
constraints. Leave it empty or omit the key to keep the original prompts
unchanged.

```toml
custom_prompt = "Write the PR comment body in Japanese."
```

The output protocol (the `REVIEW_DONE` / `VERIFY_DONE` marker on the final
line) is enforced after this section, so it cannot be accidentally overridden.
The duplicate-detection HTML marker and the `👷 Generated with
renovate-auto-review` footer are not part of the prompt at all: the tool adds
them mechanically to every comment body before posting.

## Usage

The target repository is passed as a positional argument in the form `owner/name`.

```sh
# List target Renovate PRs without invoking Claude
go run . -dry-run Cassin01/multi_agent_control_tower

# Real run: post comments on Renovate PRs (skipping those that already have a Claude comment)
go run . Cassin01/multi_agent_control_tower

# Review only a single PR by number (skips all other open Renovate PRs)
go run . -pr 42 Cassin01/multi_agent_control_tower

# Use a different config path
go run . -config /path/to/other.toml Cassin01/multi_agent_control_tower
```

By default every open Renovate PR is processed. Pass `-pr <number>` to target a
single PR; if that PR is not a Renovate PR it is skipped.

## Flags

| Flag        | Default                                             | Description                |
|-------------|-----------------------------------------------------|----------------------------|
| `-config`   | `$XDG_CONFIG_HOME/renovate-auto-review/config.toml` | Path to the config file    |
| `-dry-run`  | `false`                                             | Only list target PRs       |
| `-pr`       | `0` (all)                                           | Review only this PR number |

> **BREAKING**: The target repository is now passed as a positional CLI
> argument (`<owner/name>`); the previous `repos` key in `config.toml` is no
> longer supported.

## Renovate PR detection rules

A PR is considered a Renovate PR when both of the following are true:

1. `author.is_bot == true` and `author.login == "app/renovate"`
2. The PR labels include `renovate` or `security`

`security` is accepted because Renovate's vulnerability-fix PRs are commonly
configured with `vulnerabilityAlerts.labels: ["security"]`, and `labels` is
non-mergeable in Renovate: that list *replaces* the repository-level
`labels: ["renovate"]`, so those PRs carry only `security`. The label never
proves who opened the PR (anyone with triage permission can add it); rule 1
does, since the `app/renovate` GitHub App login cannot be used by anyone else.

## Review skip rules

The review step is skipped in either of the following cases (checked in order):

1. **CI is failing.** When at least one completed check on the PR has
   concluded as `FAILURE` / `CANCELLED` / `TIMED_OUT` / `ACTION_REQUIRED` /
   `STARTUP_FAILURE`, the review step is skipped entirely. Auto-fix may still
   run on the same PR — see the next section.
2. **Already reviewed and no new commits since.** When an existing comment
   body contains the built-in marker (`<!-- claude-auto-mergeable-check -->`)
   and no commit has been pushed after that comment, the review is skipped.
   Comments posted by the tool always include the marker (an HTML comment) at
   the top, so PRs whose HEAD has moved (via rebase or extra commits) are
   automatically re-reviewed on the next run.

Auto-fix is driven by the live CI status, not by these rules; see the next
section.

## Auto-fix on CI failure (opt-in)

Auto-fix runs after the review skip check, only when **all** of the
following hold:

- The PR's CI is currently failing (same conclusions as in the previous section)
- Either `auto_fix_ci = true` in `config.toml`, or the PR carries the
  `auto_fix_label` (default `claude-auto-fix`)
- At least one of the failing checks exposes a GitHub Actions run ID via
  its `detailsUrl` (status contexts without a `targetUrl`, e.g. Renovate's
  own status, cannot supply logs and are silently skipped)

Because auto-fix is gated on the live CI state, a PR whose previous fix
attempt did not resolve the failure will be retried on every run. The only
backstops are the prompt's "give up after 3 attempts" rule and your cron
interval.

Behavior:

1. `gh pr view --json statusCheckRollup` detects failing checks; failing
   check `detailsUrl` values are scanned for Actions run IDs
2. Each run's live state is re-fetched (`gh api`) to re-validate the
   possibly-lagging rollup. A run is skipped when it is not a genuine failure
   (a re-run turned it green, or it was cancelled) **or when its `head_sha`
   no longer matches the PR's current head** — the latter guards against the
   rollup surfacing a superseded run whose failure an earlier push (e.g. a
   prior auto-fix commit) has already fixed, which would otherwise waste a
   cycle and report a misleading "nochange"
3. `gh run view <runID> --log-failed` fetches each failing job's logs
   (tail-truncated to the last 50KB). If no run ID could be extracted,
   auto-fix is skipped without invoking Claude
4. `os.MkdirTemp` (under `work_dir`, defaulting to `os.TempDir()`) creates
   an isolated working directory; it is removed on exit via `defer os.RemoveAll`
5. The tool clones and checks out the PR branch, then runs a **verify** phase:
   Claude (in a scoped environment, with commit/push/comment tools disabled)
   identifies the root cause, applies a minimal fix, and re-runs the failing
   CI job's **full step sequence** locally using the project's own commands
   (discovered from `.github/workflows`, `package.json`, `Makefile`, etc. — not
   assumed to be Go). Because the failure log shows only the first failing step,
   every step of the job is re-run — including the formatter/linter over edited
   files — so a fix that breaks a previously-green step is caught before push.
   It writes a verdict to `rar-verify-result.json` — including the exact
   `verified_commands` it ran and observed green — and prints `VERIFY_DONE`.
   Its tool set is limited to read-only `gh`/`git`, the lint/build/test verbs
   of the supported toolchains (`go build|test|vet|get|mod download`,
   `gofmt`, `golangci-lint`, `npm ci|install|run|test`, `pnpm`/`yarn`
   `install|run|test`, `npx prettier|eslint|stylelint`, `make`, `mise run`),
   and the file tools — no `gh api`, `git remote`, `go run`, `npm exec`
6. On a `fixed` verdict, the tool **re-runs each `verified_commands` entry
   itself** and refuses to publish unless all pass — turning the self-reported
   verdict into a machine-checked gate. Entries run **without a shell**, must
   match a fixed allowlist (`go build|test|vet`, `npm run|run-script|test|ci`,
   `pnpm run|test|install`, `yarn run|test|install`, `mise run`, `make`,
   optionally prefixed by `NAME=value` assignments), and run in the verify env
   **minus** the `GH_*`/`GITHUB_*`/`ANTHROPIC_*`/`CLAUDE_*`/`AWS_*` credentials. It then stages tracked changes **plus
   newly-created files** (skipping untracked directories and credential-named
   files such as `.npmrc`/`.env*`/`.netrc`/`.git-credentials`), scans the staged
   diff and comment body for secrets, then commits as
   `fix(ci): <summary> [claude-auto-fix]`, pushes, and posts the comment

The verify phase enforces:

- No `git commit --amend` / `git rebase` / `git reset --hard` /
  `git push --force` / `git push --force-with-lease`
- No `gh pr edit`; commit/push/comment are denied (the tool performs them)
- `.git/config` and `.git/hooks` must be unchanged from checkout: the tool
  snapshots them before verify and, if they differ after re-verification,
  aborts (→ `❌` comment, **no commit/push**). Publish runs `git` with your
  `gh` credentials, so a planted hook, `core.fsmonitor`, `credential.helper`,
  or `url.*.insteadOf` / `pushurl` rewrite would otherwise execute code or
  redirect the push
- Keep existing Renovate commits intact; add exactly one new commit on top
- Only minor fixes are in scope (type fixes, test adjustments, lockfile
  regeneration); major breaking changes are out of scope
- Every step of the failed CI job (not only the step that was red) must pass
  locally — including the project's formatter/linter over edited files — before
  the verdict can be `fixed`
- A `fixed` verdict must list the exact `verified_commands` it ran; the tool
  re-runs them independently before pushing and aborts (→ `❌` comment, **no
  push**) if any fails, is missing, uses shell syntax, or falls outside the
  re-verification allowlist (so `git`, `gh`, `curl`, `npx`, `npm publish` and
  the like are never executed). An
  unverified fix is never pushed. A failure whose output looks like an
  unreachable dependency (DNS/connection error) — not a real lint/build/test
  failure — is retried once; if it persists, the gate still refuses to push but
  reports it as an environment issue (→ `⚠️` comment) rather than a fix
  rejection, so a correct fix blocked by a transient network outage can be
  re-run later

The verdict is `unfixable` (→ `❌` comment, **no push**) when:

- A major breaking change would be required (e.g. an API-incompatible upgrade)
- The fix would require API keys, secrets, or external network access
- The project's lint/test/build cannot be run locally at all (e.g. deps
  cannot be installed) — an unverifiable fix is never pushed
- Build or test still fails after 3 attempts

### Private dependency registries (token-safe)

Some target repos need a private registry token to install dependencies (e.g.
`@scope/*` packages on GitHub Packages). Provide it **via the environment only**
— export it in your shell before running; the tool never stores it and adds no
config field for it:

```sh
export NODE_AUTH_TOKEN=...   # or NPM_TOKEN / NPM_CONFIG_* / YARN_* / PNPM_*
```

The target repo's committed `.npmrc` must use `${NODE_AUTH_TOKEN}` indirection
(never a literal). Because `renovate-auto-review` is a public tool, the token is
kept from leaking by two layers:

1. **Structural.** The flow is split into a *verify* phase and a *publish*
   phase. Verify receives a scoped allowlist of env vars (tooling baseline +
   `gh`/Anthropic vars + the registry-token vars) so `npm ci` works, but it
   cannot commit/push/comment. The publish-time **re-verification** of
   `verified_commands` keeps the registry token but drops the `gh`/Anthropic/
   AWS vars, and executes only allowlisted lint/build/test commands without a
   shell — `git`, `gh`, network clients, and publish/exec subcommands are
   rejected before execution.
   *Ceiling:* `--allowedTools`/`--disallowedTools` gate only the commands
   Claude runs directly. Because verify can edit files and run build/test
   commands, it can still execute arbitrary code indirectly (edit a test or an
   npm script, then run it). Likewise, the re-verification commands still
   execute repository code (npm scripts, Makefile recipes, tests — including
   Claude's edits) as your OS user, which
   can reach the registry token in its env and credentials stored on disk or in
   the keychain (`gh` keyring, git credential helper, `~/.aws`). Full isolation
   needs an OS-level sandbox (container, `sandbox-exec`), which this tool does
   not provide; run auto-fix on a machine/account whose stored credentials you
   accept that exposure for.
   Publish itself runs with the registry-token vars **stripped**, never invokes
   a package manager, and stages tracked changes plus newly-created files —
   excluding untracked directories and credential-named files (`.npmrc`,
   `.env*`, `.netrc`, `.git-credentials`, …), so an accidental literal-token
   `.npmrc` is dropped by name. It then aborts before commit/push if a secret
   pattern is found in the staged diff or comment, which is the content gate for
   token values in any other newly-staged file.
2. **Redaction (defense-in-depth).** All subprocess stdout/stderr is passed
   through a redactor that scrubs credential-shaped strings and the runtime
   values of the known token env vars before anything is printed.

### Caveat: pushing to a Renovate branch

Renovate **stops updating any PR branch that has another author's commits on
it** (official behavior). So while pushing is allowed, note that:

- Ticking the **rebase checkbox** in the PR body or attaching the **`rebase`
  label** causes Renovate to force-rebase the branch, wiping out the manual
  commit
- Renovate will not update that branch afterward, so whoever pushed becomes
  responsible for getting the PR merged

The tool guards against this by denying `gh pr edit` via `--disallowedTools`
and by repeatedly forbidding it in the prompt, so **Claude** does not
accidentally toggle the checkbox or attach the label during a CI auto-fix.

### Conflict handling: requesting a Renovate rebase

When `auto_rebase_conflicts = true`, the **orchestrator itself** (not Claude)
attaches the `rebase_label` to any Renovate PR whose `mergeable` state is
`CONFLICTING`, so Renovate rebases/regenerates the branch and resolves the
conflict. This is the inverse of the caveat above and is safe precisely because
a pure-conflict PR has no tool-pushed commits to wipe out — Renovate stays the
branch owner. Claude's `--disallowedTools` still denies `gh pr edit`; only the
gated, opt-in orchestrator path adds the label.

If the target repository does not define `rebase_label` yet, the orchestrator
creates it there first (`gh pr edit --add-label` rejects an unknown label), then
attaches it.

The action is idempotent: it is skipped if the rebase label is already present
(a request Renovate has not yet acted on) or if a previous **rebase-request**
comment (marker `<!-- claude-auto-rebase-request -->`) is still waiting for a new
commit on the branch. That marker is deliberately separate from the review marker
— a conflict is caused by the base branch moving, not by a PR commit, so gating
on the review marker would suppress the request for any PR the tool had already
commented on. A conflicting PR skips the merge-safety
review for that run, and a freshly requested rebase skips the CI auto-fix path
(the branch is about to change).

Both CI auto-fix and conflict handling are skipped under `-dry-run`.

## Known limitations

- Claude CLI usage costs scale with the number of PRs
- PRs are processed sequentially; parallel execution is not supported
- Claude's direct `git push` (including `--force`) is denied, and unlisted
  forms such as `git -C . push` are refused by `dontAsk`. Code that Claude
  writes and then runs through a build/test command is not covered by tool
  rules (see the ceiling under *Private dependency registries*)
- The process exits with code `1` whenever any PR-level step fails, even
  if other PRs in the same run succeeded. A non-zero exit signals "at least
  one PR failed", not "no work happened" — useful for cron alerts
