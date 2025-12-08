package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-chatbot/api/service/agent/tools"
	"agent-chatbot/api/service/db"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	NodeKeyModel       = "model"
	NodeKeyTools       = "tools"
	NodeKeyFinalAnswer = "final_answer"
	NodeKeyDirectReturn = "direct_return"
)

// DebugEmitter is a function type for emitting debug events
type DebugEmitter func(eventName string, payload interface{})

// AgentConfig holds the configuration for GraphRAGAgent
type AgentConfig struct {
	Neo4jClient         *db.Neo4jClient
	ChatModel           model.ChatModel
	MaxIterations       int
	ConfidenceThreshold float64
	EnableDebug         bool
	DebugEmitter        DebugEmitter
}

// GraphRAGAgent implements a ReAct-style agent for Graph RAG
type GraphRAGAgent struct {
	graph      *compose.Graph[[]*schema.Message, *schema.Message]
	runnable   compose.Runnable[[]*schema.Message, *schema.Message]
	tools      []tool.BaseTool
	boundModel model.ToolCallingChatModel
	config     *AgentConfig
}

// NewGraphRAGAgent creates a new GraphRAG ReAct agent
func NewGraphRAGAgent(ctx context.Context, config *AgentConfig) (*GraphRAGAgent, error) {
	if config.Neo4jClient == nil {
		return nil, fmt.Errorf("neo4j client is required")
	}
	if config.ChatModel == nil {
		return nil, fmt.Errorf("chat model is required")
	}

	// Set defaults
	if config.MaxIterations <= 0 {
		config.MaxIterations = 5
	}
	if config.ConfidenceThreshold <= 0 {
		config.ConfidenceThreshold = 0.7
	}

	// Initialize tools
	agentTools := []tool.BaseTool{
		tools.NewGraphSearchTool(config.Neo4jClient),
		tools.NewQueryExpandTool(config.ChatModel),
		tools.NewRelationExploreTool(config.Neo4jClient),
		tools.NewResultEvaluateTool(config.ChatModel),
	}

	// Extract tool infos
	toolInfos := make([]*schema.ToolInfo, 0, len(agentTools))
	for _, t := range agentTools {
		info, err := t.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get tool info: %w", err)
		}
		toolInfos = append(toolInfos, info)
	}

	// Check if model supports tool calling
	toolCallingModel, ok := config.ChatModel.(model.ToolCallingChatModel)
	if !ok {
		return nil, fmt.Errorf("chat model does not support tool calling")
	}

	// Bind tools to model
	boundModel, err := toolCallingModel.WithTools(toolInfos)
	if err != nil {
		return nil, fmt.Errorf("failed to bind tools to model: %w", err)
	}

	agent := &GraphRAGAgent{
		tools:      agentTools,
		boundModel: boundModel,
		config:     config,
	}

	// Build graph
	if err := agent.buildGraph(ctx); err != nil {
		return nil, fmt.Errorf("failed to build graph: %w", err)
	}

	return agent, nil
}

