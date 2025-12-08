package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"agent-chatbot/api/service/db"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// RelationExploreTool - 관계 탐색 도구
type RelationExploreTool struct {
	neo4jClient *db.Neo4jClient
}

// ExploreInput represents the input for relation exploration
type ExploreInput struct {
	StartUUID     string `json:"start_uuid"`
	TargetLabel   string `json:"target_label,omitempty"`
	RelationType  string `json:"relation_type,omitempty"`
	MaxDepth      int    `json:"max_depth,omitempty"`
	ExploreMode   string `json:"explore_mode,omitempty"` // shortest_path, neighbors, hierarchy, schema
	Direction     string `json:"direction,omitempty"`    // outgoing, incoming, both
}

// ExploreOutput represents the exploration results
type ExploreOutput struct {
	Success      bool               `json:"success"`
	StartNode    ExploreNodeInfo    `json:"start_node"`
	Mode         string             `json:"mode_used"`
	Results      []ExploreResult    `json:"results"`
	TotalFound   int                `json:"total_found"`
	Message      string             `json:"message,omitempty"`
}

// ExploreNodeInfo represents basic node information
type ExploreNodeInfo struct {
	UUID   string   `json:"uuid"`
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
}

// ExploreResult represents a single exploration result
type ExploreResult struct {
	// For shortest_path mode
	PathChain    string `json:"path_chain,omitempty"`
	Hops         int    `json:"hops,omitempty"`

	// For neighbors mode
	Relationship string `json:"relationship,omitempty"`
	Direction    string `json:"direction,omitempty"`

	// Target node info
	TargetNode   ExploreNodeInfo    `json:"target_node"`
	Properties   map[string]any     `json:"properties,omitempty"`

	// For hierarchy mode
	Depth        int                `json:"depth,omitempty"`
	Children     []ExploreResult    `json:"children,omitempty"`
}

// NewRelationExploreTool creates a new relation exploration tool
func NewRelationExploreTool(neo4jClient *db.Neo4jClient) *RelationExploreTool {
	return &RelationExploreTool{
		neo4jClient: neo4jClient,
	}
}

// Info returns the tool information for LLM
func (t *RelationExploreTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "explore_relations",
		Desc: `특정 노드에서 시작하여 관계를 따라 탐색합니다.

## 사용 시점
- graph_search로 노드를 찾았지만 원하는 정보가 연결된 다른 노드에 있을 때
- 특정 노드와 관련된 모든 정보를 탐색하고 싶을 때
- **성능 점수/평가 점수 질문 시 반드시 사용**

## 탐색 모드 (explore_mode)
- shortest_path: 시작 노드에서 목표 라벨까지 최단 경로 찾기
- neighbors: 직접 연결된 이웃 노드 탐색 (1홉)
- hierarchy: 계층 구조 탐색 (**점수 세부사항에 필수!**)
- schema: 해당 노드에서 가능한 관계 타입 확인

## ⚠️ 중요: 점수/세부정보 탐색 시
- **반드시** explore_mode: "hierarchy" 사용
- **반드시** max_depth: 4 이상 설정 (세부 점수까지 도달)
- 점수 계층: 총점 → PT총점/주행편의 → 발진가속/추월성능 → 세부항목 (3~4단계)
- max_depth가 작으면 세부 점수가 누락됨!

## 예시
- 차량의 성능 점수: {"start_uuid": "...", "target_label": "PerformanceTotalScore", "explore_mode": "shortest_path"}
- 차량의 이웃 노드: {"start_uuid": "...", "explore_mode": "neighbors"}
- **점수 세부사항 (권장)**: {"start_uuid": "...", "explore_mode": "hierarchy", "max_depth": 4}`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"start_uuid": {
				Type:     schema.String,
				Desc:     "탐색을 시작할 노드의 UUID (graph_search 결과에서 획득)",
				Required: true,
			},
			"target_label": {
				Type:     schema.String,
				Desc:     "찾고자 하는 목표 노드의 라벨 (shortest_path 모드에서 사용)",
				Required: false,
			},
			"relation_type": {
				Type:     schema.String,
				Desc:     "필터링할 관계 타입 (예: hasScore, hasEngine)",
				Required: false,
			},
			"max_depth": {
				Type:     schema.Integer,
				Desc:     "최대 탐색 깊이 (기본: 4). 점수 세부사항은 4 이상 권장",
				Required: false,
			},
			"explore_mode": {
				Type:     schema.String,
				Desc:     "탐색 모드: shortest_path, neighbors, hierarchy, schema",
				Required: false,
				Enum:     []string{"shortest_path", "neighbors", "hierarchy", "schema"},
			},
			"direction": {
				Type:     schema.String,
				Desc:     "관계 방향: outgoing, incoming, both (기본: both)",
				Required: false,
				Enum:     []string{"outgoing", "incoming", "both"},
			},
		}),
	}, nil
}

