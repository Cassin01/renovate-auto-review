# renovate-auto-review

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

| キー    | 型       | 既定値 | 説明                                |
|---------|----------|--------|-------------------------------------|
| `limit` | int      | `50`   | `gh pr list` で取得する最大件数      |
| `repos` | []string | (必須) | 対象リポジトリの `owner/name` リスト|

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

## 既知の制約

- Claude の API 利用料が PR の本数だけ発生する
- `--permission-mode auto` の分類器が `gh pr comment` を soft-deny した場合に
  備え、`--allowedTools` で明示的に許可している
- 並列実行は未対応 (リポジトリも PR も順次処理)
