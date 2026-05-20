# renovate-auto-review

[![CI](https://github.com/Cassin01/renovate-auto-review/actions/workflows/ci.yml/badge.svg)](https://github.com/Cassin01/renovate-auto-review/actions/workflows/ci.yml)

A Go script that enumerates open Renovate PRs from the GitHub repositories
listed in a config file, then launches `claude --permission-mode auto`
(Claude auto mode) per PR so that Claude itself decides whether the PR is
safe to merge and posts a comment on GitHub.

## Prerequisites

- `gh` v2.x installed, with the `repo` scope on `gh auth status`
- `claude` CLI installed (`which claude`)
- Push permission or higher on the target repositories (required to post comments)

## Setup

Place the config file at the XDG-conventional path.

```sh
mkdir -p ~/.config/renovate-auto-review
cp config.example.toml ~/.config/renovate-auto-review/config.toml
$EDITOR ~/.config/renovate-auto-review/config.toml
```

If the `XDG_CONFIG_HOME` environment variable is set, that path takes
precedence (`$XDG_CONFIG_HOME/renovate-auto-review/config.toml`).

### Example `config.toml`

```toml
limit  = 50
repos = [
  "Cassin01/multi_agent_control_tower",
  "owner/another-repo",
]
```

| Key               | Type     | Default             | Description                                                                |
|-------------------|----------|---------------------|----------------------------------------------------------------------------|
| `limit`           | int      | `50`                | Maximum number of PRs fetched by `gh pr list`                              |
| `repos`           | []string | (required)          | List of target repositories in `owner/name` form                           |
| `auto_fix_ci`     | bool     | `false`             | When true, Claude attempts to auto-fix and push on CI failure              |
| `auto_fix_label`  | string   | `"claude-auto-fix"` | Per-PR opt-in label. A PR with this label is treated as `auto_fix_ci`      |
| `work_dir`        | string   | `os.TempDir()`      | Parent path for temporary work directories used during CI auto-fix         |
| `custom_prompt`   | string   | `""`                | Extra instruction appended to every Claude prompt (see below)              |

Unknown keys produce a warning on stderr (typo detection).

### `custom_prompt`

When set, the value is injected near the end of both the review and the
auto-fix prompts inside an `[Additional instructions]` section. Because it
appears after the built-in directives, it is well-suited to overriding
defaults (output language, tone, etc.) and adding project-specific
constraints. Leave it empty (or omit it) to keep the original prompts
unchanged.

```toml
custom_prompt = "Write the PR comment body in Japanese."
```

The output protocol (`POSTED` / `PUSHED` marker and the duplicate-detection
HTML marker in the comment body) is enforced after this section, so it
cannot be accidentally overridden.

## Usage

```sh
# List target PRs for every repo in the config without invoking claude
go run . -dry-run

# Real run: post comments on Renovate PRs across all repos (skipping ones that already have a Claude comment)
go run .

# Use a different config path
go run . -config /path/to/other.toml
```

## Flags

| Flag        | Default                                             | Description                |
|-------------|-----------------------------------------------------|----------------------------|
| `-config`   | `$XDG_CONFIG_HOME/renovate-auto-review/config.toml` | Path to the config file    |
| `-dry-run`  | `false`                                             | Only list target PRs       |

> **BREAKING**: The previous `-repo` / `-marker` / `-limit` flags have been
> removed; they are all now configured via `config.toml`.

## Renovate PR detection rules

A PR is considered a Renovate PR when both of the following are true:

1. `author.is_bot == true` and `author.login == "app/renovate"`
2. The PR labels include `renovate`

## Duplicate-comment prevention

A PR is skipped only when an existing comment body contains the built-in
marker string (`<!-- claude-auto-mergeable-check -->`) **and no new commits
have been pushed since that comment was posted**. PRs whose HEAD has moved
(via rebase or extra commits) are automatically re-reviewed. Comments
posted by Claude always include the marker (an HTML comment) at the top.

## Auto-fix on CI failure (opt-in)

After the review, if the PR's CI is failing and either of the following is
true, Claude will attempt to check out the branch locally, fix it, and
`git push`:

- `auto_fix_ci = true` in `config.toml`
- The PR has the `auto_fix_label` (default `claude-auto-fix`)

Behavior:

1. `gh pr view --json statusCheckRollup` detects failing checks
2. `gh run view <runID> --log-failed` fetches the failing job's logs (last 50KB)
3. `os.MkdirTemp` creates an isolated working directory passed to Claude via
   `cmd.Dir`. It is deleted on exit via `defer os.RemoveAll`
4. The Claude prompt enforces:
   - No `git commit --amend` / `git rebase` / `git reset` / `git push --force` family
   - No `gh pr edit` (never touch the rebase checkbox in the PR body)
   - Keep existing Renovate commits intact; add exactly one new commit on top
   - No major breaking changes; if deemed unfixable, post a comment only without pushing
5. On completion, print `PUSHED` (when a push happened) or `POSTED` (comment-only) on the final line

### Convention for pushing to a Renovate branch

Renovate **stops updating any PR branch that has another author's commits on
it** (official behavior). So while pushing itself is allowed, note that:

- Ticking the **rebase checkbox** in the PR body or attaching the **`rebase`
  label** causes Renovate to force-rebase the branch, wiping out the manual
  commit
- Renovate will not update that branch afterward, so whoever pushed becomes
  responsible for getting the PR to merge

The tool guards against this by excluding `gh pr edit` from `allowedTools`
and by repeatedly forbidding it in the prompt, so Claude does not
accidentally touch these.

CI auto-fix does not fire under `-dry-run` either.

## Known limitations

- Claude API usage costs scale with the number of PRs
- In case the `--permission-mode auto` classifier soft-denies `gh pr comment`,
  it is explicitly allowed via `--allowedTools`
- Parallel execution is not supported (repos and PRs are processed sequentially)
- The auto-fix flow cannot block `git push --force` family commands via the
  `allowedTools` prefix match, so it relies on the prompt's explicit prohibition
