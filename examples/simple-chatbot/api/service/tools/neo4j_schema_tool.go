package tools

import (
	"context"
	"encoding/json"

	"simple-chatbot/api/service/db"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Neo4jSchemaDiscoveryTool discovers Neo4j database schema
type Neo4jSchemaDiscoveryTool struct {
	client *db.Neo4jClient
}

// SchemaDiscoveryInput defines the input parameters for schema discovery
type SchemaDiscoveryInput struct {
	IncludeProperties bool `json:"include_properties"`
	IncludeCounts     bool `json:"include_counts"`
}

// NewNeo4jSchemaDiscoveryTool creates a new schema discovery tool
func NewNeo4jSchemaDiscoveryTool(client *db.Neo4jClient) *Neo4jSchemaDiscoveryTool {
	return &Neo4jSchemaDiscoveryTool{client: client}
}

// Info returns the tool information for LLM
func (t *Neo4jSchemaDiscoveryTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "neo4j_schema_discovery",
		Desc: "Discover Neo4j database schema including node labels, relationship types, and their properties. Use this tool to understand the data structure before querying.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"include_properties": {
				Type:     schema.Boolean,
				Desc:     "Whether to include property keys for each label (default: false)",
				Required: false,
			},
			"include_counts": {
				Type:     schema.Boolean,
				Desc:     "Whether to include node/relationship counts (default: false)",
				Required: false,
			},
		}),
	}, nil
}

// InvokableRun executes the schema discovery
func (t *Neo4jSchemaDiscoveryTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input SchemaDiscoveryInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		// Use defaults if parsing fails
		input = SchemaDiscoveryInput{
			IncludeProperties: false,
			IncludeCounts:     false,
		}
	}

	schemaResult, err := t.client.GetSchema(ctx, input.IncludeProperties, input.IncludeCounts)
	if err != nil {
		return "", err
	}

	result, err := json.MarshalIndent(schemaResult, "", "  ")
	if err != nil {
		return "", err
	}

	return string(result), nil
}

// Ensure Neo4jSchemaDiscoveryTool implements the required interfaces
var (
	_ tool.BaseTool      = (*Neo4jSchemaDiscoveryTool)(nil)
	_ tool.InvokableTool = (*Neo4jSchemaDiscoveryTool)(nil)
)
