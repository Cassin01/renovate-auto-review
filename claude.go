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
