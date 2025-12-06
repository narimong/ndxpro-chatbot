package service

import (
	"context"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"simple-chatbot/api/config"
)

type ChatService struct {
	chain        compose.Runnable[map[string]any, *schema.Message]
	sessionStore *SessionStore
}

func NewChatService(ctx context.Context, cfg *config.Config, sessionStore *SessionStore) (*ChatService, error) {
	model, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey: cfg.OpenAIAPIKey,
		Model:  cfg.Model,
	})
	if err != nil {
		return nil, err
	}

	chatTemplate := prompt.FromMessages(
		schema.FString,
		schema.SystemMessage("{system_prompt}"),
		schema.MessagesPlaceholder("history", true),
		schema.UserMessage("{input}"),
	)

	chain, err := compose.NewChain[map[string]any, *schema.Message]().
		AppendChatTemplate(chatTemplate).
		AppendChatModel(model).
		Compile(ctx)
	if err != nil {
		return nil, err
	}

	return &ChatService{
		chain:        chain,
		sessionStore: sessionStore,
	}, nil
}

func (c *ChatService) ChatStream(ctx context.Context, sessionID, input string) (*schema.StreamReader[*schema.Message], error) {
	session, err := c.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}

	systemPrompt := session.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "You are a helpful assistant. Answer questions concisely and clearly."
	}

	streamReader, err := c.chain.Stream(ctx, map[string]any{
		"system_prompt": systemPrompt,
		"history":       session.History,
		"input":         input,
	})
	if err != nil {
		return nil, err
	}

	return streamReader, nil
}

func (c *ChatService) Chat(ctx context.Context, sessionID, input string) (*schema.Message, error) {
	session, err := c.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}

	systemPrompt := session.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "You are a helpful assistant. Answer questions concisely and clearly."
	}

	result, err := c.chain.Invoke(ctx, map[string]any{
		"system_prompt": systemPrompt,
		"history":       session.History,
		"input":         input,
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}
