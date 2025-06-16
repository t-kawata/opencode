package prompt

import "github.com/cap-ai/cap/internal/llm/models"

func TitlePrompt(_ models.ModelProvider) string {
	return `you will generate a short title based on the first message a user begins a conversation with
- The title must be in Japanese
- ensure it is not more than 25 characters long in Japanese
- the title should be a summary of the user's message
- it should be one line long
- do not use quotes or colons
- the entire text you return will be used as the title
- never return anything that is more than one sentence (one line) long`
}