// buildGraph constructs the ReAct graph
func (a *GraphRAGAgent) buildGraph(ctx context.Context) error {
	// Create graph with local state
	graph := compose.NewGraph[[]*schema.Message, *schema.Message](
		compose.WithGenLocalState(func(ctx context.Context) *AgentState {
			return NewAgentState(a.config.MaxIterations)
		}),
	)

	// ============================================
	// Node 1: ChatModel (decides tool calls or answer)
	// ============================================
	modelPreHandle := func(ctx context.Context, input []*schema.Message, state *AgentState) ([]*schema.Message, error) {
		// Store original query on first iteration
		if state.Iteration == 0 && len(input) > 0 {
			for _, msg := range input {
				if msg.Role == schema.User {
					state.OriginalQuery = msg.Content
					break
				}
			}
		}

		state.Messages = append(state.Messages, input...)
		state.Iteration++

		// Debug event
		a.emitDebug("agent:iteration", map[string]any{
			"iteration":     state.Iteration,
			"max_iterations": state.MaxIterations,
			"collected_docs": len(state.SearchContext.CollectedDocs),
		})

		// Build system prompt with current state
		systemPrompt := a.buildSystemPrompt(state)

		// Return system prompt + accumulated messages
		result := make([]*schema.Message, 0, len(state.Messages)+1)
		result = append(result, systemPrompt)
		result = append(result, state.Messages...)

		return result, nil
	}

	if err := graph.AddChatModelNode(NodeKeyModel, a.boundModel,
		compose.WithStatePreHandler(modelPreHandle),
		compose.WithNodeName("ChatModel"),
	); err != nil {
		return err
	}

	// ============================================
	// Node 2: Tools (execute tool calls)
	// ============================================
	toolsNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
		Tools: a.tools,
	})
	if err != nil {
		return err
	}

	toolsPreHandle := func(ctx context.Context, input *schema.Message, state *AgentState) (*schema.Message, error) {
		if input != nil {
			state.Messages = append(state.Messages, input)

			// Debug: log tool calls
			if len(input.ToolCalls) > 0 {
				for _, tc := range input.ToolCalls {
					a.emitDebug("agent:tool_call", map[string]any{
						"tool_name": tc.Function.Name,
						"arguments": tc.Function.Arguments,
					})
				}
			}
		}
		return input, nil
	}

	toolsPostHandle := func(ctx context.Context, output []*schema.Message, state *AgentState) ([]*schema.Message, error) {
		// Process tool results and update state
		for _, msg := range output {
			state.Messages = append(state.Messages, msg)

			// Update search context based on tool results
			a.updateStateFromToolResult(state, msg)
		}
		return output, nil
	}

	if err := graph.AddToolsNode(NodeKeyTools, toolsNode,
		compose.WithStatePreHandler(toolsPreHandle),
		compose.WithStatePostHandler(toolsPostHandle),
		compose.WithNodeName("Tools"),
	); err != nil {
		return err
	}

	// ============================================
	// Node 3: FinalAnswer (generate final response)
	// ============================================
	// This node receives a single message from model node (when no tool calls)
	// or from tools -> model loop when done
	finalAnswerFn := func(ctx context.Context, input *schema.Message) (*schema.Message, error) {
		// If we received a valid assistant message without tool calls, return it
		if input != nil && input.Role == schema.Assistant && len(input.ToolCalls) == 0 {
			return input, nil
		}

		// Otherwise, generate final answer from accumulated messages
		var messages []*schema.Message
		err := compose.ProcessState[*AgentState](ctx, func(ctx context.Context, state *AgentState) error {
			messages = state.Messages
			return nil
		})
		if err != nil || len(messages) == 0 {
			// Fallback: return input or generate new
			if input != nil {
				return input, nil
			}
			return schema.AssistantMessage("죄송합니다, 요청하신 정보를 찾을 수 없었습니다.", nil), nil
		}

		return a.generateFinalAnswer(ctx, messages)
	}

	if err := graph.AddLambdaNode(NodeKeyFinalAnswer,
		compose.InvokableLambda(finalAnswerFn),
		compose.WithNodeName("FinalAnswer"),
	); err != nil {
		return err
	}

	// ============================================
	// Edges and Branches
	// ============================================

	// START -> model
	if err := graph.AddEdge(compose.START, NodeKeyModel); err != nil {
		return err
	}

	// Branch after model: tools or final_answer
	modelBranch := func(ctx context.Context, sr *schema.StreamReader[*schema.Message]) (string, error) {
		// Consume stream to get the message
		defer sr.Close()

		var fullMsg *schema.Message
		for {
			chunk, err := sr.Recv()
			if err != nil {
				break
			}
			if fullMsg == nil {
				fullMsg = chunk
			} else {
				// Merge chunks
				fullMsg.Content += chunk.Content
				if len(chunk.ToolCalls) > 0 {
					fullMsg.ToolCalls = append(fullMsg.ToolCalls, chunk.ToolCalls...)
				}
			}
		}

		if fullMsg != nil && len(fullMsg.ToolCalls) > 0 {
			return NodeKeyTools, nil
		}
		return NodeKeyFinalAnswer, nil
	}

	if err := graph.AddBranch(NodeKeyModel, compose.NewStreamGraphBranch(
		modelBranch,
		map[string]bool{NodeKeyTools: true, NodeKeyFinalAnswer: true},
	)); err != nil {
		return err
	}

	// Branch after tools: always go back to model
	// The model will decide whether to continue with more tools or provide final answer
	// This ensures type compatibility (tools output []*Message, model input []*Message)
	toolsBranch := func(ctx context.Context, sr *schema.StreamReader[[]*schema.Message]) (string, error) {
		sr.Close()

		// Check if we should stop and set a flag for model to know
		err := compose.ProcessState[*AgentState](ctx, func(ctx context.Context, state *AgentState) error {
			// Check stop conditions - mark state for model to generate final answer
			if state.ShouldStop {
				a.emitDebug("agent:stop_reason", map[string]any{"reason": "explicit_stop"})
				return nil
			}

			if state.Iteration >= state.MaxIterations {
				a.emitDebug("agent:stop_reason", map[string]any{"reason": "max_iterations"})
				state.ShouldStop = true
				return nil
			}

			if state.EvaluationResult != nil && state.EvaluationResult.IsSufficient {
				a.emitDebug("agent:stop_reason", map[string]any{
					"reason":     "sufficient_info",
					"confidence": state.EvaluationResult.Confidence,
				})
				state.ShouldStop = true
				return nil
			}

			return nil
		})

		if err != nil {
			return "", err
		}

		// Always go back to model - model will decide whether to call tools or generate answer
		return NodeKeyModel, nil
	}

	if err := graph.AddBranch(NodeKeyTools, compose.NewStreamGraphBranch(
		toolsBranch,
		map[string]bool{NodeKeyModel: true},
	)); err != nil {
		return err
	}

	// final_answer -> END
	if err := graph.AddEdge(NodeKeyFinalAnswer, compose.END); err != nil {
		return err
	}

	// Compile graph
	compileOpts := []compose.GraphCompileOption{
		compose.WithMaxRunSteps(a.config.MaxIterations * 3), // Allow enough steps for iterations
		compose.WithNodeTriggerMode(compose.AnyPredecessor),
		compose.WithGraphName("GraphRAGAgent"),
	}

	runnable, err := graph.Compile(ctx, compileOpts...)
	if err != nil {
		return err
	}

	a.graph = graph
	a.runnable = runnable

	return nil
}

