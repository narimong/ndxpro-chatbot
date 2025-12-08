package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"agent-chatbot/api/service/db"
	serviceTools "agent-chatbot/api/service/tools"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// GraphSearchTool - Neo4j 그래프 검색 도구
type GraphSearchTool struct {
	neo4jClient  *db.Neo4jClient
	fullTextTool *serviceTools.Neo4jFullTextSearchTool
}

// GraphSearchInput represents the input parameters for graph search
type GraphSearchInput struct {
	Query     string `json:"query"`
	Strategy  string `json:"strategy,omitempty"`
	LabelHint string `json:"label_hint,omitempty"`
	TopK      int    `json:"top_k,omitempty"`
}

// GraphSearchOutput represents the search results
type GraphSearchOutput struct {
	Success    bool                       `json:"success"`
	TotalFound int                        `json:"total_found"`
	Nodes      []GraphSearchNodeResult    `json:"nodes"`
	Message    string                     `json:"message,omitempty"`
	Strategy   string                     `json:"strategy_used"`
}

// GraphSearchNodeResult represents a single node result
type GraphSearchNodeResult struct {
	UUID       string         `json:"uuid"`
	Name       string         `json:"name"`
	Labels     []string       `json:"labels"`
	Score      float64        `json:"score"`
	Properties map[string]any `json:"properties,omitempty"`
}

// NewGraphSearchTool creates a new graph search tool
func NewGraphSearchTool(neo4jClient *db.Neo4jClient) *GraphSearchTool {
	return &GraphSearchTool{
		neo4jClient:  neo4jClient,
		fullTextTool: serviceTools.NewNeo4jFullTextSearchTool(neo4jClient),
	}
}

// Info returns the tool information for LLM
func (t *GraphSearchTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "graph_search",
		Desc: `Graph Database에서 노드를 검색합니다.

## 사용 시점
- 사용자 질문에서 특정 엔티티(차량, 제조사, 점수 등)를 찾을 때
- 검색 결과가 부족하면 다른 전략을 시도하세요

## 전략 (strategy)
- exact: 정확한 키워드 매칭 (기본값)
- fuzzy: 유사어 포함 검색 (~1 편집거리)
- wildcard: 부분 일치 검색 (prefix*)
- label_filtered: 특정 라벨로 필터링

## 라벨 힌트 (label_hint)
Vehicle, CompetitorVehicle, Manufacturer, Engine, PerformanceTotalScore 등

## 예시
- {"query": "Tucson", "strategy": "exact"}
- {"query": "현대", "strategy": "fuzzy", "label_hint": "Manufacturer"}`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type:     schema.String,
				Desc:     "검색할 키워드 또는 이름",
				Required: true,
			},
			"strategy": {
				Type:     schema.String,
				Desc:     "검색 전략: exact, fuzzy, wildcard, label_filtered",
				Required: false,
				Enum:     []string{"exact", "fuzzy", "wildcard", "label_filtered"},
			},
			"label_hint": {
				Type:     schema.String,
				Desc:     "필터링할 노드 라벨 (예: Vehicle, Manufacturer)",
				Required: false,
			},
			"top_k": {
				Type:     schema.Integer,
				Desc:     "반환할 최대 결과 수 (기본: 5)",
				Required: false,
			},
		}),
	}, nil
}

// InvokableRun executes the search
func (t *GraphSearchTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input GraphSearchInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		log.Printf("[DEBUG] GraphSearchTool: parse error: %v", err)
		return t.errorResponse("인자 파싱 실패: " + err.Error())
	}

	// Set defaults
	if input.TopK <= 0 {
		input.TopK = 5
	}
	if input.Strategy == "" {
		input.Strategy = "exact"
	}

	// Debug logging for query tracking
	log.Printf("[DEBUG] GraphSearchTool: query=%q, strategy=%s, labelHint=%q, topK=%d",
		input.Query, input.Strategy, input.LabelHint, input.TopK)

	// Execute search based on strategy
	var candidates []serviceTools.NodeCandidate
	var err error

	switch input.Strategy {
	case "exact":
		candidates, err = t.fullTextTool.SearchWithFallback(ctx, input.Query, input.LabelHint, input.TopK)
	case "fuzzy":
		fuzzyQuery := input.Query + "~1"
		candidates, err = t.searchWithQuery(ctx, fuzzyQuery, input.LabelHint, input.TopK)
	case "wildcard":
		wildcardQuery := input.Query + "*"
		candidates, err = t.searchWithQuery(ctx, wildcardQuery, input.LabelHint, input.TopK)
	case "label_filtered":
		if input.LabelHint == "" {
			return t.errorResponse("label_filtered 전략은 label_hint가 필요합니다")
		}
		candidates, err = t.fullTextTool.SearchWithFallback(ctx, input.Query, input.LabelHint, input.TopK)
	default:
		candidates, err = t.fullTextTool.SearchWithFallback(ctx, input.Query, input.LabelHint, input.TopK)
	}

	if err != nil {
		log.Printf("[DEBUG] GraphSearchTool: search error: %v", err)
		return t.errorResponse(fmt.Sprintf("검색 실패: %v", err))
	}

	// Log search results
	log.Printf("[DEBUG] GraphSearchTool: found %d candidates for query=%q", len(candidates), input.Query)
	for i, c := range candidates {
		if i < 3 { // Log first 3 results
			log.Printf("[DEBUG] GraphSearchTool: result[%d] name=%q, labels=%v, score=%.3f",
				i, c.Name, c.Labels, c.Score)
		}
	}

	// Build output
	output := GraphSearchOutput{
		Success:    len(candidates) > 0,
		TotalFound: len(candidates),
		Nodes:      make([]GraphSearchNodeResult, 0, len(candidates)),
		Strategy:   input.Strategy,
	}

	if len(candidates) == 0 {
		output.Message = fmt.Sprintf("'%s' 검색 결과 없음. 다른 전략이나 키워드를 시도해보세요.", input.Query)
	} else {
		output.Message = fmt.Sprintf("%d개 노드 발견", len(candidates))
	}

	for _, c := range candidates {
		node := GraphSearchNodeResult{
			UUID:   c.UUID,
			Name:   c.Name,
			Labels: c.Labels,
			Score:  c.Score,
		}
		// Include relevant properties (exclude internal ones)
		if len(c.Properties) > 0 {
			node.Properties = make(map[string]any)
			for k, v := range c.Properties {
				if !strings.HasPrefix(k, "_") && k != "uuid" && k != "embedding" {
					node.Properties[k] = v
				}
			}
		}
		output.Nodes = append(output.Nodes, node)
	}

	return t.jsonResponse(output)
}

func (t *GraphSearchTool) searchWithQuery(ctx context.Context, query, labelHint string, topK int) ([]serviceTools.NodeCandidate, error) {
	input := serviceTools.FullTextSearchInput{
		Query:       query,
		TopK:        topK,
		LabelFilter: labelHint,
	}

	inputJSON, _ := json.Marshal(input)
	resultJSON, err := t.fullTextTool.InvokableRun(ctx, string(inputJSON))
	if err != nil {
		return nil, err
	}

	var result serviceTools.FullTextSearchResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, err
	}

	return result.Candidates, nil
}

func (t *GraphSearchTool) jsonResponse(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (t *GraphSearchTool) errorResponse(msg string) (string, error) {
	output := GraphSearchOutput{
		Success: false,
		Message: msg,
	}
	return t.jsonResponse(output)
}
