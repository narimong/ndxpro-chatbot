package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"simple-chatbot/api/service/db"
	"simple-chatbot/api/service/tools"

	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
)

// DebugEventType represents the type of debug event
type DebugEventType string

const (
	DebugEventQuery         DebugEventType = "query"
	DebugEventRetrieval     DebugEventType = "retrieval"
	DebugEventStep          DebugEventType = "step"
	DebugEventPlan          DebugEventType = "plan"
	DebugEventShortestPath  DebugEventType = "shortest_path"
	DebugEventClarification DebugEventType = "clarification"
	DebugEventHierarchy     DebugEventType = "hierarchy"
)

// Hierarchical query keywords in Korean and English
var hierarchicalKeywords = []string{
	"하위", "세부", "모두", "전체", "상세", "연결된", "관련", "포함",
	"breakdown", "details", "all", "complete", "sub", "children", "nested",
}

// DebugEvent represents a debug event from GraphRAGRetriever
type DebugEvent struct {
	Type    DebugEventType `json:"type"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

// GraphRAGRetriever implements a Multi-Stage Retrieval pattern using Neo4j
// Stage 1: Query Analysis - Extract entities and target labels
// Stage 2: Full-Text Search - Find starting nodes
// Stage 3: Shortest Path - Navigate to target nodes
// Stage 4: Context Assembly - Build comprehensive response
type GraphRAGRetriever struct {
	neo4jClient          *db.Neo4jClient
	chatModel            einoModel.ChatModel
	fullTextTool         *tools.Neo4jFullTextSearchTool
	rerankerTool         *tools.LLMRerankerTool
	graphTool            *tools.Neo4jGraphTraversalTool
	shortestPathTool     *tools.Neo4jShortestPathTool
	queryAnalyzer        *tools.QueryAnalyzerTool
	clarificationService *ClarificationService
	debugEmitter         DebugEmitter
	defaultTopK          int
	maxGraphDepth        int
	enableReranking      bool
	enableShortestPath   bool
}

// GraphRAGConfig holds configuration for GraphRAGRetriever
type GraphRAGConfig struct {
	Neo4jClient        *db.Neo4jClient
	ChatModel          einoModel.ChatModel
	DebugEmitter       DebugEmitter
	DefaultTopK        int
	MaxGraphDepth      int
	EnableReranking    bool
	EnableShortestPath bool // Enable shortest path navigation
}

// NewGraphRAGRetriever creates a new Graph RAG retriever
func NewGraphRAGRetriever(config *GraphRAGConfig) (*GraphRAGRetriever, error) {
	if config.Neo4jClient == nil {
		return nil, fmt.Errorf("neo4j client is required")
	}
	if config.ChatModel == nil {
		return nil, fmt.Errorf("chat model is required")
	}

	if config.DefaultTopK <= 0 {
		config.DefaultTopK = 5
	}
	if config.MaxGraphDepth <= 0 {
		config.MaxGraphDepth = 2
	}

	// Initialize clarification service
	clarificationSvc, err := NewClarificationService(&ClarificationConfig{
		Neo4jClient: config.Neo4jClient,
		ChatModel:   config.ChatModel,
	})
	if err != nil {
		// Non-fatal: continue without clarification
		clarificationSvc = nil
	}

	// Get query analyzer from clarification service or create new one
	var queryAnalyzer *tools.QueryAnalyzerTool
	if clarificationSvc != nil {
		queryAnalyzer = clarificationSvc.GetQueryAnalyzer()
	}

	return &GraphRAGRetriever{
		neo4jClient:          config.Neo4jClient,
		chatModel:            config.ChatModel,
		fullTextTool:         tools.NewNeo4jFullTextSearchTool(config.Neo4jClient),
		rerankerTool:         tools.NewLLMRerankerTool(config.ChatModel),
		graphTool:            tools.NewNeo4jGraphTraversalTool(config.Neo4jClient),
		shortestPathTool:     tools.NewNeo4jShortestPathTool(config.Neo4jClient),
		queryAnalyzer:        queryAnalyzer,
		clarificationService: clarificationSvc,
		debugEmitter:         config.DebugEmitter,
		defaultTopK:          config.DefaultTopK,
		maxGraphDepth:        config.MaxGraphDepth,
		enableReranking:      config.EnableReranking,
		enableShortestPath:   config.EnableShortestPath,
	}, nil
}

// Retrieve implements the retriever.Retriever interface
func (r *GraphRAGRetriever) Retrieve(ctx context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
	// Get options
	o := retriever.GetCommonOptions(&retriever.Options{
		TopK: ptrInt(r.defaultTopK),
	}, opts...)

	topK := r.defaultTopK
	if o.TopK != nil {
		topK = *o.TopK
	}

	// Use shortest path mode if enabled and query analyzer is available
	if r.enableShortestPath && r.queryAnalyzer != nil {
		return r.retrieveWithShortestPath(ctx, query, topK)
	}

	// Fallback to legacy retrieval
	return r.retrieveLegacy(ctx, query, topK)
}

// RetrieveWithTargetLabels retrieves documents with explicit target labels (post-clarification)
func (r *GraphRAGRetriever) RetrieveWithTargetLabels(ctx context.Context, query string, targetLabels []string, topK int) ([]*schema.Document, error) {
	if topK <= 0 {
		topK = r.defaultTopK
	}

	// Stage 1: Full-text search for starting nodes
	r.emitDebug(DebugEvent{
		Type:    DebugEventQuery,
		Message: fmt.Sprintf("Searching with target labels: %v", targetLabels),
		Data:    map[string]any{"query": query, "target_labels": targetLabels},
	})

	searchInput := tools.FullTextSearchInput{
		Query: query,
		TopK:  10,
	}
	searchInputJSON, _ := json.Marshal(searchInput)

	searchResultJSON, err := r.fullTextTool.InvokableRun(ctx, string(searchInputJSON))
	if err != nil {
		return nil, fmt.Errorf("full-text search failed: %w", err)
	}

	var searchResult tools.FullTextSearchResult
	if err := json.Unmarshal([]byte(searchResultJSON), &searchResult); err != nil {
		return nil, fmt.Errorf("failed to parse search result: %w", err)
	}

	if len(searchResult.Candidates) == 0 {
		return []*schema.Document{}, nil
	}

	// Stage 2: Find shortest paths to target labels
	documents := make([]*schema.Document, 0)
	for _, candidate := range searchResult.Candidates[:min(3, len(searchResult.Candidates))] {
		for _, targetLabel := range targetLabels {
			paths, err := r.neo4jClient.ShortestPathToLabel(ctx, candidate.UUID, targetLabel, 6, topK)
			if err != nil || len(paths) == 0 {
				continue
			}

			r.emitDebug(DebugEvent{
				Type:    DebugEventShortestPath,
				Message: fmt.Sprintf("Found %d paths from %s to %s", len(paths), candidate.Name, targetLabel),
				Data:    map[string]any{"source": candidate.Name, "target_label": targetLabel, "paths": paths},
			})

			for _, path := range paths {
				doc := r.pathToDocument(candidate, path, targetLabel)
				documents = append(documents, doc)
			}
		}
	}

	return documents, nil
}

// retrieveWithShortestPath uses query analysis and shortest path navigation
func (r *GraphRAGRetriever) retrieveWithShortestPath(ctx context.Context, query string, topK int) ([]*schema.Document, error) {
	// Stage 1: Query Analysis
	r.emitDebug(DebugEvent{
		Type:    DebugEventStep,
		Message: "Stage 1: Query Analysis",
		Data:    map[string]any{"query": query},
	})

	analysis, err := r.queryAnalyzer.Analyze(ctx, query)
	if err != nil {
		// Fallback to legacy retrieval
		return r.retrieveLegacy(ctx, query, topK)
	}

	// Check if this is a hierarchical query (e.g., "하위 점수 모두")
	isHierarchical := r.DetectHierarchicalQuery(query)

	r.emitDebug(DebugEvent{
		Type:    DebugEventPlan,
		Message: fmt.Sprintf("Analysis: confidence=%.2f, targets=%v, hierarchical=%v", analysis.Confidence, analysis.Target.ExpectedLabels, isHierarchical),
		Data:    map[string]any{"analysis": analysis, "is_hierarchical": isHierarchical},
	})

	// Check if clarification is needed (low confidence)
	if tools.NeedsClarification(analysis) {
		r.emitDebug(DebugEvent{
			Type:    DebugEventClarification,
			Message: fmt.Sprintf("Clarification needed: %s", tools.ClarificationReason(analysis)),
			Data:    map[string]any{"reason": tools.ClarificationReason(analysis)},
		})
		// Continue with best effort - use keywords for search
	}

	// Stage 2: Full-Text Search using extracted keywords
	keywords := analysis.StartEntity.Keywords
	if len(keywords) == 0 {
		// Use original query
		keywords = []string{query}
	}

	searchQuery := strings.Join(keywords, " ")
	r.emitDebug(DebugEvent{
		Type:    DebugEventQuery,
		Message: fmt.Sprintf("Stage 2: Full-Text Search for '%s'", searchQuery),
		Data:    map[string]any{"keywords": keywords},
	})

	searchInput := tools.FullTextSearchInput{
		Query: searchQuery,
		TopK:  10,
	}
	searchInputJSON, _ := json.Marshal(searchInput)

	searchResultJSON, err := r.fullTextTool.InvokableRun(ctx, string(searchInputJSON))
	if err != nil {
		return nil, fmt.Errorf("full-text search failed: %w", err)
	}

	var searchResult tools.FullTextSearchResult
	if err := json.Unmarshal([]byte(searchResultJSON), &searchResult); err != nil {
		return nil, fmt.Errorf("failed to parse search result: %w", err)
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventRetrieval,
		Message: fmt.Sprintf("Found %d candidate nodes", len(searchResult.Candidates)),
		Data:    map[string]any{"candidates": searchResult.Candidates},
	})

	if len(searchResult.Candidates) == 0 {
		return []*schema.Document{}, nil
	}

	// Stage 3: Shortest Path Navigation to target labels
	targetLabels := analysis.Target.ExpectedLabels
	if len(targetLabels) == 0 {
		// Fallback to graph traversal
		return r.retrieveWithTraversal(ctx, searchResult.Candidates, topK)
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventStep,
		Message: fmt.Sprintf("Stage 3: Shortest Path to %v (hierarchical=%v)", targetLabels, isHierarchical),
		Data:    map[string]any{"target_labels": targetLabels, "is_hierarchical": isHierarchical},
	})

	documents := make([]*schema.Document, 0)
	processedPaths := make(map[string]bool) // Dedupe paths

	// For each top candidate, find paths to target labels
	maxCandidates := min(3, len(searchResult.Candidates))
	for _, candidate := range searchResult.Candidates[:maxCandidates] {
		// If hierarchical query, use hierarchical retrieval
		if isHierarchical {
			r.emitDebug(DebugEvent{
				Type:    DebugEventHierarchy,
				Message: fmt.Sprintf("Using hierarchical retrieval for %s", candidate.Name),
				Data:    map[string]any{"source": candidate.Name, "targets": targetLabels},
			})

			hierarchicalDocs, err := r.RetrieveWithHierarchy(ctx, query, candidate, targetLabels, 4)
			if err != nil {
				r.emitDebug(DebugEvent{
					Type:    DebugEventStep,
					Message: fmt.Sprintf("Hierarchical retrieval failed: %v, falling back to shortest path", err),
				})
				// Fall through to regular shortest path
			} else if len(hierarchicalDocs) > 0 {
				documents = append(documents, hierarchicalDocs...)
				// Hierarchical retrieval returns comprehensive data, so we're done
				return documents, nil
			}
		}

		// Regular shortest path retrieval
		for _, targetLabel := range targetLabels {
			paths, err := r.neo4jClient.ShortestPathToLabel(ctx, candidate.UUID, targetLabel, 6, topK)
			if err != nil {
				continue
			}

			r.emitDebug(DebugEvent{
				Type:    DebugEventShortestPath,
				Message: fmt.Sprintf("Paths from [%s]%s to %s: %d found", strings.Join(candidate.Labels, ","), candidate.Name, targetLabel, len(paths)),
				Data:    map[string]any{"source": candidate.Name, "target_label": targetLabel, "path_count": len(paths)},
			})

			for _, path := range paths {
				// Dedupe by end node UUID
				if processedPaths[path.EndNodeUUID] {
					continue
				}
				processedPaths[path.EndNodeUUID] = true

				doc := r.pathToDocument(candidate, path, targetLabel)
				documents = append(documents, doc)

				if len(documents) >= topK {
					break
				}
			}

			if len(documents) >= topK {
				break
			}
		}
		if len(documents) >= topK {
			break
		}
	}

	// If no paths found via shortest path, fall back to traversal
	if len(documents) == 0 {
		r.emitDebug(DebugEvent{
			Type:    DebugEventStep,
			Message: "No paths found, falling back to graph traversal",
		})
		return r.retrieveWithTraversal(ctx, searchResult.Candidates, topK)
	}

	return documents, nil
}

// pathToDocument converts a path result to a document
func (r *GraphRAGRetriever) pathToDocument(source tools.NodeCandidate, path db.PathResult, targetLabel string) *schema.Document {
	var content strings.Builder

	// Source node
	content.WriteString(fmt.Sprintf("Source: [%s] %s\n", strings.Join(source.Labels, ", "), source.Name))
	content.WriteString(fmt.Sprintf("Path: %s\n", path.PathChain))
	content.WriteString(fmt.Sprintf("Hops: %d\n\n", path.Hops))

	// Target node details
	content.WriteString(fmt.Sprintf("Target: [%s] %s\n", strings.Join(path.EndNodeLabels, ", "), path.EndNodeName))
	if len(path.EndProperties) > 0 {
		content.WriteString("Properties:\n")
		for k, v := range path.EndProperties {
			// Skip internal properties
			if strings.HasPrefix(k, "_") || k == "uuid" {
				continue
			}
			content.WriteString(fmt.Sprintf("  %s: %v\n", k, v))
		}
	}

	return &schema.Document{
		ID:      path.EndNodeUUID,
		Content: content.String(),
		MetaData: map[string]any{
			"source_uuid":   source.UUID,
			"source_name":   source.Name,
			"target_label":  targetLabel,
			"path_chain":    path.PathChain,
			"hops":          path.Hops,
			"target_labels": path.EndNodeLabels,
			"target_props":  path.EndProperties,
			"source":        "graph_rag_shortest_path",
		},
	}
}

// retrieveWithTraversal falls back to simple graph traversal
func (r *GraphRAGRetriever) retrieveWithTraversal(ctx context.Context, candidates []tools.NodeCandidate, topK int) ([]*schema.Document, error) {
	selectedNodes := r.candidatesToSelectedNodes(candidates, topK)

	documents := make([]*schema.Document, 0)
	for i, node := range selectedNodes {
		traversalInput := tools.GraphTraversalInput{
			StartUUID: node.UUID,
			Direction: "OUTGOING",
			MaxDepth:  1,
		}
		traversalInputJSON, _ := json.Marshal(traversalInput)

		traversalResultJSON, err := r.graphTool.InvokableRun(ctx, string(traversalInputJSON))
		if err != nil {
			doc := r.nodeToDocument(node, nil)
			documents = append(documents, doc)
			continue
		}

		var traversalResult tools.GraphTraversalOutput
		if err := json.Unmarshal([]byte(traversalResultJSON), &traversalResult); err != nil {
			doc := r.nodeToDocument(node, nil)
			documents = append(documents, doc)
			continue
		}

		doc := r.nodeToDocument(node, &traversalResult)
		doc = doc.WithScore(node.Confidence)
		documents = append(documents, doc)

		r.emitDebug(DebugEvent{
			Type:    DebugEventRetrieval,
			Message: fmt.Sprintf("Node %d: [%s] %s with %d relationships", i+1, strings.Join(node.Labels, ","), node.Name, len(traversalResult.Paths)),
			Data:    map[string]any{"node": node, "paths": traversalResult.Paths},
		})
	}

	return documents, nil
}

// retrieveLegacy is the original retrieval method
func (r *GraphRAGRetriever) retrieveLegacy(ctx context.Context, query string, topK int) ([]*schema.Document, error) {
	// Stage 1: Full-Text Search for candidate generation
	r.emitDebug(DebugEvent{
		Type:    DebugEventQuery,
		Message: fmt.Sprintf("Stage 1: Full-Text Search for '%s'", query),
		Data:    map[string]any{"query": query, "top_k": 20},
	})

	searchInput := tools.FullTextSearchInput{
		Query: query,
		TopK:  20, // Get more candidates for re-ranking
	}
	searchInputJSON, _ := json.Marshal(searchInput)

	searchResultJSON, err := r.fullTextTool.InvokableRun(ctx, string(searchInputJSON))
	if err != nil {
		return nil, fmt.Errorf("full-text search failed: %w", err)
	}

	var searchResult tools.FullTextSearchResult
	if err := json.Unmarshal([]byte(searchResultJSON), &searchResult); err != nil {
		return nil, fmt.Errorf("failed to parse search result: %w", err)
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventRetrieval,
		Message: fmt.Sprintf("Found %d candidate nodes", len(searchResult.Candidates)),
		Data:    map[string]any{"candidates": searchResult.Candidates},
	})

	if len(searchResult.Candidates) == 0 {
		return []*schema.Document{}, nil
	}

	// Stage 2: LLM Re-ranking (if enabled)
	var selectedNodes []tools.SelectedNode
	if r.enableReranking && len(searchResult.Candidates) > 1 {
		r.emitDebug(DebugEvent{
			Type:    DebugEventStep,
			Message: "Stage 2: LLM Re-ranking",
			Data:    map[string]any{"candidate_count": len(searchResult.Candidates)},
		})

		rerankerInput := tools.RerankerInput{
			UserQuery:  query,
			Candidates: searchResult.Candidates,
			TopN:       topK,
		}
		rerankerInputJSON, _ := json.Marshal(rerankerInput)

		rerankerResultJSON, err := r.rerankerTool.InvokableRun(ctx, string(rerankerInputJSON))
		if err != nil {
			// Fallback to search results
			selectedNodes = r.candidatesToSelectedNodes(searchResult.Candidates, topK)
		} else {
			var rerankerResult tools.RerankerOutput
			if err := json.Unmarshal([]byte(rerankerResultJSON), &rerankerResult); err != nil {
				selectedNodes = r.candidatesToSelectedNodes(searchResult.Candidates, topK)
			} else {
				selectedNodes = rerankerResult.SelectedNodes
				r.emitDebug(DebugEvent{
					Type:    DebugEventStep,
					Message: fmt.Sprintf("Re-ranked: Selected %d nodes. Reason: %s", len(selectedNodes), rerankerResult.Reasoning),
					Data:    map[string]any{"selected_nodes": selectedNodes, "reasoning": rerankerResult.Reasoning},
				})
			}
		}
	} else {
		// Skip re-ranking, use top candidates by score
		selectedNodes = r.candidatesToSelectedNodes(searchResult.Candidates, topK)
	}

	// Stage 3: Graph Traversal for context expansion
	r.emitDebug(DebugEvent{
		Type:    DebugEventStep,
		Message: "Stage 3: Graph Traversal",
		Data:    map[string]any{"selected_count": len(selectedNodes), "max_depth": r.maxGraphDepth},
	})

	documents := make([]*schema.Document, 0)
	for i, node := range selectedNodes {
		// Get graph context for each selected node
		// Use OUTGOING direction to avoid excessive paths (token overflow)
		// Limit max_depth to 1 for initial context
		traversalInput := tools.GraphTraversalInput{
			StartUUID: node.UUID,
			Direction: "OUTGOING",
			MaxDepth:  1, // Reduced from r.maxGraphDepth to prevent token overflow
		}
		traversalInputJSON, _ := json.Marshal(traversalInput)

		traversalResultJSON, err := r.graphTool.InvokableRun(ctx, string(traversalInputJSON))
		if err != nil {
			// Just use the node itself without graph context
			doc := r.nodeToDocument(node, nil)
			documents = append(documents, doc)
			continue
		}

		var traversalResult tools.GraphTraversalOutput
		if err := json.Unmarshal([]byte(traversalResultJSON), &traversalResult); err != nil {
			doc := r.nodeToDocument(node, nil)
			documents = append(documents, doc)
			continue
		}

		// Create document with graph context
		doc := r.nodeToDocument(node, &traversalResult)
		doc = doc.WithScore(node.Confidence)
		documents = append(documents, doc)

		r.emitDebug(DebugEvent{
			Type:    DebugEventRetrieval,
			Message: fmt.Sprintf("Node %d: [%s] %s with %d relationships", i+1, strings.Join(node.Labels, ","), node.Name, len(traversalResult.Paths)),
			Data:    map[string]any{"node": node, "paths": traversalResult.Paths},
		})
	}

	return documents, nil
}

// nodeToDocument converts a selected node and its graph context to a Document
func (r *GraphRAGRetriever) nodeToDocument(node tools.SelectedNode, graphContext *tools.GraphTraversalOutput) *schema.Document {
	var content strings.Builder

	// Node information
	content.WriteString(fmt.Sprintf("[%s] %s\n", strings.Join(node.Labels, ", "), node.Name))
	content.WriteString(fmt.Sprintf("UUID: %s\n", node.UUID))

	// Graph context - limit paths to prevent token overflow
	const maxPaths = 15
	if graphContext != nil && len(graphContext.Paths) > 0 {
		content.WriteString("\nRelated entities:\n")
		pathCount := len(graphContext.Paths)
		if pathCount > maxPaths {
			pathCount = maxPaths
		}
		for i := 0; i < pathCount; i++ {
			path := graphContext.Paths[i]
			// Only show relationship and node name (no Properties to save tokens)
			content.WriteString(fmt.Sprintf("  -[%s]-> [%s] %s\n",
				path.Relationship,
				strings.Join(path.EndNode.Labels, ","),
				path.EndNode.Name,
			))
		}
		if len(graphContext.Paths) > maxPaths {
			content.WriteString(fmt.Sprintf("  ... and %d more relationships\n", len(graphContext.Paths)-maxPaths))
		}
	}

	return &schema.Document{
		ID:      node.UUID,
		Content: content.String(),
		MetaData: map[string]any{
			"labels":     node.Labels,
			"name":       node.Name,
			"confidence": node.Confidence,
			"source":     "graph_rag",
		},
	}
}

// candidatesToSelectedNodes converts search candidates to selected nodes
func (r *GraphRAGRetriever) candidatesToSelectedNodes(candidates []tools.NodeCandidate, topN int) []tools.SelectedNode {
	result := make([]tools.SelectedNode, 0, topN)
	for i := 0; i < len(candidates) && i < topN; i++ {
		c := candidates[i]
		result = append(result, tools.SelectedNode{
			UUID:       c.UUID,
			Labels:     c.Labels,
			Name:       c.Name,
			Confidence: c.Score / 10.0, // Normalize score
		})
	}
	return result
}

// emitDebug emits a debug event if emitter is set
func (r *GraphRAGRetriever) emitDebug(event DebugEvent) {
	if r.debugEmitter != nil {
		r.debugEmitter("debug:graph_rag", event)
	}
}

// GetType returns the retriever type
func (r *GraphRAGRetriever) GetType() string {
	return "GraphRAGRetriever"
}

// GetClarificationService returns the clarification service
func (r *GraphRAGRetriever) GetClarificationService() *ClarificationService {
	return r.clarificationService
}

// GetQueryAnalyzer returns the query analyzer tool
func (r *GraphRAGRetriever) GetQueryAnalyzer() *tools.QueryAnalyzerTool {
	return r.queryAnalyzer
}

// IsShortestPathEnabled returns whether shortest path mode is enabled
func (r *GraphRAGRetriever) IsShortestPathEnabled() bool {
	return r.enableShortestPath
}

// SearchInitialNodes performs full-text search for initial node candidates
// Uses label groups to expand search (e.g., "Vehicle" searches Vehicle, CompetitorVehicle, SimilarVehicle)
func (r *GraphRAGRetriever) SearchInitialNodes(
	ctx context.Context,
	query string,
	keywords []string,
	topK int,
) ([]tools.NodeCandidate, error) {
	if topK <= 0 {
		topK = 10
	}

	// Build search query from keywords or use original query
	searchQuery := strings.Join(keywords, " ")
	if searchQuery == "" {
		searchQuery = query
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventQuery,
		Message: fmt.Sprintf("SearchInitialNodes: query='%s'", searchQuery),
		Data:    map[string]any{"keywords": keywords},
	})

	// Search without label filter to get all matching nodes
	// Label groups will be used for re-ranking later
	results, err := r.neo4jClient.FullTextSearch(ctx, searchQuery, "", topK*2)
	if err != nil {
		return nil, fmt.Errorf("full-text search failed: %w", err)
	}

	candidates := make([]tools.NodeCandidate, 0, len(results))
	for _, r := range results {
		candidate := tools.NodeCandidate{
			UUID: r["uuid"].(string),
			Name: r["name"].(string),
		}

		if labels, ok := r["labels"].([]any); ok {
			candidate.Labels = make([]string, 0, len(labels))
			for _, l := range labels {
				if label, ok := l.(string); ok {
					candidate.Labels = append(candidate.Labels, label)
				}
			}
		}

		if score, ok := r["score"].(float64); ok {
			candidate.Score = score
		}

		candidates = append(candidates, candidate)
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventRetrieval,
		Message: fmt.Sprintf("Found %d candidates", len(candidates)),
		Data:    map[string]any{"candidates": candidates},
	})

	return candidates, nil
}

// SearchInitialNodesWithLabelHint searches with a label hint that expands to related labels
// e.g., labelHint="Vehicle" will search Vehicle, CompetitorVehicle, SimilarVehicle
func (r *GraphRAGRetriever) SearchInitialNodesWithLabelHint(
	ctx context.Context,
	query string,
	keywords []string,
	labelHint string,
	topK int,
) ([]tools.NodeCandidate, error) {
	if topK <= 0 {
		topK = 10
	}

	searchQuery := strings.Join(keywords, " ")
	if searchQuery == "" {
		searchQuery = query
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventQuery,
		Message: fmt.Sprintf("SearchInitialNodesWithLabelHint: query='%s', labelHint='%s'", searchQuery, labelHint),
		Data:    map[string]any{"keywords": keywords, "label_hint": labelHint},
	})

	// Use label group search if label hint is provided
	var results []map[string]any
	var err error

	if labelHint != "" {
		results, err = r.neo4jClient.FullTextSearchWithLabelGroup(ctx, searchQuery, labelHint, topK*2)
	} else {
		results, err = r.neo4jClient.FullTextSearch(ctx, searchQuery, "", topK*2)
	}

	if err != nil {
		return nil, fmt.Errorf("full-text search failed: %w", err)
	}

	candidates := make([]tools.NodeCandidate, 0, len(results))
	for _, r := range results {
		candidate := tools.NodeCandidate{
			UUID: r["uuid"].(string),
			Name: r["name"].(string),
		}

		if labels, ok := r["labels"].([]any); ok {
			candidate.Labels = make([]string, 0, len(labels))
			for _, l := range labels {
				if label, ok := l.(string); ok {
					candidate.Labels = append(candidate.Labels, label)
				}
			}
		}

		if score, ok := r["score"].(float64); ok {
			candidate.Score = score
		}

		candidates = append(candidates, candidate)
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventRetrieval,
		Message: fmt.Sprintf("Found %d candidates with label hint '%s'", len(candidates), labelHint),
		Data:    map[string]any{"candidates": candidates, "related_labels": db.GetRelatedLabels(labelHint)},
	})

	return candidates, nil
}

// RetrieveWithSourceAndTargets retrieves documents starting from a source node to target labels
func (r *GraphRAGRetriever) RetrieveWithSourceAndTargets(
	ctx context.Context,
	sourceUUID string,
	targetLabels []string,
	topK int,
) ([]*schema.Document, error) {
	if topK <= 0 {
		topK = r.defaultTopK
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventStep,
		Message: fmt.Sprintf("RetrieveWithSourceAndTargets: source=%s, targets=%v", sourceUUID, targetLabels),
		Data:    map[string]any{"source_uuid": sourceUUID, "target_labels": targetLabels},
	})

	// Get source node info for document creation
	sourceNodeMap, err := r.neo4jClient.GetNodeByUUID(ctx, sourceUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get source node: %w", err)
	}

	sourceNode := tools.NodeCandidate{
		UUID: sourceUUID,
		Name: sourceNodeMap["properties"].(map[string]any)["name"].(string),
	}
	if labels, ok := sourceNodeMap["labels"].([]any); ok {
		sourceNode.Labels = make([]string, 0, len(labels))
		for _, l := range labels {
			if label, ok := l.(string); ok {
				sourceNode.Labels = append(sourceNode.Labels, label)
			}
		}
	}

	// Find shortest paths to each target label
	documents := make([]*schema.Document, 0)
	processedPaths := make(map[string]bool)

	for _, targetLabel := range targetLabels {
		// Expand target label to related labels (for target too)
		relatedLabels := db.GetRelatedLabels(targetLabel)

		for _, relLabel := range relatedLabels {
			paths, err := r.neo4jClient.ShortestPathToLabel(ctx, sourceUUID, relLabel, 6, topK)
			if err != nil {
				continue
			}

			r.emitDebug(DebugEvent{
				Type:    DebugEventShortestPath,
				Message: fmt.Sprintf("Found %d paths from %s to %s", len(paths), sourceNode.Name, relLabel),
				Data:    map[string]any{"source": sourceNode.Name, "target_label": relLabel, "paths": paths},
			})

			for _, path := range paths {
				if processedPaths[path.EndNodeUUID] {
					continue
				}
				processedPaths[path.EndNodeUUID] = true

				doc := r.pathToDocument(sourceNode, path, relLabel)
				documents = append(documents, doc)

				if len(documents) >= topK {
					return documents, nil
				}
			}
		}
	}

	// If no paths found, try graph traversal
	if len(documents) == 0 {
		r.emitDebug(DebugEvent{
			Type:    DebugEventStep,
			Message: "No shortest paths found, falling back to traversal",
		})

		traversalInput := tools.GraphTraversalInput{
			StartUUID: sourceUUID,
			Direction: "OUTGOING",
			MaxDepth:  2,
		}
		traversalInputJSON, _ := json.Marshal(traversalInput)

		traversalResultJSON, err := r.graphTool.InvokableRun(ctx, string(traversalInputJSON))
		if err == nil {
			var traversalResult tools.GraphTraversalOutput
			if json.Unmarshal([]byte(traversalResultJSON), &traversalResult) == nil {
				selectedNode := tools.SelectedNode{
					UUID:   sourceNode.UUID,
					Labels: sourceNode.Labels,
					Name:   sourceNode.Name,
				}
				doc := r.nodeToDocument(selectedNode, &traversalResult)
				documents = append(documents, doc)
			}
		}
	}

	return documents, nil
}

// DetectHierarchicalQuery checks if query asks for hierarchical data
// Returns true if the query contains keywords indicating hierarchical/nested data request
func (r *GraphRAGRetriever) DetectHierarchicalQuery(query string) bool {
	lowerQuery := strings.ToLower(query)
	for _, kw := range hierarchicalKeywords {
		if strings.Contains(lowerQuery, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// RetrieveWithHierarchy performs hierarchical retrieval for queries asking for nested data
// This is the main entry point for hierarchical queries like "하위 점수 모두 알려줘"
func (r *GraphRAGRetriever) RetrieveWithHierarchy(
	ctx context.Context,
	query string,
	sourceNode tools.NodeCandidate,
	targetLabels []string,
	maxDepth int,
) ([]*schema.Document, error) {
	if maxDepth <= 0 {
		maxDepth = 4 // Default: 4 levels deep for score hierarchies
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventHierarchy,
		Message: fmt.Sprintf("Starting hierarchical retrieval from %s to %v (depth=%d)", sourceNode.Name, targetLabels, maxDepth),
		Data:    map[string]any{"source": sourceNode.Name, "targets": targetLabels, "max_depth": maxDepth},
	})

	// Step 1: Find path to target label to get target node UUID
	var targetNodeUUID string
	var targetLabel string

	for _, tl := range targetLabels {
		paths, err := r.neo4jClient.ShortestPathToLabel(ctx, sourceNode.UUID, tl, 6, 1)
		if err != nil || len(paths) == 0 {
			continue
		}

		targetNodeUUID = paths[0].EndNodeUUID
		targetLabel = tl

		r.emitDebug(DebugEvent{
			Type:    DebugEventShortestPath,
			Message: fmt.Sprintf("Found path to %s: %s", tl, paths[0].PathChain),
			Data:    map[string]any{"path": paths[0]},
		})
		break
	}

	if targetNodeUUID == "" {
		return nil, fmt.Errorf("no path found to target labels: %v", targetLabels)
	}

	// Step 2: Get hierarchical children from target node
	_, children, err := r.neo4jClient.GetPerformanceScoreDetails(ctx, targetNodeUUID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("failed to get hierarchical children: %w", err)
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventHierarchy,
		Message: fmt.Sprintf("Retrieved %d hierarchical nodes", len(children)),
		Data:    map[string]any{"children_count": len(children)},
	})

	// Step 3: Build hierarchical tree from flat list
	tree := r.buildHierarchicalTree(targetNodeUUID, children)

	// Step 4: Convert to structured document
	doc := r.hierarchyToDocument(sourceNode, tree, targetLabel)

	return []*schema.Document{doc}, nil
}

// buildHierarchicalTree builds a tree structure from flat hierarchical nodes
func (r *GraphRAGRetriever) buildHierarchicalTree(rootUUID string, nodes []*db.HierarchicalNode) *db.HierarchicalNode {
	// Create a map for quick lookup
	nodeMap := make(map[string]*db.HierarchicalNode)
	var root *db.HierarchicalNode

	// First pass: create all nodes
	for _, node := range nodes {
		nodeMap[node.UUID] = node
		if node.UUID == rootUUID || node.Depth == 0 {
			root = node
		}
	}

	// If no root found by UUID, use depth 0 or first node
	if root == nil && len(nodes) > 0 {
		for _, node := range nodes {
			if node.Depth == 0 {
				root = node
				break
			}
		}
		if root == nil {
			root = nodes[0]
		}
	}

	// Second pass: build relationships based on depth
	// Group children by depth
	depthGroups := make(map[int][]*db.HierarchicalNode)
	for _, node := range nodes {
		if node.UUID != rootUUID {
			depthGroups[node.Depth] = append(depthGroups[node.Depth], node)
		}
	}

	// Attach children to root (depth 1 nodes are direct children of root)
	if root != nil && len(depthGroups[1]) > 0 {
		root.Children = depthGroups[1]
	}

	// Attach deeper levels (simplified: attach all depth N nodes to depth N-1 nodes that seem related)
	for depth := 2; depth <= 5; depth++ {
		parentNodes := depthGroups[depth-1]
		childNodes := depthGroups[depth]

		for _, child := range childNodes {
			// Try to find parent by name matching or just attach to first available parent
			attached := false
			for _, parent := range parentNodes {
				// Check if child name contains parent name keywords
				if r.isLikelyChild(parent, child) {
					parent.Children = append(parent.Children, child)
					attached = true
					break
				}
			}
			// If no match found, attach to first parent at previous depth
			if !attached && len(parentNodes) > 0 {
				parentNodes[0].Children = append(parentNodes[0].Children, child)
			}
		}
	}

	return root
}

// isLikelyChild checks if child node is likely a child of parent based on naming patterns
func (r *GraphRAGRetriever) isLikelyChild(parent, child *db.HierarchicalNode) bool {
	// Check label relationships
	parentLabel := ""
	childLabel := ""
	if len(parent.Labels) > 0 {
		parentLabel = parent.Labels[0]
	}
	if len(child.Labels) > 0 {
		childLabel = child.Labels[0]
	}

	// Score hierarchy patterns
	scorePatterns := map[string][]string{
		"PerformanceScore":         {"PTTotalScore", "ComfortTotalScore", "BodyTotalScore", "DynamicTotalScore", "EnvironmentTotalScore"},
		"PerformanceTotalScore":    {"PerformanceScore", "ConnectedCarTotalScore"},
		"PTTotalScore":             {"발진가속", "추월성능", "최고속도", "NVH", "응답성", "변속기", "충전주유", "테스트연비", "주행거리"},
		"ComfortTotalScore":        {"좌석", "내부공간", "공조", "승차감", "편의성"},
		"BodyTotalScore":           {"트렁크", "품질", "조작계", "시야", "조명"},
		"DynamicTotalScore":        {"조향", "핸들링", "제동", "엔진", "안전"},
		"EnvironmentTotalScore":    {"탄소배출", "배기가스", "타이어마모", "미세먼지"},
		"ConnectedCarTotalScore":   {"텔레폰", "내비게이션", "온라인앱", "오디오", "음성제어"},
		"AutobildTotalScore":       {"PerformanceTotalScore", "CostTotalScore"},
	}

	if expectedChildren, ok := scorePatterns[parentLabel]; ok {
		for _, expected := range expectedChildren {
			if strings.Contains(childLabel, expected) || strings.Contains(child.Name, expected) {
				return true
			}
		}
	}

	return false
}

// hierarchyToDocument converts a hierarchical structure to a well-formatted document
func (r *GraphRAGRetriever) hierarchyToDocument(
	source tools.NodeCandidate,
	hierarchy *db.HierarchicalNode,
	targetLabel string,
) *schema.Document {
	var content strings.Builder

	// Title section
	content.WriteString(fmt.Sprintf("# %s의 %s 정보\n\n", source.Name, r.getLabelDisplayName(targetLabel)))

	if hierarchy == nil {
		content.WriteString("데이터를 찾을 수 없습니다.\n")
		return &schema.Document{
			ID:      source.UUID,
			Content: content.String(),
			MetaData: map[string]any{
				"source":       source.Name,
				"target_label": targetLabel,
				"hierarchy":    false,
			},
		}
	}

	// Top-level score
	value := r.getPropertyValue(hierarchy.Properties, "value", "")
	maxScore := r.getPropertyValue(hierarchy.Properties, "최대 점수", "")
	if value != "" && maxScore != "" {
		ratio := r.calculateRatio(value, maxScore)
		content.WriteString(fmt.Sprintf("## %s: %v / %v (%.1f%%)\n\n", hierarchy.Name, value, maxScore, ratio))
	} else {
		content.WriteString(fmt.Sprintf("## %s\n\n", hierarchy.Name))
	}

	// Build category table for direct children
	if len(hierarchy.Children) > 0 {
		content.WriteString("| 카테고리 | 점수 | 만점 | 비율 |\n")
		content.WriteString("|----------|------|------|------|\n")

		for _, child := range hierarchy.Children {
			childValue := r.getPropertyValue(child.Properties, "value", "-")
			childMax := r.getPropertyValue(child.Properties, "최대 점수", "-")
			childRatio := r.calculateRatio(childValue, childMax)

			if childRatio > 0 {
				content.WriteString(fmt.Sprintf("| %s | %v | %v | %.1f%% |\n",
					child.Name, childValue, childMax, childRatio))
			} else {
				content.WriteString(fmt.Sprintf("| %s | %v | %v | - |\n",
					child.Name, childValue, childMax))
			}
		}
		content.WriteString("\n")

		// Add detailed breakdown for each category
		for _, child := range hierarchy.Children {
			if len(child.Children) > 0 {
				childValue := r.getPropertyValue(child.Properties, "value", "")
				childMax := r.getPropertyValue(child.Properties, "최대 점수", "")
				if childValue != "" && childMax != "" {
					content.WriteString(fmt.Sprintf("### %s 세부 (%v/%v점)\n", child.Name, childValue, childMax))
				} else {
					content.WriteString(fmt.Sprintf("### %s 세부\n", child.Name))
				}

				// Group items in rows of 3 for readability
				items := make([]string, 0)
				for _, grandchild := range child.Children {
					gcValue := r.getPropertyValue(grandchild.Properties, "value", "-")
					gcMax := r.getPropertyValue(grandchild.Properties, "최대 점수", "-")
					items = append(items, fmt.Sprintf("%s: %v/%v", grandchild.Name, gcValue, gcMax))
				}

				// Write items, 3 per line
				for i := 0; i < len(items); i += 3 {
					end := i + 3
					if end > len(items) {
						end = len(items)
					}
					content.WriteString("- " + strings.Join(items[i:end], " | ") + "\n")
				}
				content.WriteString("\n")
			}
		}
	}

	// Add properties if available
	if len(hierarchy.Properties) > 0 && len(hierarchy.Children) == 0 {
		content.WriteString("### 속성\n")
		for k, v := range hierarchy.Properties {
			if strings.HasPrefix(k, "_") || k == "uuid" || k == "embedding" {
				continue
			}
			content.WriteString(fmt.Sprintf("- %s: %v\n", k, v))
		}
	}

	return &schema.Document{
		ID:      hierarchy.UUID,
		Content: content.String(),
		MetaData: map[string]any{
			"source_uuid":   source.UUID,
			"source_name":   source.Name,
			"target_label":  targetLabel,
			"hierarchy":     true,
			"children_count": len(hierarchy.Children),
			"source":        "graph_rag_hierarchy",
		},
	}
}

// getLabelDisplayName returns a human-readable name for a label
func (r *GraphRAGRetriever) getLabelDisplayName(label string) string {
	displayNames := map[string]string{
		"PerformanceTotalScore":    "성능 총점",
		"PerformanceScore":         "성능 점수",
		"AutobildTotalScore":       "Autobild 총점",
		"CostTotalScore":           "비용 총점",
		"PTTotalScore":             "PT 총점",
		"ComfortTotalScore":        "컴포트 총점",
		"BodyTotalScore":           "바디 총점",
		"DynamicTotalScore":        "다이나믹 총점",
		"EnvironmentTotalScore":    "환경 총점",
		"ConnectedCarTotalScore":   "커넥티드카 총점",
		"Vehicle":                  "차량",
		"CompetitorVehicle":        "경쟁 차량",
	}

	if name, ok := displayNames[label]; ok {
		return name
	}
	return label
}

// getPropertyValue safely retrieves a property value
func (r *GraphRAGRetriever) getPropertyValue(props map[string]any, key string, defaultVal string) string {
	if props == nil {
		return defaultVal
	}
	if v, ok := props[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return defaultVal
}

// calculateRatio calculates percentage from value/maxScore
func (r *GraphRAGRetriever) calculateRatio(value, maxScore any) float64 {
	var val, max float64

	switch v := value.(type) {
	case float64:
		val = v
	case int64:
		val = float64(v)
	case int:
		val = float64(v)
	case string:
		fmt.Sscanf(v, "%f", &val)
	}

	switch m := maxScore.(type) {
	case float64:
		max = m
	case int64:
		max = float64(m)
	case int:
		max = float64(m)
	case string:
		fmt.Sscanf(m, "%f", &max)
	}

	if max > 0 {
		return (val / max) * 100
	}
	return 0
}

// Ensure GraphRAGRetriever implements the Retriever interface
var _ retriever.Retriever = (*GraphRAGRetriever)(nil)