// buildSystemPrompt creates a dynamic system prompt based on current state
func (a *GraphRAGAgent) buildSystemPrompt(state *AgentState) *schema.Message {
	var prompt strings.Builder

	prompt.WriteString(`당신은 Graph Database에서 정보를 찾는 전문가 에이전트입니다.

## 목표
사용자 질문에 답하기 위해 **수단과 방법을 가리지 않고** 정보를 찾아야 합니다.

## 사용 가능한 도구
1. **graph_search**: 키워드로 노드 검색
   - 첫 번째로 시도해야 할 도구
   - 전략: exact, fuzzy, wildcard, label_filtered

2. **query_expand**: 검색 쿼리 확장/변형
   - graph_search 결과가 없을 때 사용
   - 동의어, 유사어, 다른 관점의 쿼리 생성

3. **explore_relations**: 노드 간 관계 탐색
   - 노드를 찾았지만 원하는 정보가 연결된 다른 노드에 있을 때
   - 모드: shortest_path, neighbors, hierarchy, schema
   - **⚠️ 점수 질문 시**: 반드시 hierarchy 모드 + max_depth: 4 사용!

4. **evaluate_results**: 수집된 정보 충분성 평가
   - 검색 후 답변 가능 여부 확인
   - 추가 검색이 필요한지 판단

## 전략
1. 먼저 graph_search로 직접 검색
2. 결과가 없으면: query_expand로 쿼리 확장 후 재검색
3. 노드를 찾았으면: explore_relations로 관련 정보 탐색
4. 충분한 정보가 모이면: 도구 호출 없이 바로 답변

## ⚠️ 중요: 점수/성능평가 질문 처리
사용자가 점수, 성능평가, 세부점수에 대해 질문하면:
1. graph_search로 차량/점수 노드 검색
2. 찾은 노드의 UUID로 **반드시** explore_relations 호출:
   - explore_mode: "hierarchy"
   - max_depth: 4 (세부 점수까지 도달)
3. 세부 점수(PT총점, 발진가속, 추월성능 등)까지 **모두 수집** 후 답변
4. "(정보 없음)" 대신 실제 데이터를 포함해야 함

## 주의사항
- 포기하지 마세요! 여러 방법을 시도하세요
- 이미 시도한 검색은 피하세요
- 찾은 정보를 종합하여 답변하세요
- 정보가 충분하면 도구를 더 이상 호출하지 말고 바로 답변하세요
- **점수 질문에는 반드시 hierarchy 탐색으로 세부 정보 수집**
`)

	// 계층적 쿼리 자동 감지 - 점수/세부 정보 질문인 경우 강조
	if state.OriginalQuery != "" && isHierarchicalQuery(state.OriginalQuery) {
		prompt.WriteString(`
## 🚨 감지됨: 계층적/세부 정보 질문
이 질문은 **세부 점수 또는 계층 정보**를 요구합니다.
- 반드시 explore_relations + hierarchy 모드 사용
- max_depth: 4 이상 설정 필수
- 세부 점수(발진가속, 추월성능, 주행편의성 등) 모두 포함
- "(정보 없음)" 없이 실제 데이터로 답변

`)
	}

	// Add current progress if not first iteration
	if state.Iteration > 1 {
		prompt.WriteString(fmt.Sprintf("\n## 현재 진행 상황 (반복 %d/%d)\n",
			state.Iteration, state.MaxIterations))

		if len(state.SearchContext.AttemptedQueries) > 0 {
			prompt.WriteString("### 시도한 검색:\n")
			for _, q := range state.SearchContext.AttemptedQueries {
				prompt.WriteString(fmt.Sprintf("- %s\n", q))
			}
		}

		if len(state.SearchContext.CollectedDocs) > 0 {
			prompt.WriteString(fmt.Sprintf("\n### 수집된 정보: %d건\n", len(state.SearchContext.CollectedDocs)))
		}

		if len(state.SearchContext.DiscoveredNodes) > 0 {
			prompt.WriteString("\n### 발견된 노드:\n")
			for i, node := range state.SearchContext.DiscoveredNodes {
				if i >= 5 {
					prompt.WriteString(fmt.Sprintf("... 외 %d개\n", len(state.SearchContext.DiscoveredNodes)-5))
					break
				}
				prompt.WriteString(fmt.Sprintf("- [%s] %s (UUID: %s)\n",
					strings.Join(node.Labels, ","), node.Name, node.UUID[:8]))
			}
		}

		if state.EvaluationResult != nil {
			prompt.WriteString(fmt.Sprintf("\n### 마지막 평가 결과:\n"))
			prompt.WriteString(fmt.Sprintf("- 충분성: %v (신뢰도: %.0f%%)\n",
				state.EvaluationResult.IsSufficient, state.EvaluationResult.Confidence*100))
			if len(state.EvaluationResult.MissingInfo) > 0 {
				prompt.WriteString("- 부족한 정보: " + strings.Join(state.EvaluationResult.MissingInfo, ", ") + "\n")
			}
			if state.EvaluationResult.SuggestedAction != "" {
				prompt.WriteString("- 제안: " + state.EvaluationResult.SuggestedAction + "\n")
			}
		}
	}

	return schema.SystemMessage(prompt.String())
}

