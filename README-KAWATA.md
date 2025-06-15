# README for KAWATA
- 独自にKAWATAがカスタマイズした内容と、
- 適切なインストール方法をメモ
# dependencies
```
brew install ripgrep
brew install fzf
npm install -g vscode-html-languageserver-bin
npm install -g vscode-css-languageserver-bin
npm install -g typescript typescript-language-server
go install golang.org/x/tools/gopls@latest
```
# settings
```
cat <<EOF > ~/.opencode.json
{
  "providers": {
    "openai": {
      "apiKey": "<OPENAI_API_KEY>",
      "disabled": false
    },
    "gemini": {
      "apiKey": "<GEMINI_API_KEY>",
      "disabled": false
    },
    "openrouter": {
      "apiKey": "<OPENROUTER_API_KEY>",
      "disabled": false
    },
    "local": {
      "apiKey": "dummy",
      "disabled": false,
      "endpoint": "<e.g. http://localhost:11434/v1>"
    }
  },
  "agents": {
    "coder": {
      "model": "gpt-4.1-mini",
      "maxTokens": 30000
    },
    "task": {
      "model": "gpt-4.1-mini",
      "maxTokens": 5000
    },
    "title": {
      "model": "openrouter.gemma-3-4b-it-free",
      "maxTokens": 80
    },
    "summarizer": {
      "model": "openrouter.gemma-3-4b-it-free",
      "maxTokens": 2000
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
    },
    "html": {
      "disabled": false,
      "command": "html-languageserver",
      "args": ["--stdio"]
    },
    "css": {  
      "disabled": false,  
      "command": "css-languageserver",  
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