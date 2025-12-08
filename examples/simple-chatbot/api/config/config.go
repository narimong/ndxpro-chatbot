package config

import (
	"os"
)

type Config struct {
	OpenAIAPIKey string
	Port         string
	Model        string

	// Neo4j settings
	Neo4jURI      string
	Neo4jUsername string
	Neo4jPassword string

	// Graph RAG settings
	GraphRAGEnabled   bool
	DefaultGraphDepth int
}

func Load() *Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "58080"
	}

	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-4o"
	}

	// Neo4j settings
	neo4jURI := os.Getenv("NEO4J_URI")
	if neo4jURI == "" {
		neo4jURI = "bolt://localhost:7687"
	}

	neo4jUsername := os.Getenv("NEO4J_USERNAME")
	if neo4jUsername == "" {
		neo4jUsername = "neo4j"
	}

	neo4jPassword := os.Getenv("NEO4J_PASSWORD")
	if neo4jPassword == "" {
		neo4jPassword = "ndxpro123!"
	}

	// Graph RAG settings
	graphRAGEnabled := os.Getenv("GRAPH_RAG_ENABLED") != "false"
	defaultGraphDepth := 2

	return &Config{
		OpenAIAPIKey:      os.Getenv("OPENAI_API_KEY"),
		Port:              port,
		Model:             model,
		Neo4jURI:          neo4jURI,
		Neo4jUsername:     neo4jUsername,
		Neo4jPassword:     neo4jPassword,
		GraphRAGEnabled:   graphRAGEnabled,
		DefaultGraphDepth: defaultGraphDepth,
	}
}