// updateStateFromToolResult updates agent state based on tool execution result
func (a *GraphRAGAgent) updateStateFromToolResult(state *AgentState, msg *schema.Message) {
	if msg.Role != schema.Tool || msg.Content == "" {
		return
	}

	// Try to determine which tool was called and update state accordingly
	content := msg.Content

	// Check for graph_search results
	if strings.Contains(content, `"nodes"`) && strings.Contains(content, `"total_found"`) {
		var result tools.GraphSearchOutput
		if err := parseJSON(content, &result); err == nil {
			// Add discovered nodes with Properties
			for _, node := range result.Nodes {
				state.SearchContext.AddDiscoveredNode(NodeInfo{
					UUID:       node.UUID,
					Name:       node.Name,
					Labels:     node.Labels,
					Score:      node.Score,
					Properties: node.Properties, // Properties 포함!
				})

				// CollectedDocs에 노드 정보 추가
				doc := &schema.Document{
					ID:      node.UUID,
					Content: formatNodeAsDocument(node),
					MetaData: map[string]any{
						"source":     "graph_search",
						"labels":     node.Labels,
						"score":      node.Score,
						"properties": node.Properties,
					},
				}
				state.SearchContext.AddCollectedDoc(doc)
			}
		}
	}

	// Check for evaluate_results
	if strings.Contains(content, `"is_sufficient"`) && strings.Contains(content, `"confidence"`) {
		var result tools.EvaluateOutput
		if err := parseJSON(content, &result); err == nil {
			state.EvaluationResult = &EvaluationResult{
				IsSufficient:    result.IsSufficient,
				Confidence:      result.Confidence,
				MissingInfo:     result.MissingInfo,
				SuggestedAction: result.SuggestedAction,
				Reason:          result.Reasoning,
			}

			// Set stop flag if sufficient
			if result.IsSufficient && result.Confidence >= a.config.ConfidenceThreshold {
				state.ShouldStop = true
			}
		}
	}

	// Check for explore_relations results
	if strings.Contains(content, `"results"`) && strings.Contains(content, `"mode_used"`) {
		var result tools.ExploreOutput
		if err := parseJSON(content, &result); err == nil {
			for _, r := range result.Results {
				if r.TargetNode.UUID != "" {
					state.SearchContext.AddDiscoveredNode(NodeInfo{
						UUID:       r.TargetNode.UUID,
						Name:       r.TargetNode.Name,
						Labels:     r.TargetNode.Labels,
						Properties: r.Properties, // Properties 포함!
					})

					// CollectedDocs에 탐색 결과 추가
					doc := &schema.Document{
						ID:      r.TargetNode.UUID,
						Content: formatExploreResultAsDocument(r, result.Mode),
						MetaData: map[string]any{
							"source":     "explore_relations",
							"mode":       result.Mode,
							"labels":     r.TargetNode.Labels,
							"properties": r.Properties,
						},
					}
					state.SearchContext.AddCollectedDoc(doc)
				}
			}
		}
	}
}

