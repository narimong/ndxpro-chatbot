package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

func init() {
	// Register state type for Eino's state management
	schema.RegisterName[*AgentState]("_agent_chatbot_state")
}

// AgentState는 ReAct 그래프 실행 중 공유되는 상태
type AgentState struct {
	// 원본 쿼리
	OriginalQuery string

	// 메시지 히스토리 (ReAct 패턴)
	Messages []*schema.Message

	// 검색 컨텍스트
	SearchContext *SearchContext

	// 평가 결과
	EvaluationResult *EvaluationResult

	// 반복 제어
	Iteration     int
	MaxIterations int
	ShouldStop    bool

	// 디버그 정보
	DebugInfo *DebugInfo

	// 가시화 데이터 수집기 (StreamWithDebug에서 초기화)
	Visualization *VisualizationCollector
}

// NewAgentState creates a new agent state with default values
func NewAgentState(maxIterations int) *AgentState {
	return &AgentState{
		Messages:      make([]*schema.Message, 0, maxIterations+1),
		MaxIterations: maxIterations,
		SearchContext: &SearchContext{
			AttemptedQueries: make([]string, 0),
			CollectedDocs:    make([]*schema.Document, 0),
			VisitedNodes:     make(map[string]bool),
			FailedStrategies: make([]SearchStrategy, 0),
		},
		DebugInfo: &DebugInfo{
			ToolCalls: make([]ToolCallRecord, 0),
		},
	}
}

// SearchContext는 검색 상태를 추적
type SearchContext struct {
	// 시도한 쿼리들
	AttemptedQueries []string

	// 수집된 문서들
	CollectedDocs []*schema.Document

	// 탐색한 노드들 (중복 방지)
	VisitedNodes map[string]bool

	// 현재 검색 전략
	CurrentStrategy SearchStrategy

	// 실패한 전략들
	FailedStrategies []SearchStrategy

	// 발견된 노드 UUID들 (관계 탐색용)
	DiscoveredNodes []NodeInfo
}

// NodeInfo는 발견된 노드 정보
type NodeInfo struct {
	UUID       string
	Name       string
	Labels     []string
	Score      float64
	Properties map[string]any
}

// SearchStrategy는 검색 전략 타입
type SearchStrategy string

const (
	StrategyExact      SearchStrategy = "exact"
	StrategyFuzzy      SearchStrategy = "fuzzy"
	StrategyWildcard   SearchStrategy = "wildcard"
	StrategyExpanded   SearchStrategy = "expanded"
	StrategyRelational SearchStrategy = "relational"
	StrategyHierarchy  SearchStrategy = "hierarchy"
	StrategyBroad      SearchStrategy = "broad"
)

// EvaluationResult는 검색 결과 평가
type EvaluationResult struct {
	IsSufficient    bool     // 답변 가능 여부
	Confidence      float64  // 신뢰도 (0.0-1.0)
	MissingInfo     []string // 부족한 정보
	SuggestedAction string   // 다음 행동 제안
	Reason          string   // 평가 근거
}

// DebugInfo는 디버그 정보를 추적
type DebugInfo struct {
	ToolCalls     []ToolCallRecord
	TotalTokens   int
	StartTime     int64
	LastEventTime int64
}

// ToolCallRecord는 도구 호출 기록
type ToolCallRecord struct {
	ToolName  string
	Arguments string
	Result    string
	Duration  int64 // milliseconds
	Success   bool
	Error     string
}

// AddAttemptedQuery adds a query to the attempted list if not already present
func (s *SearchContext) AddAttemptedQuery(query string) bool {
	for _, q := range s.AttemptedQueries {
		if q == query {
			return false // Already attempted
		}
	}
	s.AttemptedQueries = append(s.AttemptedQueries, query)
	return true
}

// HasVisitedNode checks if a node has been visited
func (s *SearchContext) HasVisitedNode(uuid string) bool {
	return s.VisitedNodes[uuid]
}

// MarkNodeVisited marks a node as visited
func (s *SearchContext) MarkNodeVisited(uuid string) {
	s.VisitedNodes[uuid] = true
}

// AddDiscoveredNode adds a discovered node if not already visited
func (s *SearchContext) AddDiscoveredNode(node NodeInfo) bool {
	if s.HasVisitedNode(node.UUID) {
		return false
	}
	s.DiscoveredNodes = append(s.DiscoveredNodes, node)
	return true
}

