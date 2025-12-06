package config

import (
	"os"
)

type Config struct {
	OpenAIAPIKey string
	Port         string
	Model        string
}

func Load() *Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "58080"
	}

	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-4o-mini"
	}

	return &Config{
		OpenAIAPIKey: os.Getenv("OPENAI_API_KEY"),
		Port:         port,
		Model:        model,
	}
}
