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

// QueryExpandTool - 쿼리 확장/변형 도구
type QueryExpandTool struct {
	chatModel model.ChatModel
}

// QueryExpandInput represents the input for query expansion
type QueryExpandInput struct {
	OriginalQuery   string   `json:"original_query"`
	FailedQueries   []string `json:"failed_queries,omitempty"`
	ExpansionCount  int      `json:"expansion_count,omitempty"`
	Context         string   `json:"context,omitempty"`
}

// QueryExpandOutput represents the expanded queries
type QueryExpandOutput struct {
	Success        bool     `json:"success"`
	ExpandedQueries []string `json:"expanded_queries"`
	Reasoning      string   `json:"reasoning"`
	Message        string   `json:"message,omitempty"`
}

// NewQueryExpandTool creates a new query expansion tool
func NewQueryExpandTool(chatModel model.ChatModel) *QueryExpandTool {
	return &QueryExpandTool{
		chatModel: chatModel,
	}
}

// Info returns the tool information for LLM
func (t *QueryExpandTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "query_expand",
		Desc: `검색 쿼리를 다양한 형태로 확장합니다.

## 사용 시점
- graph_search 결과가 없거나 부족할 때
- 원본 쿼리로 원하는 정보를 찾지 못했을 때

## 기능
- 동의어/유사어 생성
- 다른 관점의 쿼리 생성
- 한국어↔영어 변환
- 약어/풀네임 변환

## 예시
입력: "현기차 투싼"
출력: ["Tucson", "투싼", "현대 투싼", "Hyundai Tucson"]

입력: "연비 좋은 차"
출력: ["FuelEfficiency", "연료효율", "경제성", "리터당 주행거리"]`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"original_query": {
				Type:     schema.String,
				Desc:     "확장할 원본 검색 쿼리",
				Required: true,
			},
			"failed_queries": {
				Type:     schema.Array,
				ElemInfo: &schema.ParameterInfo{Type: schema.String},
				Desc:     "이미 시도했지만 실패한 쿼리들 (중복 방지)",
				Required: false,
			},
			"expansion_count": {
				Type:     schema.Integer,
				Desc:     "생성할 변형 쿼리 수 (기본: 3)",
				Required: false,
			},
			"context": {
				Type:     schema.String,
				Desc:     "추가 컨텍스트 (예: 찾고자 하는 정보 유형)",
				Required: false,
			},
		}),
	}, nil
}

// InvokableRun executes the query expansion
func (t *QueryExpandTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input QueryExpandInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return t.errorResponse("인자 파싱 실패: " + err.Error())
	}

	// Set defaults
	if input.ExpansionCount <= 0 {
		input.ExpansionCount = 3
	}
	if input.ExpansionCount > 5 {
		input.ExpansionCount = 5
	}

	// Build prompt for expansion
	prompt := t.buildExpansionPrompt(input)

	// Call LLM
	messages := []*schema.Message{
		schema.SystemMessage(`당신은 검색 쿼리 확장 전문가입니다.
사용자의 쿼리를 다양한 형태로 변형하여 검색 성공률을 높이는 것이 목표입니다.

규칙:
1. 동의어, 유사어, 관련어를 활용
2. 한국어↔영어 변환 고려
3. 약어와 풀네임 모두 고려
4. 이미 실패한 쿼리는 제외
5. 각 쿼리는 간결하게 (1-3 단어)

JSON 형식으로 응답:
{
  "expanded_queries": ["쿼리1", "쿼리2", ...],
  "reasoning": "확장 이유 설명"
}`),
		schema.UserMessage(prompt),
	}

	response, err := t.chatModel.Generate(ctx, messages)
	if err != nil {
		return t.errorResponse(fmt.Sprintf("LLM 호출 실패: %v", err))
	}

	// Parse LLM response
	output, err := t.parseExpansionResponse(response.Content, input.FailedQueries)
	if err != nil {
		// Fallback: simple expansion
		output = t.simpleExpansion(input)
	}

	output.Success = len(output.ExpandedQueries) > 0
	if !output.Success {
		output.Message = "추가 쿼리 확장에 실패했습니다"
	} else {
		output.Message = fmt.Sprintf("%d개의 확장 쿼리 생성", len(output.ExpandedQueries))
	}

	return t.jsonResponse(output)
}

func (t *QueryExpandTool) buildExpansionPrompt(input QueryExpandInput) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("원본 쿼리: %s\n", input.OriginalQuery))
	sb.WriteString(fmt.Sprintf("생성할 변형 수: %d\n", input.ExpansionCount))

	if len(input.FailedQueries) > 0 {
		sb.WriteString(fmt.Sprintf("\n이미 시도한 쿼리 (제외):\n- %s\n", strings.Join(input.FailedQueries, "\n- ")))
	}

	if input.Context != "" {
		sb.WriteString(fmt.Sprintf("\n추가 컨텍스트: %s\n", input.Context))
	}

	sb.WriteString("\n새로운 검색 쿼리를 생성해주세요.")

	return sb.String()
}

func (t *QueryExpandTool) parseExpansionResponse(content string, failedQueries []string) (*QueryExpandOutput, error) {
	// Try to extract JSON from response
	content = strings.TrimSpace(content)

	// Handle markdown code blocks
	if strings.HasPrefix(content, "```") {
		lines := strings.Split(content, "\n")
		var jsonLines []string
		inBlock := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				jsonLines = append(jsonLines, line)
			}
		}
		content = strings.Join(jsonLines, "\n")
	}

	var output QueryExpandOutput
	if err := json.Unmarshal([]byte(content), &output); err != nil {
		return nil, err
	}

	// Filter out failed queries
	failedSet := make(map[string]bool)
	for _, q := range failedQueries {
		failedSet[strings.ToLower(q)] = true
	}

	filtered := make([]string, 0)
	for _, q := range output.ExpandedQueries {
		if !failedSet[strings.ToLower(q)] {
			filtered = append(filtered, q)
		}
	}
	output.ExpandedQueries = filtered

	return &output, nil
}

func (t *QueryExpandTool) simpleExpansion(input QueryExpandInput) *QueryExpandOutput {
	query := input.OriginalQuery
	expanded := make([]string, 0)

	// Simple transformations
	// 1. Remove common prefixes/suffixes
	cleaned := strings.TrimSpace(query)
	if cleaned != query {
		expanded = append(expanded, cleaned)
	}

	// 2. Split compound words
	words := strings.Fields(query)
	if len(words) > 1 {
		for _, w := range words {
			if len(w) >= 2 {
				expanded = append(expanded, w)
			}
		}
	}

	// 3. Add wildcard version
	if !strings.HasSuffix(query, "*") {
		expanded = append(expanded, query+"*")
	}

	// Filter out failed queries and original
	failedSet := make(map[string]bool)
	failedSet[strings.ToLower(query)] = true
	for _, q := range input.FailedQueries {
		failedSet[strings.ToLower(q)] = true
	}

	filtered := make([]string, 0)
	for _, q := range expanded {
		if !failedSet[strings.ToLower(q)] && len(filtered) < input.ExpansionCount {
			filtered = append(filtered, q)
		}
	}

	return &QueryExpandOutput{
		ExpandedQueries: filtered,
		Reasoning:       "기본 확장 규칙 적용",
	}
}

func (t *QueryExpandTool) jsonResponse(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (t *QueryExpandTool) errorResponse(msg string) (string, error) {
	output := QueryExpandOutput{
		Success: false,
		Message: msg,
	}
	return t.jsonResponse(output)
}