// GetUnvisitedNodes returns nodes that haven't been explored yet
func (s *SearchContext) GetUnvisitedNodes() []NodeInfo {
	unvisited := make([]NodeInfo, 0)
	for _, node := range s.DiscoveredNodes {
		if !s.HasVisitedNode(node.UUID) {
			unvisited = append(unvisited, node)
		}
	}
	return unvisited
}

// AddCollectedDoc adds a document if not already collected (by ID)
func (s *SearchContext) AddCollectedDoc(doc *schema.Document) bool {
	for _, d := range s.CollectedDocs {
		if d.ID == doc.ID {
			return false // Already collected
		}
	}
	s.CollectedDocs = append(s.CollectedDocs, doc)
	return true
}

// HasTriedStrategy checks if a strategy has been tried
func (s *SearchContext) HasTriedStrategy(strategy SearchStrategy) bool {
	for _, fs := range s.FailedStrategies {
		if fs == strategy {
			return true
		}
	}
	return false
}

// MarkStrategyFailed marks a strategy as failed
func (s *SearchContext) MarkStrategyFailed(strategy SearchStrategy) {
	if !s.HasTriedStrategy(strategy) {
		s.FailedStrategies = append(s.FailedStrategies, strategy)
	}
}

// GetNextStrategy suggests the next strategy to try
func (s *SearchContext) GetNextStrategy() SearchStrategy {
	strategies := []SearchStrategy{
		StrategyExact,
		StrategyFuzzy,
		StrategyWildcard,
		StrategyExpanded,
		StrategyRelational,
		StrategyHierarchy,
		StrategyBroad,
	}

	for _, strategy := range strategies {
		if !s.HasTriedStrategy(strategy) {
			return strategy
		}
	}

	return "" // All strategies tried
}

// ShouldContinueSearching determines if more searching is warranted
func (s *AgentState) ShouldContinueSearching() bool {
	// Stop if explicitly flagged
	if s.ShouldStop {
		return false
	}

	// Stop if max iterations reached
	if s.Iteration >= s.MaxIterations {
		return false
	}

	// Stop if evaluation says sufficient
	if s.EvaluationResult != nil && s.EvaluationResult.IsSufficient {
		return false
	}

	// Continue if there are unvisited nodes or untried strategies
	if len(s.SearchContext.GetUnvisitedNodes()) > 0 {
		return true
	}

	if s.SearchContext.GetNextStrategy() != "" {
		return true
	}

	return false
}

// GetCollectedContext returns all collected information as a comprehensive context string
func (s *AgentState) GetCollectedContext() string {
	var sb strings.Builder

	// 1. CollectedDocs 내용 (도구 결과에서 수집된 상세 정보)
	if len(s.SearchContext.CollectedDocs) > 0 {
		sb.WriteString("## 수집된 정보\n\n")
		for i, doc := range s.SearchContext.CollectedDocs {
			sb.WriteString(fmt.Sprintf("### 정보 %d\n", i+1))
			sb.WriteString(doc.Content)
			sb.WriteString("\n\n")
		}
	}

	// 2. DiscoveredNodes 요약 (검색으로 발견된 노드들)
	if len(s.SearchContext.DiscoveredNodes) > 0 {
		sb.WriteString("## 발견된 노드 요약\n\n")
		for _, node := range s.SearchContext.DiscoveredNodes {
			sb.WriteString(fmt.Sprintf("- **%s** [%s]",
				node.Name, strings.Join(node.Labels, ", ")))
			if node.Score > 0 {
				sb.WriteString(fmt.Sprintf(" (관련도: %.2f)", node.Score))
			}
			sb.WriteString("\n")

			// Properties 출력
			if node.Properties != nil && len(node.Properties) > 0 {
				for k, v := range node.Properties {
					// 값이 복잡한 경우 JSON으로 출력
					switch val := v.(type) {
					case map[string]any, []any:
						jsonBytes, _ := json.Marshal(val)
						sb.WriteString(fmt.Sprintf("  - %s: %s\n", k, string(jsonBytes)))
					default:
						sb.WriteString(fmt.Sprintf("  - %s: %v\n", k, v))
					}
				}
			}
		}
	}

	return sb.String()
}

// RecordToolCall records a tool call for debugging
func (s *AgentState) RecordToolCall(record ToolCallRecord) {
	if s.DebugInfo != nil {
		s.DebugInfo.ToolCalls = append(s.DebugInfo.ToolCalls, record)
	}
}
