package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const promptTemplate = `対象: %s の PR #%d (Renovate 自動更新)

あなたは熟練のレビュアーです。次の手順で PR を分析し、最後に GitHub にコメントを投稿してください。

1. ` + "`gh pr view %d --repo %s`" + ` で PR の概要・状態を取得する
2. ` + "`gh pr diff %d --repo %s`" + ` で差分を確認する
3. ` + "`gh pr checks %d --repo %s`" + ` で CI 結果を確認する
4. 上記から「マージしても安全か」を判断し、以下のテンプレートで日本語コメントを生成する:

%s
## Claude 自動レビュー
- **マージ可否判定**: (✅安全 / ⚠️要確認 / ❌マージ非推奨 のいずれか)
- **理由**: (2〜4 行で根拠)
- **CI 状態**: (PASS/FAIL/PENDING を列挙)
- **注意点**: (ある場合のみ。なければ「特になし」)

5. 生成したコメントを ` + "`gh pr comment %d --repo %s --body \"<コメント本文>\"`" + ` で投稿する
   - 改行を含む場合は ` + "`--body-file -`" + ` を使い、ヒアドキュメントで流し込む方式でもよい

重要事項:
- コメント本文の最初の行に必ず %s を含めること(重複検出マーカー)
- 不確実な場合は ⚠️要確認 を選び、断定的な「安全」判定は避ける
- 投稿成功後、最後の出力行に POSTED とだけ表示する
- それ以外の余計な解説出力は不要
`

func buildPrompt(repo string, prNumber int, marker string) string {
	return fmt.Sprintf(promptTemplate,
		repo, prNumber,
		prNumber, repo,
		prNumber, repo,
		prNumber, repo,
		marker,
		prNumber, repo,
		marker,
	)
}

type claudeResult struct {
	Stdout string
	Posted bool
}

func runClaudeAutoMode(claudeBin, repo string, prNumber int, marker string) (*claudeResult, error) {
	prompt := buildPrompt(repo, prNumber, marker)
	cmd := exec.Command(
		claudeBin,
		"--permission-mode", "auto",
		"--print",
		"--output-format", "text",
		"--allowedTools",
		"Bash(gh pr view:*) Bash(gh pr diff:*) Bash(gh pr checks:*) Bash(gh pr comment:*)",
		"-p", prompt,
	)
	out, err := cmd.CombinedOutput()
	res := &claudeResult{
		Stdout: string(out),
		Posted: strings.Contains(string(out), "POSTED"),
	}
	if err != nil {
		return res, fmt.Errorf("claude exec failed for PR #%s: %w", strconv.Itoa(prNumber), err)
	}
	if !res.Posted {
		return res, fmt.Errorf("claude did not report POSTED for PR #%d", prNumber)
	}
	return res, nil
}

const fixPromptTemplate = `対象: %s の PR #%d (Renovate 自動更新)。CI 失敗をローカルで最小修正し、PR ブランチ ` + "`%s`" + ` に直接 push してください。

【絶対遵守 — 違反した場合は即終了】
- 作業ディレクトリは ` + "`%s`" + ` 固定。` + "`cd`" + ` でこの外に出ない
- ` + "`git commit --amend`" + ` / ` + "`git rebase`" + ` / ` + "`git reset --hard`" + ` / ` + "`git push --force`" + ` / ` + "`--force-with-lease`" + ` を使わない
- ` + "`gh pr edit`" + ` を使わない (PR 本文の rebase checkbox に絶対に触れない)
- 既存 Renovate コミットは保持し、新規コミットを 1 つだけ上に積む
- 依存のメジャー破壊変更まで踏み込まない (軽微な型修正・テスト調整・lock 再生成のみ)

【手順】
1. ` + "`gh pr checkout %d --repo %s`" + ` でブランチを checkout
2. 以下の失敗ログ抜粋を読み、原因を特定:
---
%s
---
3. ソースを最小修正し、ローカルで build/test (` + "`go build ./...`" + ` / ` + "`go test ./...`" + ` 等) して通ることを確認
4. ` + "`git add`" + ` → ` + "`git commit -m \"fix(ci): <概要> [claude-auto-fix]\"`" + `
5. ` + "`git push origin HEAD:%s`" + ` (force 系オプションなし)
6. ` + "`gh pr comment %d --repo %s --body-file -`" + ` で以下のテンプレを投稿:

%s
## Claude 自動修正
- 判定: ✅修正 push 済 / ⚠️部分修正 / ❌修正不能 のいずれか
- 失敗 Check: <name 列挙>
- 推定原因: (2-3 行)
- 修正内容: (2-3 行。push した場合のみ。push しなかった場合は「なし」)

【フォールバック】
以下のいずれかに該当する場合は **絶対に push せず**、❌修正不能 でコメントだけ投稿して終了:
- メジャー版破壊変更が必要 (例: API 互換性が無い library upgrade)
- lock file 再生成だけではテストが通らない複合変更
- 修正に API キー / シークレット / 外部接続が必要
- build または test を 3 回試行しても通らない

【出力規約】
- push して comment 投稿成功時: 最終行に ` + "`PUSHED`" + ` を出力
- comment 投稿のみ (修正不能フォールバック) 成功時: 最終行に ` + "`POSTED`" + ` を出力
- 余計な解説出力は不要
`

func buildFixPrompt(repo string, prNumber int, headRef, marker, workDir, logsExcerpt string) string {
	return fmt.Sprintf(fixPromptTemplate,
		repo, prNumber, headRef,
		workDir,
		prNumber, repo,
		logsExcerpt,
		headRef,
		prNumber, repo,
		marker,
	)
}

const fixAllowedTools = "Bash(gh pr view:*) Bash(gh pr diff:*) Bash(gh pr checks:*) " +
	"Bash(gh pr checkout:*) Bash(gh pr comment:*) Bash(gh run view:*) " +
	"Bash(gh repo clone:*) Bash(gh api:*) " +
	"Bash(git status:*) Bash(git diff:*) Bash(git log:*) Bash(git add:*) " +
	"Bash(git commit:*) Bash(git push:*) Bash(git fetch:*) Bash(git checkout:*) " +
	"Bash(git remote:*) Bash(git show:*) " +
	"Bash(go:*) Bash(make:*) Bash(npm:*) Bash(pnpm:*) Bash(yarn:*) Bash(mise:*) " +
	"Bash(ls:*) Bash(cat:*) Bash(rg:*) Bash(fd:*) " +
	"Read Write Edit Glob Grep"

func runClaudeFixMode(claudeBin, repo string, prNumber int, headRef, marker, workDir, logs string) (*claudeResult, error) {
	prompt := buildFixPrompt(repo, prNumber, headRef, marker, workDir, logs)
	cmd := exec.Command(
		claudeBin,
		"--permission-mode", "auto",
		"--print",
		"--output-format", "text",
		"--allowedTools", fixAllowedTools,
		"-p", prompt,
	)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	res := &claudeResult{
		Stdout: string(out),
		Posted: strings.Contains(string(out), "POSTED") || strings.Contains(string(out), "PUSHED"),
	}
	if err != nil {
		return res, fmt.Errorf("claude fix exec failed for PR #%d: %w", prNumber, err)
	}
	if !res.Posted {
		return res, fmt.Errorf("claude did not report PUSHED or POSTED for PR #%d", prNumber)
	}
	return res, nil
}
