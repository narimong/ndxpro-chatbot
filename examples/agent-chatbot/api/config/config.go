package config

import (
	"os"
	"strconv"
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

	// Agent settings
	UseAgentMode        bool    // Enable ReAct agent mode
	MaxAgentIterations  int     // Maximum search iterations (default: 5)
	ConfidenceThreshold float64 // Threshold for sufficient results (default: 0.7)
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

	// Agent settings
	useAgentMode := os.Getenv("USE_AGENT_MODE") != "false"
	maxAgentIterations := 5
	if v := os.Getenv("MAX_AGENT_ITERATIONS"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			maxAgentIterations = parsed
		}
	}
	confidenceThreshold := 0.7
	if v := os.Getenv("CONFIDENCE_THRESHOLD"); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil && parsed > 0 && parsed <= 1 {
			confidenceThreshold = parsed
		}
	}

	return &Config{
		OpenAIAPIKey:        os.Getenv("OPENAI_API_KEY"),
		Port:                port,
		Model:               model,
		Neo4jURI:            neo4jURI,
		Neo4jUsername:       neo4jUsername,
		Neo4jPassword:       neo4jPassword,
		GraphRAGEnabled:     graphRAGEnabled,
		DefaultGraphDepth:   defaultGraphDepth,
		UseAgentMode:        useAgentMode,
		MaxAgentIterations:  maxAgentIterations,
		ConfidenceThreshold: confidenceThreshold,
	}
}