// formatNodeAsDocument formats a search result node as readable document content
func formatNodeAsDocument(node tools.GraphSearchNodeResult) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("**%s** [%s]\n", node.Name, strings.Join(node.Labels, ", ")))

	if node.Score > 0 {
		sb.WriteString(fmt.Sprintf("- 관련도: %.2f\n", node.Score))
	}

	// Properties를 읽기 쉬운 형식으로 출력
	if node.Properties != nil && len(node.Properties) > 0 {
		sb.WriteString("- 속성:\n")
		for k, v := range node.Properties {
			switch val := v.(type) {
			case map[string]any:
				jsonBytes, _ := json.Marshal(val)
				sb.WriteString(fmt.Sprintf("  - %s: %s\n", k, string(jsonBytes)))
			case []any:
				jsonBytes, _ := json.Marshal(val)
				sb.WriteString(fmt.Sprintf("  - %s: %s\n", k, string(jsonBytes)))
			case float64:
				// 점수 형식으로 표시
				if strings.Contains(strings.ToLower(k), "score") || strings.Contains(strings.ToLower(k), "점수") {
					sb.WriteString(fmt.Sprintf("  - %s: %.1f점\n", k, val))
				} else {
					sb.WriteString(fmt.Sprintf("  - %s: %v\n", k, v))
				}
			default:
				sb.WriteString(fmt.Sprintf("  - %s: %v\n", k, v))
			}
		}
	}

	return sb.String()
}

