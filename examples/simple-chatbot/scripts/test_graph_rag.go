// +build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/retriever"

	"simple-chatbot/api/service"
	"simple-chatbot/api/service/db"
)

func main() {
	ctx := context.Background()

	// Get API key
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY environment variable is required")
	}

	// Connect to Neo4j
	fmt.Println("Connecting to Neo4j...")
	neo4jClient, err := db.NewNeo4jClient(ctx, "bolt://localhost:7687", "neo4j", "ndxpro123!")
	if err != nil {
		log.Fatalf("Failed to connect to Neo4j: %v", err)
	}
	defer neo4jClient.Close(ctx)
	fmt.Println("Connected to Neo4j!")

	// Create ChatModel
	fmt.Println("Creating ChatModel...")
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey: apiKey,
		Model:  "gpt-4o",
	})
	if err != nil {
		log.Fatalf("Failed to create ChatModel: %v", err)
	}

	// Create GraphRAGRetriever
	fmt.Println("Creating GraphRAGRetriever...")
	graphRAG, err := service.NewGraphRAGRetriever(&service.GraphRAGConfig{
		Neo4jClient:     neo4jClient,
		ChatModel:       chatModel,
		DefaultTopK:     3,
		MaxGraphDepth:   2,
		EnableReranking: true,
		DebugEmitter: func(event string, data interface{}) {
			fmt.Printf("[DEBUG] %s: %+v\n", event, data)
		},
	})
	if err != nil {
		log.Fatalf("Failed to create GraphRAGRetriever: %v", err)
	}

	// Test queries
	testQueries := []string{
		"NE2",
		"NE2 모델의 엔진 사양",
		"Vehicle",
	}

	separator := "============================================================"
	for _, query := range testQueries {
		fmt.Printf("\n%s\n", separator)
		fmt.Printf("Query: %s\n", query)
		fmt.Println(separator)

		docs, err := graphRAG.Retrieve(ctx, query, retriever.WithTopK(3))
		if err != nil {
			log.Printf("Error retrieving for query '%s': %v", query, err)
			continue
		}

		fmt.Printf("\nRetrieved %d documents:\n", len(docs))
		for i, doc := range docs {
			fmt.Printf("\n--- Document %d ---\n", i+1)
			fmt.Printf("ID: %s\n", doc.ID)
			fmt.Printf("Content:\n%s\n", doc.Content)
			if score, ok := doc.MetaData["confidence"].(float64); ok {
				fmt.Printf("Confidence: %.4f\n", score)
			}
		}
	}

	fmt.Println("\n✅ GraphRAG test completed!")
}
