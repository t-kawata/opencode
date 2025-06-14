# README for KAWATA
- 独自にKAWATAがカスタマイズした内容と、
- 適切なインストール方法をメモ
# dependencies
```
brew install ripgrep
brew install fzf
```
# settings
```
cat <<EOF > ~/.opencode.json
{
  "providers": {
    "openrouter": {
      "apiKey": "your-openrouter-api-key",
      "disabled": false
    }
  },
  "agents": {
    "coder": {
      "model": "openrouter.devstral-small-free",
      "maxTokens": 30000
    },
    "task": {
      "model": "openrouter.devstral-small-free",
      "maxTokens": 5000
    },
    "title": {
      "model": "openrouter.gemma-3-4b-it-free",
      "maxTokens": 80
    },
    "translater": {
      "model": "openrouter.gemma-3-4b-it-free",
      "maxTokens": 5000
    }
  },
  "lsp": {
    "go": {
      "disabled": false,
      "command": "gopls"
    },
    "typescript": {
      "disabled": false,
      "command": "typescript-language-server",
      "args": ["--stdio"]
    }
  }
}
EOF
```
# build & install
```
make build-install
```
# build (Optional)
```
make build-kawata
```
# install (Optional)
```
make install
```
# 本家OpenCodeに追加した機能
## `/en` prefix と自動翻訳
- ユーザーの入力は、基本的には全て日英翻訳されてAIに入力されるようにした
- `/en` prefix をつけることで、そのメッセージはそのまま英語として入力することもできる
## 使用可能なモデルの追加
### 編集ファイル
- 以下のファイルで `Kawata` と検索して、それに倣って記述すると、
```
- internal/llm/models/models.go
- internal/llm/models/openrouter.go
```
- OpenRouterのモデルを使用して色々できるかな。