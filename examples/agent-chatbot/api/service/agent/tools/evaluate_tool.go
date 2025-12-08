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

// ResultEvaluateTool - 검색 결과 평가 도구
type ResultEvaluateTool struct {
	chatModel model.ChatModel
}

// EvaluateInput represents the input for result evaluation
type EvaluateInput struct {
	Query         string `json:"query"`
	CollectedInfo string `json:"collected_info"`
}

// EvaluateOutput represents the evaluation result
type EvaluateOutput struct {
	IsSufficient    bool     `json:"is_sufficient"`
	Confidence      float64  `json:"confidence"`
	MissingInfo     []string `json:"missing_info,omitempty"`
	SuggestedAction string   `json:"suggested_action,omitempty"`
	Reasoning       string   `json:"reasoning"`
	CanAnswer       bool     `json:"can_answer"`
}

// NewResultEvaluateTool creates a new result evaluation tool
func NewResultEvaluateTool(chatModel model.ChatModel) *ResultEvaluateTool {
	return &ResultEvaluateTool{
		chatModel: chatModel,
	}
}

// Info returns the tool information for LLM
func (t *ResultEvaluateTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "evaluate_results",
		Desc: `수집된 정보가 사용자 질문에 답변하기 충분한지 평가합니다.

## 사용 시점
- 검색/탐색 후 수집된 정보를 평가할 때
- 추가 검색이 필요한지 결정할 때

## 평가 기준
- 질문의 핵심 요소가 모두 포함되어 있는가?
- 정보가 정확하고 구체적인가?
- 답변을 구성하기에 충분한가?

## 반환 값
- is_sufficient: 충분히 답변 가능 여부
- confidence: 신뢰도 (0.0-1.0)
- missing_info: 부족한 정보 목록
- suggested_action: 다음 행동 제안

## 예시
신뢰도 0.8 이상이면 답변 가능, 미만이면 추가 검색 필요`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type:     schema.String,
				Desc:     "사용자의 원본 질문",
				Required: true,
			},
			"collected_info": {
				Type:     schema.String,
				Desc:     "지금까지 수집된 모든 정보 (검색 결과, 탐색 결과 등)",
				Required: true,
			},
		}),
	}, nil
}

// InvokableRun executes the evaluation
func (t *ResultEvaluateTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input EvaluateInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return t.errorResponse("인자 파싱 실패: " + err.Error())
	}

	if input.Query == "" {
		return t.errorResponse("query가 필요합니다")
	}

	// If no collected info, definitely insufficient
	if strings.TrimSpace(input.CollectedInfo) == "" {
		return t.jsonResponse(EvaluateOutput{
			IsSufficient:    false,
			Confidence:      0.0,
			MissingInfo:     []string{"검색 결과 없음"},
			SuggestedAction: "graph_search로 다른 키워드 검색 또는 query_expand로 쿼리 확장",
			Reasoning:       "수집된 정보가 없습니다",
			CanAnswer:       false,
		})
	}

	// Use LLM to evaluate
	prompt := t.buildEvaluationPrompt(input)
	messages := []*schema.Message{
		schema.SystemMessage(`당신은 정보 충분성 평가 전문가입니다.
수집된 정보가 사용자 질문에 답변하기에 충분한지 평가합니다.

평가 기준:
1. 질문의 핵심 요소가 모두 포함되어 있는가?
2. 정보가 구체적이고 정확한가?
3. 답변을 구성하기에 충분한가?

JSON 형식으로 응답하세요:
{
  "is_sufficient": true/false,
  "confidence": 0.0-1.0,
  "missing_info": ["부족한 정보1", "부족한 정보2"],
  "suggested_action": "다음에 해야 할 행동",
  "reasoning": "평가 이유"
}

suggested_action 예시:
- "충분한 정보가 모였습니다. 최종 답변을 생성하세요."
- "graph_search로 '키워드' 검색"
- "explore_relations로 노드 탐색"
- "query_expand로 쿼리 확장"`),
		schema.UserMessage(prompt),
	}

	response, err := t.chatModel.Generate(ctx, messages)
	if err != nil {
		// Fallback to simple evaluation
		return t.simpleEvaluation(input)
	}

	// Parse LLM response
	output, err := t.parseEvaluationResponse(response.Content)
	if err != nil {
		return t.simpleEvaluation(input)
	}

	// Set can_answer based on confidence
	output.CanAnswer = output.Confidence >= 0.7

	return t.jsonResponse(output)
}

func (t *ResultEvaluateTool) buildEvaluationPrompt(input EvaluateInput) string {
	var sb strings.Builder

	sb.WriteString("## 사용자 질문\n")
	sb.WriteString(input.Query)
	sb.WriteString("\n\n## 수집된 정보\n")
	sb.WriteString(input.CollectedInfo)
	sb.WriteString("\n\n위 정보가 사용자 질문에 답변하기에 충분한지 평가해주세요.")

	return sb.String()
}

func (t *ResultEvaluateTool) parseEvaluationResponse(content string) (*EvaluateOutput, error) {
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

	var output EvaluateOutput
	if err := json.Unmarshal([]byte(content), &output); err != nil {
		return nil, err
	}

	// Normalize confidence
	if output.Confidence < 0 {
		output.Confidence = 0
	}
	if output.Confidence > 1 {
		output.Confidence = 1
	}

	return &output, nil
}

func (t *ResultEvaluateTool) simpleEvaluation(input EvaluateInput) (string, error) {
	// Simple heuristic-based evaluation
	infoLen := len(input.CollectedInfo)
	queryWords := strings.Fields(strings.ToLower(input.Query))

	// Check if query keywords appear in collected info
	matchCount := 0
	collectedLower := strings.ToLower(input.CollectedInfo)
	for _, word := range queryWords {
		if len(word) >= 2 && strings.Contains(collectedLower, word) {
			matchCount++
		}
	}

	confidence := 0.0
	if len(queryWords) > 0 {
		confidence = float64(matchCount) / float64(len(queryWords))
	}

	// Adjust confidence based on info length
	if infoLen < 100 {
		confidence *= 0.5
	} else if infoLen < 300 {
		confidence *= 0.7
	} else if infoLen > 1000 {
		confidence = min(confidence*1.2, 1.0)
	}

	output := EvaluateOutput{
		IsSufficient: confidence >= 0.7,
		Confidence:   confidence,
		Reasoning:    fmt.Sprintf("키워드 매칭률: %.0f%%, 정보 길이: %d자", confidence*100, infoLen),
		CanAnswer:    confidence >= 0.7,
	}

	if !output.IsSufficient {
		output.MissingInfo = []string{"추가 정보 필요"}
		output.SuggestedAction = "graph_search 또는 explore_relations로 추가 검색"
	} else {
		output.SuggestedAction = "충분한 정보가 모였습니다. 최종 답변을 생성하세요."
	}

	return t.jsonResponse(output)
}

func (t *ResultEvaluateTool) jsonResponse(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (t *ResultEvaluateTool) errorResponse(msg string) (string, error) {
	output := EvaluateOutput{
		IsSufficient: false,
		Confidence:   0,
		Reasoning:    msg,
		CanAnswer:    false,
	}
	return t.jsonResponse(output)
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
