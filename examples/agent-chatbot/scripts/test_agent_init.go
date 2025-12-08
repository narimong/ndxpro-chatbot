//go:build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"agent-chatbot/api/config"
	"agent-chatbot/api/service/agent"
	"agent-chatbot/api/service/db"

	"github.com/cloudwego/eino-ext/components/model/openai"
)

func main() {
	ctx := context.Background()

	// Load config
	cfg := config.Load()

	if cfg.OpenAIAPIKey == "" {
		log.Fatal("OPENAI_API_KEY not set")
	}

	fmt.Println("=== Agent-Chatbot Initialization Test ===")
	fmt.Printf("Neo4j URI: %s\n", cfg.Neo4jURI)
	fmt.Printf("Model: %s\n", cfg.Model)
	fmt.Printf("Agent Mode: %v\n", cfg.UseAgentMode)
	fmt.Printf("Max Iterations: %d\n", cfg.MaxAgentIterations)
	fmt.Printf("Confidence Threshold: %.2f\n", cfg.ConfidenceThreshold)

	// Initialize ChatModel
	fmt.Println("\n1. Initializing ChatModel...")
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey: cfg.OpenAIAPIKey,
		Model:  cfg.Model,
	})
	if err != nil {
		log.Fatalf("Failed to create chat model: %v", err)
	}
	fmt.Println("   ✓ ChatModel initialized")

	// Initialize Neo4j
	fmt.Println("\n2. Connecting to Neo4j...")
	neo4jClient, err := db.NewNeo4jClient(ctx, cfg.Neo4jURI, cfg.Neo4jUsername, cfg.Neo4jPassword)
	if err != nil {
		log.Fatalf("Failed to connect to Neo4j: %v", err)
	}
	defer neo4jClient.Close(ctx)
	fmt.Println("   ✓ Neo4j connected")

	// Initialize Agent
	fmt.Println("\n3. Creating GraphRAGAgent...")
	graphAgent, err := agent.NewGraphRAGAgent(ctx, &agent.AgentConfig{
		Neo4jClient:         neo4jClient,
		ChatModel:           chatModel,
		MaxIterations:       cfg.MaxAgentIterations,
		ConfidenceThreshold: cfg.ConfidenceThreshold,
		EnableDebug:         true,
		DebugEmitter: func(eventName string, payload interface{}) {
			fmt.Printf("   [DEBUG] %s: %v\n", eventName, payload)
		},
	})
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}
	fmt.Println("   ✓ GraphRAGAgent created")

	// Check tools
	tools := graphAgent.GetTools()
	fmt.Printf("\n4. Available Tools (%d):\n", len(tools))
	for i, t := range tools {
		info, _ := t.Info(ctx)
		fmt.Printf("   %d. %s\n", i+1, info.Name)
	}

	// Check config
	agentConfig := graphAgent.GetConfig()
	fmt.Printf("\n5. Agent Configuration:\n")
	fmt.Printf("   - Max Iterations: %d\n", agentConfig.MaxIterations)
	fmt.Printf("   - Confidence Threshold: %.2f\n", agentConfig.ConfidenceThreshold)
	fmt.Printf("   - Debug Enabled: %v\n", agentConfig.EnableDebug)

	fmt.Println("\n=== All Tests Passed! ===")
	os.Exit(0)
}
