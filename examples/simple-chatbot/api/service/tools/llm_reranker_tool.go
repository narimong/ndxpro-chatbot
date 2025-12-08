package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// LLMRerankerTool uses LLM to select the most relevant nodes from candidates
type LLMRerankerTool struct {
	chatModel model.ChatModel
}

// RerankerInput defines the input parameters for re-ranking
type RerankerInput struct {
	UserQuery      string          `json:"user_query"`
	Candidates     []NodeCandidate `json:"candidates"`
	TopN           int             `json:"top_n,omitempty"`
	ExpectedLabels []string        `json:"expected_labels,omitempty"` // LLM-inferred expected label types
}

// RerankerOutput represents the re-ranking result
type RerankerOutput struct {
	SelectedNodes []SelectedNode `json:"selected_nodes"`
	Reasoning     string         `json:"reasoning"`
}

// SelectedNode represents a selected node with confidence
type SelectedNode struct {
	UUID       string   `json:"uuid"`
	Labels     []string `json:"labels"`
	Name       string   `json:"name"`
	Confidence float64  `json:"confidence"`
}

// NewLLMRerankerTool creates a new LLM re-ranker tool
func NewLLMRerankerTool(chatModel model.ChatModel) *LLMRerankerTool {
	return &LLMRerankerTool{chatModel: chatModel}
}

// Info returns the tool information for LLM
func (t *LLMRerankerTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "llm_rerank_nodes",
		Desc: "Use LLM to select the most relevant node(s) from candidates based on user query. Essential for accurate node identification.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"user_query": {
				Type:     schema.String,
				Desc:     "Original user question",
				Required: true,
			},
			"candidates": {
				Type:     schema.Array,
				Desc:     "Array of candidate nodes with their properties",
				Required: true,
			},
			"top_n": {
				Type:     schema.Integer,
				Desc:     "Number of best nodes to select (default: 3)",
				Required: false,
			},
		}),
	}, nil
}

// InvokableRun executes the LLM re-ranking
func (t *LLMRerankerTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input RerankerInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", err
	}

	// Default TopN
	if input.TopN <= 0 {
		input.TopN = 3
	}

	// If no candidates, return empty result
	if len(input.Candidates) == 0 {
		output := RerankerOutput{
			SelectedNodes: []SelectedNode{},
			Reasoning:     "No candidates provided",
		}
		result, _ := json.MarshalIndent(output, "", "  ")
		return string(result), nil
	}

	// Build prompt for LLM (now includes expected labels context)
	prompt := t.buildRerankerPrompt(input.UserQuery, input.Candidates, input.TopN, input.ExpectedLabels)

	// Call LLM
	messages := []*schema.Message{
		schema.SystemMessage("You are a node selection expert for knowledge graphs. Select the most relevant nodes based on user intent."),
		schema.UserMessage(prompt),
	}

	response, err := t.chatModel.Generate(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("LLM generation failed: %w", err)
	}

	// Parse LLM response
	output, err := t.parseRerankerResponse(response.Content, input.Candidates, input.TopN)
	if err != nil {
		// Fallback: return top candidates by score
		output = t.fallbackSelection(input.Candidates, input.TopN)
	}

	result, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return "", err
	}

	return string(result), nil
}

// buildRerankerPrompt creates the prompt for LLM re-ranking
// Now includes expected labels for better context matching
func (t *LLMRerankerTool) buildRerankerPrompt(userQuery string, candidates []NodeCandidate, topN int, expectedLabels []string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("User Question: %s\n\n", userQuery))

	// Add expected labels context if available
	if len(expectedLabels) > 0 {
		sb.WriteString(fmt.Sprintf("Expected Entity Types: %s\n", strings.Join(expectedLabels, ", ")))
		sb.WriteString("(These are the entity types the user is likely looking for)\n\n")
	}

	sb.WriteString("Candidate Nodes:\n")

	for i, c := range candidates {
		propsJSON, _ := json.Marshal(c.Properties)
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i, strings.Join(c.Labels, ","), c.Name))
		sb.WriteString(fmt.Sprintf("   UUID: %s\n", c.UUID))
		sb.WriteString(fmt.Sprintf("   Properties: %s\n", string(propsJSON)))
		sb.WriteString(fmt.Sprintf("   Search Score: %.4f\n", c.Score))

		// Check if this candidate matches expected labels
		if len(expectedLabels) > 0 {
			matchesExpected := false
			for _, expected := range expectedLabels {
				for _, label := range c.Labels {
					if label == expected {
						matchesExpected = true
						break
					}
				}
				if matchesExpected {
					break
				}
			}
			if matchesExpected {
				sb.WriteString("   ★ Matches expected type\n")
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf(`Task:
1. Analyze which node(s) best match the user's intent
2. Consider exact name matches, label relevance, and property values
3. Prioritize nodes that match the expected entity types (if specified)
4. Select the top %d most relevant nodes

Return as JSON (no markdown):
{
  "selected_indices": [0, 2],
  "reasoning": "Brief explanation of why these nodes were selected"
}`, topN))

	return sb.String()
}

// parseRerankerResponse parses the LLM response to extract selected nodes
func (t *LLMRerankerTool) parseRerankerResponse(content string, candidates []NodeCandidate, topN int) (*RerankerOutput, error) {
	// Try to extract JSON from response
	content = strings.TrimSpace(content)

	// Remove markdown code blocks if present
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var parsed struct {
		SelectedIndices []int  `json:"selected_indices"`
		Reasoning       string `json:"reasoning"`
	}

	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	// Convert indices to selected nodes
	selectedNodes := make([]SelectedNode, 0, len(parsed.SelectedIndices))
	for i, idx := range parsed.SelectedIndices {
		if idx >= 0 && idx < len(candidates) {
			c := candidates[idx]
			// Assign confidence based on position (first is highest)
			confidence := 1.0 - float64(i)*0.1
			if confidence < 0.5 {
				confidence = 0.5
			}

			selectedNodes = append(selectedNodes, SelectedNode{
				UUID:       c.UUID,
				Labels:     c.Labels,
				Name:       c.Name,
				Confidence: confidence,
			})
		}
	}

	// Limit to topN
	if len(selectedNodes) > topN {
		selectedNodes = selectedNodes[:topN]
	}

	return &RerankerOutput{
		SelectedNodes: selectedNodes,
		Reasoning:     parsed.Reasoning,
	}, nil
}

// fallbackSelection returns top candidates by search score
func (t *LLMRerankerTool) fallbackSelection(candidates []NodeCandidate, topN int) *RerankerOutput {
	selectedNodes := make([]SelectedNode, 0, topN)

	for i := 0; i < len(candidates) && i < topN; i++ {
		c := candidates[i]
		selectedNodes = append(selectedNodes, SelectedNode{
			UUID:       c.UUID,
			Labels:     c.Labels,
			Name:       c.Name,
			Confidence: c.Score / 10.0, // Normalize score
		})
	}

	return &RerankerOutput{
		SelectedNodes: selectedNodes,
		Reasoning:     "Fallback: Selected top candidates by search score",
	}
}

// Ensure LLMRerankerTool implements the required interfaces
var (
	_ tool.BaseTool      = (*LLMRerankerTool)(nil)
	_ tool.InvokableTool = (*LLMRerankerTool)(nil)
)
