# renovate-auto-review

[![CI](https://github.com/Cassin01/renovate-auto-review/actions/workflows/ci.yml/badge.svg)](https://github.com/Cassin01/renovate-auto-review/actions/workflows/ci.yml)

設定ファイルに列挙した GitHub リポジトリの Open Renovate PR を抽出し、各 PR
ごとに `claude --permission-mode auto` (Claude auto mode) を起動して、PR が
マージ可能かを Claude 自身に判断させて GitHub にコメント投稿させる Go スクリプト。

## 前提

- `gh` v2 系インストール済、`gh auth status` で `repo` スコープが付いていること
- `claude` CLI インストール済 (`which claude`)
- 対象リポジトリへの push 権限以上 (コメント投稿に必要)

## セットアップ

設定ファイルを XDG 規約に従ったパスに配置する。

```sh
mkdir -p ~/.config/renovate-auto-review
cp config.example.toml ~/.config/renovate-auto-review/config.toml
$EDITOR ~/.config/renovate-auto-review/config.toml
```

`XDG_CONFIG_HOME` 環境変数が設定されていればそちらが優先される
(`$XDG_CONFIG_HOME/renovate-auto-review/config.toml`)。

### config.toml の例

```toml
limit  = 50
repos = [
  "Cassin01/multi_agent_control_tower",
  "owner/another-repo",
]
```

| キー              | 型       | 既定値              | 説明                                                                       |
|-------------------|----------|---------------------|----------------------------------------------------------------------------|
| `limit`           | int      | `50`                | `gh pr list` で取得する最大件数                                            |
| `repos`           | []string | (必須)              | 対象リポジトリの `owner/name` リスト                                       |
| `auto_fix_ci`     | bool     | `false`             | true で CI 失敗時に Claude による自動修正・push を試行                     |
| `auto_fix_label`  | string   | `"claude-auto-fix"` | PR 単位の opt-in 用ラベル。このラベルが付いた PR は `auto_fix_ci` 扱い      |
| `work_dir`        | string   | `os.TempDir()`      | CI 自動修正時の一時作業ディレクトリ親パス                                  |

未知のキーがあると stderr に warning が出る (タイポ検出)。

## 使い方

```sh
# config に書かれた全リポジトリを対象に、対象 PR の一覧だけ確認 (claude は起動しない)
go run . -dry-run

# 本実行: 全リポジトリの Renovate PR にコメントを付ける (既に Claude コメントがあるものはスキップ)
go run .

# 別の config パスを指定
go run . -config /path/to/other.toml
```

## フラグ

| フラグ      | 既定値                                              | 説明                       |
|-------------|----------------------------------------------------|----------------------------|
| `-config`   | `$XDG_CONFIG_HOME/renovate-auto-review/config.toml`| 設定ファイルのパス         |
| `-dry-run`  | `false`                                            | 対象 PR の一覧表示のみ     |

> **BREAKING**: 以前あった `-repo` / `-marker` / `-limit` フラグは廃止し、
> すべて `config.toml` に移動した。

## Renovate PR 判定基準

以下の AND 条件で判定する:

1. `author.is_bot == true` かつ `author.login == "app/renovate"`
2. PR のラベルに `renovate` を含む

## 重複コメント防止

各 PR の既存コメント本文に組込みのマーカー文字列
(`<!-- claude-auto-mergeable-check -->`) が含まれており、**かつそのコメント
投稿以降に新しいコミットが PR に追加されていない場合のみ** スキップする。
rebase や追加コミットで HEAD が動いた PR は自動的に再レビューされる。
Claude が投稿するコメントは必ず先頭にこのマーカー (HTML コメント) を含む。

## CI 失敗の自動修正 (opt-in)

レビュー後、PR の CI が失敗していて以下のいずれかに該当する場合、Claude が
ローカルで checkout → 修正 → `git push` を試行する:

- `config.toml` で `auto_fix_ci = true`
- PR に `auto_fix_label` (default `claude-auto-fix`) が付与されている

動作:

1. `gh pr view --json statusCheckRollup` で失敗 check を検出
2. `gh run view <runID> --log-failed` で失敗ジョブのログを取得 (末尾 50KB)
3. `os.MkdirTemp` で隔離した作業ディレクトリを作成し、Claude に `cmd.Dir`
   として渡す。終了時に `defer os.RemoveAll` で完全削除
4. Claude プロンプトで以下を強制:
   - `git commit --amend` / `git rebase` / `git reset` / `git push --force` 系禁止
   - `gh pr edit` 禁止 (PR 本文の rebase checkbox に絶対触れない)
   - Renovate の既存コミットは保持し、新規コミットを 1 つだけ積む
   - メジャー破壊変更は試みず、修正不能と判定したら push せずコメントのみ
5. 完了時は最終行に `PUSHED` (push あり) または `POSTED` (コメントのみ) を出力

### Renovate ブランチへ push する慣習について

Renovate は **他者の commit が乗った PR ブランチの更新を停止する** (公式仕様)。
そのため push 自体は許容されるが、以下に注意:

- PR 本文の **rebase checkbox** にチェックを入れる / **`rebase` ラベル** を付与
  すると、Renovate が強制 rebase して手動コミットが消える
- 以降 Renovate がそのブランチを更新しなくなるため、merge までの責任を持つこと

ツール側では `gh pr edit` を allowedTools から除外し、プロンプトでも繰り返し
禁止することで、Claude が誤ってこれらを触らないようガードしている。

`-dry-run` 時は CI 自動修正も発火しない。

## 既知の制約

- Claude の API 利用料が PR の本数だけ発生する
- `--permission-mode auto` の分類器が `gh pr comment` を soft-deny した場合に
  備え、`--allowedTools` で明示的に許可している
- 並列実行は未対応 (リポジトリも PR も順次処理)
- `auto_fix_ci` 経由の自動修正は `git push --force` 系を allowedTools の
  prefix マッチでは弾けないため、プロンプトでの禁止指示に依存する