// InvokableRun executes the exploration
func (t *RelationExploreTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input ExploreInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return t.errorResponse("인자 파싱 실패: " + err.Error())
	}

	// Set defaults
	if input.MaxDepth <= 0 {
		input.MaxDepth = 4 // 세부 점수까지 도달하도록 상향 (depth 3-4까지 필요)
	}
	if input.MaxDepth > 6 {
		input.MaxDepth = 6
	}
	if input.ExploreMode == "" {
		input.ExploreMode = "neighbors"
	}
	if input.Direction == "" {
		input.Direction = "both"
	}

	// Execute exploration based on mode
	var output *ExploreOutput
	var err error

	switch input.ExploreMode {
	case "shortest_path":
		output, err = t.exploreShortestPath(ctx, input)
	case "neighbors":
		output, err = t.exploreNeighbors(ctx, input)
	case "hierarchy":
		output, err = t.exploreHierarchy(ctx, input)
	case "schema":
		output, err = t.exploreSchema(ctx, input)
	default:
		output, err = t.exploreNeighbors(ctx, input)
	}

	if err != nil {
		return t.errorResponse(fmt.Sprintf("탐색 실패: %v", err))
	}

	return t.jsonResponse(output)
}

func (t *RelationExploreTool) exploreShortestPath(ctx context.Context, input ExploreInput) (*ExploreOutput, error) {
	if input.TargetLabel == "" {
		return &ExploreOutput{
			Success: false,
			Message: "shortest_path 모드에는 target_label이 필요합니다",
		}, nil
	}

	paths, err := t.neo4jClient.ShortestPathToLabel(ctx, input.StartUUID, input.TargetLabel, input.MaxDepth, 5)
	if err != nil {
		return nil, err
	}

	output := &ExploreOutput{
		Success:    len(paths) > 0,
		Mode:       "shortest_path",
		Results:    make([]ExploreResult, 0, len(paths)),
		TotalFound: len(paths),
	}

	if len(paths) == 0 {
		output.Message = fmt.Sprintf("'%s' 라벨까지의 경로를 찾지 못했습니다", input.TargetLabel)
	} else {
		output.Message = fmt.Sprintf("%d개 경로 발견", len(paths))
	}

	for _, path := range paths {
		result := ExploreResult{
			PathChain: path.PathChain,
			Hops:      path.Hops,
			TargetNode: ExploreNodeInfo{
				UUID:   path.EndNodeUUID,
				Name:   path.EndNodeName,
				Labels: path.EndNodeLabels,
			},
			Properties: t.filterProperties(path.EndProperties),
		}
		output.Results = append(output.Results, result)
	}

	return output, nil
}

func (t *RelationExploreTool) exploreNeighbors(ctx context.Context, input ExploreInput) (*ExploreOutput, error) {
	// Get node info first
	nodeInfo, err := t.neo4jClient.GetNodeByUUID(ctx, input.StartUUID)
	if err != nil {
		return nil, err
	}

	// Extract name and labels from nodeInfo map
	nodeName := ""
	if name, ok := nodeInfo["name"].(string); ok {
		nodeName = name
	}
	nodeLabels := []string{}
	if labels, ok := nodeInfo["labels"].([]any); ok {
		for _, l := range labels {
			if label, ok := l.(string); ok {
				nodeLabels = append(nodeLabels, label)
			}
		}
	}

	output := &ExploreOutput{
		Success: true,
		Mode:    "neighbors",
		StartNode: ExploreNodeInfo{
			UUID:   input.StartUUID,
			Name:   nodeName,
			Labels: nodeLabels,
		},
		Results: make([]ExploreResult, 0),
	}

	// Get relationships
	schemas, err := t.neo4jClient.GetRelationshipSchema(ctx, input.StartUUID)
	if err != nil {
		return nil, err
	}

	for _, rel := range schemas {
		// Apply direction filter
		if input.Direction == "outgoing" && rel.Direction != "outgoing" {
			continue
		}
		if input.Direction == "incoming" && rel.Direction != "incoming" {
			continue
		}

		// Apply relation type filter
		if input.RelationType != "" && !strings.EqualFold(rel.RelType, input.RelationType) {
			continue
		}

		result := ExploreResult{
			Relationship: rel.RelType,
			Direction:    rel.Direction,
			TargetNode: ExploreNodeInfo{
				Labels: []string{rel.ConnectedLabel},
			},
			Properties: map[string]any{
				"count":        rel.Count,
				"sample_nodes": rel.SampleNodes,
			},
		}
		output.Results = append(output.Results, result)
	}

	output.TotalFound = len(output.Results)
	if output.TotalFound == 0 {
		output.Message = "연결된 노드가 없습니다"
		output.Success = false
	} else {
		output.Message = fmt.Sprintf("%d개 관계 유형 발견", output.TotalFound)
	}

	return output, nil
}