// formatExploreResultAsDocument formats an explore result as readable document content
func formatExploreResultAsDocument(r tools.ExploreResult, mode string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("**%s** [%s]\n", r.TargetNode.Name, strings.Join(r.TargetNode.Labels, ", ")))

	if mode != "" {
		sb.WriteString(fmt.Sprintf("- 탐색 모드: %s\n", mode))
	}

	if r.Relationship != "" {
		sb.WriteString(fmt.Sprintf("- 관계: %s (%s)\n", r.Relationship, r.Direction))
	}

	if r.PathChain != "" {
		sb.WriteString(fmt.Sprintf("- 경로: %s\n", r.PathChain))
	}

	if r.Hops > 0 {
		sb.WriteString(fmt.Sprintf("- 홉 수: %d\n", r.Hops))
	}

	if r.Depth > 0 {
		sb.WriteString(fmt.Sprintf("- 깊이: %d\n", r.Depth))
	}

	// Properties 출력
	if r.Properties != nil && len(r.Properties) > 0 {
		sb.WriteString("- 속성:\n")
		for k, v := range r.Properties {
			switch val := v.(type) {
			case map[string]any:
				jsonBytes, _ := json.Marshal(val)
				sb.WriteString(fmt.Sprintf("  - %s: %s\n", k, string(jsonBytes)))
			case []any:
				jsonBytes, _ := json.Marshal(val)
				sb.WriteString(fmt.Sprintf("  - %s: %s\n", k, string(jsonBytes)))
			case float64:
				if strings.Contains(strings.ToLower(k), "score") || strings.Contains(strings.ToLower(k), "점수") {
					sb.WriteString(fmt.Sprintf("  - %s: %.1f점\n", k, val))
				} else {
					sb.WriteString(fmt.Sprintf("  - %s: %v\n", k, v))
				}
			default:
				sb.WriteString(fmt.Sprintf("  - %s: %v\n", k, v))
			}
		}
	}

	// 하위 노드 (계층 탐색의 경우)
	if len(r.Children) > 0 {
		sb.WriteString("- 하위 항목:\n")
		for _, child := range r.Children {
			sb.WriteString(fmt.Sprintf("  - %s [%s]\n",
				child.TargetNode.Name, strings.Join(child.TargetNode.Labels, ", ")))
			if child.Properties != nil {
				for k, v := range child.Properties {
					sb.WriteString(fmt.Sprintf("    - %s: %v\n", k, v))
				}
			}
		}
	}

	return sb.String()
}

// generateFinalAnswer generates the final answer when tool-based answer is not available
func (a *GraphRAGAgent) generateFinalAnswer(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
	// Get state
	var collectedContext string
	var originalQuery string
	err := compose.ProcessState[*AgentState](ctx, func(ctx context.Context, state *AgentState) error {
		collectedContext = state.GetCollectedContext()
		originalQuery = state.OriginalQuery
		return nil
	})
	if err != nil {
		collectedContext = ""
	}

	// Find original query from messages if not in state
	if originalQuery == "" {
		for _, msg := range messages {
			if msg.Role == schema.User {
				originalQuery = msg.Content
				break
			}
		}
	}

	// Build system prompt with formatting guidelines (Chain 모드의 ragSystemPrompt 스타일 적용)
	systemPrompt := `당신은 Graph Database 정보를 기반으로 답변하는 자동차 성능 분석 전문가입니다.

## 답변 형식 가이드라인

### 점수 표현
- 성능 점수: XX/125점 (백분율%) 형식 사용
- 세부 점수가 있으면 반드시 포함
- 예: "84/125점 (67.2%)"

### 비교 요청시
- 마크다운 테이블 사용
- 항목별로 명확히 비교
- 예:
| 항목 | 차량A | 차량B |
|------|-------|-------|
| 총점 | 84점 | 92점 |

### 목록 요청시
- 번호 또는 불릿 리스트 사용
- 각 항목에 관련 정보 포함

### 구조화된 답변
- 명확한 섹션 구분
- 핵심 정보 먼저 제시
- 세부 정보는 하위 항목으로

### 주의사항
- 수집된 정보에 있는 모든 관련 데이터를 누락 없이 포함
- 추측이나 가정은 명시적으로 표시
- 정보가 없는 경우 "해당 정보가 없습니다" 명시`

	// Build user prompt with collected context
	var userPrompt strings.Builder
	userPrompt.WriteString("## 사용자 질문\n")
	userPrompt.WriteString(originalQuery)
	userPrompt.WriteString("\n\n")

	if collectedContext != "" {
		userPrompt.WriteString("## 수집된 정보 (Graph DB에서 검색/탐색한 결과)\n\n")
		userPrompt.WriteString(collectedContext)
		userPrompt.WriteString("\n\n")
	} else {
		userPrompt.WriteString("## 수집된 정보\n")
		userPrompt.WriteString("검색 결과가 없습니다.\n\n")
	}

	userPrompt.WriteString("위 정보를 바탕으로 사용자 질문에 상세하게 답변해주세요. ")
	userPrompt.WriteString("수집된 정보에 있는 모든 관련 데이터를 누락 없이 포함하세요.")

	// Generate answer without tools
	answerMessages := []*schema.Message{
		schema.SystemMessage(systemPrompt),
		schema.UserMessage(userPrompt.String()),
	}

	return a.config.ChatModel.Generate(ctx, answerMessages)
}

