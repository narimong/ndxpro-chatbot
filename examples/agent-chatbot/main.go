package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/schema"
)

func main() {
	ctx := context.Background()

	// Check for API key
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Println("Error: OPENAI_API_KEY environment variable is not set")
		fmt.Println("Usage: OPENAI_API_KEY=your-key go run main.go")
		os.Exit(1)
	}

	// 1. Initialize OpenAI ChatModel
	model, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey: apiKey,
		Model:  "gpt-4o-mini",
	})
	if err != nil {
		fmt.Printf("Failed to create chat model: %v\n", err)
		os.Exit(1)
	}

	// 2. Create chat template with system message, history placeholder, and user input
	chatTemplate := prompt.FromMessages(
		schema.FString,
		schema.SystemMessage("You are a helpful assistant. Answer questions concisely and clearly."),
		schema.MessagesPlaceholder("history", true), // optional history
		schema.UserMessage("{input}"),
	)

	// 3. Build and compile the chain
	chain, err := compose.NewChain[map[string]any, *schema.Message]().
		AppendChatTemplate(chatTemplate).
		AppendChatModel(model).
		Compile(ctx)
	if err != nil {
		fmt.Printf("Failed to compile chain: %v\n", err)
		os.Exit(1)
	}

	// 4. Chat loop
	var history []*schema.Message
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("========================================")
	fmt.Println("  Simple Chatbot powered by Eino")
	fmt.Println("  Type 'quit' or 'exit' to end")
	fmt.Println("  Type 'clear' to clear history")
	fmt.Println("========================================")
	fmt.Println()

	for {
		fmt.Print("You: ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())

		if input == "" {
			continue
		}

		// Handle special commands
		switch strings.ToLower(input) {
		case "quit", "exit":
			fmt.Println("Goodbye!")
			return
		case "clear":
			history = nil
			fmt.Println("[History cleared]")
			fmt.Println()
			continue
		}

		// Execute the chain
		result, err := chain.Invoke(ctx, map[string]any{
			"history": history,
			"input":   input,
		})
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		// Update history
		history = append(history,
			schema.UserMessage(input),
			result,
		)

		fmt.Printf("Bot: %s\n\n", result.Content)
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("Scanner error: %v\n", err)
	}
}