func (t *RelationExploreTool) exploreHierarchy(ctx context.Context, input ExploreInput) (*ExploreOutput, error) {
	root, children, err := t.neo4jClient.GetPerformanceScoreDetails(ctx, input.StartUUID, input.MaxDepth)
	if err != nil {
		return nil, err
	}

	// ✅ Flat List를 Tree 구조로 변환
	treeRoot := buildHierarchyTree(root, children)

	output := &ExploreOutput{
		Success: true,
		Mode:    "hierarchy",
		StartNode: ExploreNodeInfo{
			UUID:   treeRoot.UUID,
			Name:   treeRoot.Name,
			Labels: treeRoot.Labels,
		},
		Results: make([]ExploreResult, 0),
	}

	// treeRoot.Children이 이제 계층적으로 채워져 있음
	for _, child := range treeRoot.Children {
		result := t.hierarchicalNodeToResult(child)
		output.Results = append(output.Results, result)
	}

	output.TotalFound = countAllNodes(treeRoot) - 1 // root 제외
	if output.TotalFound == 0 {
		output.Message = "하위 노드가 없습니다"
	} else {
		output.Message = fmt.Sprintf("%d개 하위 노드 발견 (최대 깊이: %d)", output.TotalFound, input.MaxDepth)
	}

	return output, nil
}

// buildHierarchyTree converts flat list to hierarchical tree structure
func buildHierarchyTree(root *db.HierarchicalNode, flatList []*db.HierarchicalNode) *db.HierarchicalNode {
	// 1. nodeMap 생성 (UUID → Node 매핑)
	nodeMap := make(map[string]*db.HierarchicalNode)
	nodeMap[root.UUID] = root
	root.Children = make([]*db.HierarchicalNode, 0) // Children 초기화

	for _, node := range flatList {
		node.Children = make([]*db.HierarchicalNode, 0) // 각 노드의 Children 초기화
		nodeMap[node.UUID] = node
	}

	// 2. Depth 순으로 정렬 (부모가 먼저 처리되도록)
	sort.Slice(flatList, func(i, j int) bool {
		return flatList[i].Depth < flatList[j].Depth
	})

	// 3. ParentUUID로 트리 구조 생성
	for _, node := range flatList {
		if node.ParentUUID != "" {
			if parent, ok := nodeMap[node.ParentUUID]; ok {
				parent.Children = append(parent.Children, node)
			}
		} else if node.Depth == 1 {
			// depth 1 노드는 root의 직접 자식
			root.Children = append(root.Children, node)
		}
	}

	return root
}

// countAllNodes counts total nodes in tree (including root)
func countAllNodes(node *db.HierarchicalNode) int {
	count := 1
	for _, child := range node.Children {
		count += countAllNodes(child)
	}
	return count
}

func (t *RelationExploreTool) exploreSchema(ctx context.Context, input ExploreInput) (*ExploreOutput, error) {
	schemas, err := t.neo4jClient.GetRelationshipSchema(ctx, input.StartUUID)
	if err != nil {
		return nil, err
	}

	output := &ExploreOutput{
		Success:    true,
		Mode:       "schema",
		TotalFound: len(schemas),
		Results:    make([]ExploreResult, 0, len(schemas)),
	}

	for _, rel := range schemas {
		result := ExploreResult{
			Relationship: rel.RelType,
			Direction:    rel.Direction,
			TargetNode: ExploreNodeInfo{
				Labels: []string{rel.ConnectedLabel},
			},
			Properties: map[string]any{
				"count":        rel.Count,
				"sample_nodes": rel.SampleNodes,
			},
		}
		output.Results = append(output.Results, result)
	}

	if output.TotalFound == 0 {
		output.Message = "이 노드에서 사용 가능한 관계가 없습니다"
		output.Success = false
	} else {
		output.Message = fmt.Sprintf("%d개 관계 유형 발견. 원하는 정보에 따라 explore_mode를 선택하세요", output.TotalFound)
	}

	return output, nil
}

func (t *RelationExploreTool) hierarchicalNodeToResult(node *db.HierarchicalNode) ExploreResult {
	result := ExploreResult{
		Depth: node.Depth,
		TargetNode: ExploreNodeInfo{
			UUID:   node.UUID,
			Name:   node.Name,
			Labels: node.Labels,
		},
		Properties: t.filterProperties(node.Properties),
	}

	if len(node.Children) > 0 {
		result.Children = make([]ExploreResult, 0, len(node.Children))
		for _, child := range node.Children {
			result.Children = append(result.Children, t.hierarchicalNodeToResult(child))
		}
	}

	return result
}

func (t *RelationExploreTool) filterProperties(props map[string]any) map[string]any {
	if props == nil {
		return nil
	}

	filtered := make(map[string]any)
	for k, v := range props {
		// Skip internal properties
		if strings.HasPrefix(k, "_") || k == "uuid" || k == "embedding" {
			continue
		}
		filtered[k] = v
	}

	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func (t *RelationExploreTool) jsonResponse(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (t *RelationExploreTool) errorResponse(msg string) (string, error) {
	output := ExploreOutput{
		Success: false,
		Message: msg,
	}
	return t.jsonResponse(output)
}
