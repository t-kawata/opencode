package prompt

import "github.com/opencode-ai/opencode/internal/llm/models"

func TranslaterPrompt(_ models.ModelProvider) string {
	return `You are a professional Japanese to English translator, an expert in prompt translation that conveys precise intent to LLM.\n` +
		`Please translate the given Japanese prompts wrapped by <target></target> tag into English prompts without the tag for input into LLM. Please translate literally so that no loss of meaning occurs. Never output anything other than the translated English prompts.`
}
