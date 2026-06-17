# renovate-auto-review

[![CI](https://github.com/Cassin01/renovate-auto-review/actions/workflows/ci.yml/badge.svg)](https://github.com/Cassin01/renovate-auto-review/actions/workflows/ci.yml)

A Go script that enumerates open Renovate PRs from a GitHub repository
specified on the command line. For each PR, it launches
`claude --permission-mode auto` (Claude auto mode) so that Claude decides
whether the PR is safe to merge and then posts a comment on GitHub.

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

The output protocol (the `POSTED` / `PUSHED` marker on the final line, the
duplicate-detection HTML marker, and the `👷 Generated with renovate-auto-review`
footer at the end of the comment body) is enforced after this section, so it
cannot be accidentally overridden.

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
2. The PR labels include `renovate`

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
2. `gh run view <runID> --log-failed` fetches each failing job's logs
   (tail-truncated to the last 50KB). If no run ID could be extracted,
   auto-fix is skipped without invoking Claude
3. `os.MkdirTemp` (under `work_dir`, defaulting to `os.TempDir()`) creates
   an isolated working directory; it is removed on exit via `defer os.RemoveAll`
4. The tool clones and checks out the PR branch, then runs a **verify** phase:
   Claude (in a scoped environment, with commit/push/comment tools disabled)
   identifies the root cause, applies a minimal fix, and re-runs the failing
   CI job's **full step sequence** locally using the project's own commands
   (discovered from `.github/workflows`, `package.json`, `Makefile`, etc. — not
   assumed to be Go). Because the failure log shows only the first failing step,
   every step of the job is re-run — including the formatter/linter over edited
   files — so a fix that breaks a previously-green step is caught before push.
   It writes a verdict to `rar-verify-result.json` — including the exact
   `verified_commands` it ran and observed green — and prints `VERIFY_DONE`
5. On a `fixed` verdict, the tool **re-runs each `verified_commands` entry
   itself** (same scoped verify env, so lint/build/test reproduce what Claude
   saw) and refuses to publish unless all pass — turning the self-reported
   verdict into a machine-checked gate. It then stages **tracked changes only**,
   scans the staged diff and comment body for secrets, then commits as
   `fix(ci): <summary> [claude-auto-fix]`, pushes, and posts the comment

The verify phase enforces:

- No `git commit --amend` / `git rebase` / `git reset --hard` /
  `git push --force` / `git push --force-with-lease`
- No `gh pr edit`; commit/push/comment are disabled (the tool performs them)
- Keep existing Renovate commits intact; add exactly one new commit on top
- Only minor fixes are in scope (type fixes, test adjustments, lockfile
  regeneration); major breaking changes are out of scope
- Every step of the failed CI job (not only the step that was red) must pass
  locally — including the project's formatter/linter over edited files — before
  the verdict can be `fixed`
- A `fixed` verdict must list the exact `verified_commands` it ran; the tool
  re-runs them independently before pushing and aborts (→ `❌` comment, **no
  push**) if any fails, is missing, or embeds a `git`/`gh` write verb. A command
  that cannot be reproduced tool-side (e.g. needs unavailable network) fails the
  gate — an unverified fix is never pushed

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
   `verified_commands` reuses the same scoped env (token present) but only
   re-runs read-only lint/build/test — `git`/`gh` write verbs are rejected
   before execution, so the token never reaches a publish operation through it.
   Publish itself runs with the registry-token vars **stripped**, never invokes
   a package manager, stages **tracked files only** (an accidental literal-token
   `.npmrc` is untracked and excluded), and aborts before commit/push if a
   secret pattern is found in the staged diff or comment.
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

The tool guards against this by excluding `gh pr edit` from `allowedTools`
and by repeatedly forbidding it in the prompt, so **Claude** does not
accidentally toggle the checkbox or attach the label during a CI auto-fix.

### Conflict handling: requesting a Renovate rebase

When `auto_rebase_conflicts = true`, the **orchestrator itself** (not Claude)
attaches the `rebase_label` to any Renovate PR whose `mergeable` state is
`CONFLICTING`, so Renovate rebases/regenerates the branch and resolves the
conflict. This is the inverse of the caveat above and is safe precisely because
a pure-conflict PR has no tool-pushed commits to wipe out — Renovate stays the
branch owner. Claude's `allowedTools` still excludes `gh pr edit`; only the
gated, opt-in orchestrator path adds the label.

The action is idempotent: it is skipped if the rebase label is already present
(a request Renovate has not yet acted on) or if a marker comment was already
posted with no newer commit since. A conflicting PR skips the merge-safety
review for that run, and a freshly requested rebase skips the CI auto-fix path
(the branch is about to change).

Both CI auto-fix and conflict handling are skipped under `-dry-run`.

## Known limitations

- Claude CLI usage costs scale with the number of PRs
- The `--permission-mode auto` classifier sometimes soft-denies
  `gh pr comment`, so it is explicitly granted via `--allowedTools`
- PRs are processed sequentially; parallel execution is not supported
- The auto-fix flow cannot block `git push --force` family commands via the
  `allowedTools` prefix match, so it relies on the prompt's explicit prohibition
- The process exits with code `1` whenever any PR-level step fails, even
  if other PRs in the same run succeeded. A non-zero exit signals "at least
  one PR failed", not "no work happened" — useful for cron alerts
