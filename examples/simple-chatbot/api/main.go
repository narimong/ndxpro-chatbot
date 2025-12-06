package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"simple-chatbot/api/config"
	"simple-chatbot/api/server"
	"simple-chatbot/api/service"
)

func main() {
	cfg := config.Load()

	if cfg.OpenAIAPIKey == "" {
		fmt.Println("Error: OPENAI_API_KEY environment variable is not set")
		fmt.Println("Usage: OPENAI_API_KEY=your-key go run ./api")
		os.Exit(1)
	}

	ctx := context.Background()

	sessionStore := service.NewSessionStore()

	chatService, err := service.NewChatService(ctx, cfg, sessionStore)
	if err != nil {
		log.Fatalf("Failed to create chat service: %v", err)
	}

	srv := server.New(sessionStore, chatService)

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		fmt.Println("\nShutting down server...")
		os.Exit(0)
	}()

	fmt.Println("========================================")
	fmt.Println("  Eino Chatbot API Server")
	fmt.Printf("  Running on http://localhost:%s\n", cfg.Port)
	fmt.Println("========================================")
	fmt.Println()
	fmt.Println("Endpoints:")
	fmt.Println("  POST   /api/v1/sessions         - Create session")
	fmt.Println("  GET    /api/v1/sessions         - List sessions")
	fmt.Println("  GET    /api/v1/sessions/:id     - Get session")
	fmt.Println("  DELETE /api/v1/sessions/:id     - Delete session")
	fmt.Println("  POST   /api/v1/sessions/:id/clear - Clear history")
	fmt.Printf("  WS     /api/v1/chat/:session_id - WebSocket chat\n")
	fmt.Println()

	if err := srv.Run(":" + cfg.Port); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