// Invoke runs the agent synchronously
func (a *GraphRAGAgent) Invoke(ctx context.Context, messages []*schema.Message, opts ...compose.Option) (*schema.Message, error) {
	return a.runnable.Invoke(ctx, messages, opts...)
}

// Stream runs the agent and returns a streaming response
func (a *GraphRAGAgent) Stream(ctx context.Context, messages []*schema.Message, opts ...compose.Option) (*schema.StreamReader[*schema.Message], error) {
	return a.runnable.Stream(ctx, messages, opts...)
}

// emitDebug emits a debug event if debug is enabled
func (a *GraphRAGAgent) emitDebug(eventName string, payload interface{}) {
	if a.config.EnableDebug && a.config.DebugEmitter != nil {
		a.config.DebugEmitter(eventName, payload)
	}
}

// SetDebugEmitter sets the debug emitter
func (a *GraphRAGAgent) SetDebugEmitter(emitter DebugEmitter) {
	a.config.DebugEmitter = emitter
}

// parseJSON is a helper to parse JSON strings
func parseJSON(content string, v any) error {
	content = strings.TrimSpace(content)
	return parseJSONContent(content, v)
}

func parseJSONContent(content string, v any) error {
	// Try direct parse first
	if err := tryParseJSON(content, v); err == nil {
		return nil
	}

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

	return tryParseJSON(content, v)
}

func tryParseJSON(content string, v any) error {
	return json.Unmarshal([]byte(content), v)
}

// GetTools returns the agent's tools
func (a *GraphRAGAgent) GetTools() []tool.BaseTool {
	return a.tools
}

// GetConfig returns the agent's configuration
func (a *GraphRAGAgent) GetConfig() *AgentConfig {
	return a.config
}

// GetMaxIterations returns the maximum iterations setting
func (a *GraphRAGAgent) GetMaxIterations() int {
	return a.config.MaxIterations
}

// GetConfidenceThreshold returns the confidence threshold setting
func (a *GraphRAGAgent) GetConfidenceThreshold() float64 {
	return a.config.ConfidenceThreshold
}

// isHierarchicalQuery detects if the query requires hierarchical/detailed information
func isHierarchicalQuery(query string) bool {
	// 계층적 쿼리를 나타내는 키워드들
	hierarchicalKeywords := []string{
		"하위", "세부", "모두", "전체", "상세", "자세히", "자세하게",
		"점수", "성능평가", "평가점수", "세부점수",
		"PT총점", "발진가속", "추월성능", "주행편의",
		"연결된", "관련된", "포함된",
	}

	queryLower := strings.ToLower(query)
	for _, keyword := range hierarchicalKeywords {
		if strings.Contains(queryLower, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

// StreamWithDebug runs the agent with a debug emitter and returns streaming response
func (a *GraphRAGAgent) StreamWithDebug(ctx context.Context, messages []*schema.Message, emitter DebugEmitter) (*schema.StreamReader[*schema.Message], error) {
	// Set the debug emitter temporarily for this run
	originalEmitter := a.config.DebugEmitter
	a.config.DebugEmitter = emitter

	// Emit agent start
	a.emitDebug("agent:start", map[string]any{
		"max_iterations":       a.config.MaxIterations,
		"confidence_threshold": a.config.ConfidenceThreshold,
		"tools_count":          len(a.tools),
	})

	// Run the agent
	streamReader, err := a.runnable.Stream(ctx, messages)
	if err != nil {
		a.config.DebugEmitter = originalEmitter
		return nil, err
	}

	// Wrap stream to restore emitter and emit completion
	newReader, writer := schema.Pipe[*schema.Message](1)

	go func() {
		defer writer.Close()
		defer func() {
			a.emitDebug("agent:complete", map[string]any{
				"status": "completed",
			})
			a.config.DebugEmitter = originalEmitter
		}()

		for {
			msg, recvErr := streamReader.Recv()
			if recvErr != nil {
				return
			}
			writer.Send(msg, nil)
		}
	}()

	return newReader, nil
}
