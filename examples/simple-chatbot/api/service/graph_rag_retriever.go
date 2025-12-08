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
	conditionalRetriever *ConditionalRetriever // Exploration-first conditional query handler
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

	// Initialize full-text tool (shared by multiple components)
	fullTextTool := tools.NewNeo4jFullTextSearchTool(config.Neo4jClient)

	// Initialize conditional retriever for exploration-first queries
	conditionalRetriever := NewConditionalRetriever(&ConditionalRetrieverConfig{
		Neo4jClient:  config.Neo4jClient,
		FullTextTool: fullTextTool,
		DebugEmitter: config.DebugEmitter,
	})

	return &GraphRAGRetriever{
		neo4jClient:          config.Neo4jClient,
		chatModel:            config.ChatModel,
		fullTextTool:         fullTextTool,
		rerankerTool:         tools.NewLLMRerankerTool(config.ChatModel),
		graphTool:            tools.NewNeo4jGraphTraversalTool(config.Neo4jClient),
		shortestPathTool:     tools.NewNeo4jShortestPathTool(config.Neo4jClient),
		queryAnalyzer:        queryAnalyzer,
		clarificationService: clarificationSvc,
		conditionalRetriever: conditionalRetriever,
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

	// NEW: Check for label listing query (e.g., "모든 경쟁차 알려줘")
	// This must be checked BEFORE other paths since label listing doesn't need keyword search
	if analysis.IsLabelListing {
		r.emitDebug(DebugEvent{
			Type:    DebugEventStep,
			Message: fmt.Sprintf("Label listing query detected: labels=%v, limit=%d",
				analysis.ListingLabels, analysis.ListingLimit),
			Data: map[string]any{
				"listing_labels":  analysis.ListingLabels,
				"listing_limit":   analysis.ListingLimit,
				"resolved_labels": analysis.ResolvedLabels,
			},
		})
		return r.retrieveWithLabelListing(ctx, query, analysis, topK)
	}

	// NEW: Check for conditional query (e.g., "Honda가 생산하는 모든 차량")
	// This uses exploration-first approach: find anchor -> explore relations -> traverse
	if analysis.IsConditionalQuery && analysis.ConditionalAnchor != nil {
		r.emitDebug(DebugEvent{
			Type:    DebugEventStep,
			Message: fmt.Sprintf("Conditional query detected: anchor=%v, condition=%s",
				analysis.ConditionalAnchor.Keywords, analysis.ConditionalAnchor.ConditionType),
			Data: map[string]any{
				"conditional_anchor":  analysis.ConditionalAnchor,
				"conditional_filters": analysis.ConditionalFilters,
			},
		})
		return r.conditionalRetriever.RetrieveWithCondition(
			ctx, query, analysis.ConditionalAnchor, analysis.ConditionalFilters, topK)
	}

	// Check if this is a hierarchical query (e.g., "하위 점수 모두")
	isHierarchical := r.DetectHierarchicalQuery(query)

	r.emitDebug(DebugEvent{
		Type:    DebugEventPlan,
		Message: fmt.Sprintf("Analysis: confidence=%.2f, targets=%v, hierarchical=%v, strategy=%s",
			analysis.Confidence, analysis.Target.ExpectedLabels, isHierarchical, analysis.SearchStrategy),
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

	// Stage 2: Search based on strategy from analysis
	keywords := analysis.StartEntity.Keywords
	if len(keywords) == 0 {
		// Use original query
		keywords = []string{query}
	}

	searchQuery := strings.Join(keywords, " ")

	// Determine search strategy
	searchStrategy := analysis.SearchStrategy
	if searchStrategy == "" {
		searchStrategy = "generic" // Default to generic search
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventQuery,
		Message: fmt.Sprintf("Stage 2: Search with strategy '%s' for '%s'", searchStrategy, searchQuery),
		Data:    map[string]any{"keywords": keywords, "strategy": searchStrategy},
	})

	// Execute search based on strategy
	var candidates []tools.NodeCandidate
	var searchErr error

	switch searchStrategy {
	case "exact", "fuzzy", "generic":
		// Use generic search with fallback (handles exact, wildcard, fuzzy)
		candidates, searchErr = r.fullTextTool.SearchWithFallback(ctx, searchQuery, "", topK*2)
	case "label_filtered":
		// Use label-filtered search if expected labels are available
		labelHint := ""
		if len(analysis.StartEntity.ExpectedLabels) > 0 {
			labelHint = analysis.StartEntity.ExpectedLabels[0]
		}
		candidates, searchErr = r.fullTextTool.SearchWithFallback(ctx, searchQuery, labelHint, topK*2)
	default:
		// Default fallback to generic search
		candidates, searchErr = r.fullTextTool.SearchWithFallback(ctx, searchQuery, "", topK*2)
	}

	if searchErr != nil {
		return nil, fmt.Errorf("search failed: %w", searchErr)
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventRetrieval,
		Message: fmt.Sprintf("Found %d candidate nodes", len(candidates)),
		Data:    map[string]any{"candidates": candidates},
	})

	if len(candidates) == 0 {
		// For exploration queries with no results, return informative message
		if analysis.QueryType == "exploration" {
			return []*schema.Document{{
				ID:      "no_results",
				Content: fmt.Sprintf("검색어 '%s'에 해당하는 데이터를 찾지 못했습니다.", searchQuery),
				MetaData: map[string]any{
					"source": "graph_rag_no_results",
				},
			}}, nil
		}
		return []*schema.Document{}, nil
	}

	// Enrich candidates with variant info (engine, trim, year) for disambiguation
	// This is essential when multiple variants of the same vehicle exist (e.g., Tucson 1.6T vs 2.0D)
	candidates = r.enrichCandidatesWithVariant(ctx, candidates)

	// For exploration queries without target labels, return node info with neighbors
	targetLabels := analysis.Target.ExpectedLabels
	if len(targetLabels) == 0 || analysis.QueryType == "exploration" {
		r.emitDebug(DebugEvent{
			Type:    DebugEventStep,
			Message: "No target labels - using exploration mode",
		})
		return r.retrieveWithExploration(ctx, candidates, topK)
	}

	// Stage 3: Shortest Path Navigation to target labels
	r.emitDebug(DebugEvent{
		Type:    DebugEventStep,
		Message: fmt.Sprintf("Stage 3: Shortest Path to %v (hierarchical=%v)", targetLabels, isHierarchical),
		Data:    map[string]any{"target_labels": targetLabels, "is_hierarchical": isHierarchical},
	})

	documents := make([]*schema.Document, 0)
	processedPaths := make(map[string]bool) // Dedupe paths

	// For each top candidate, find paths to target labels
	maxCandidates := min(3, len(candidates))
	for _, candidate := range candidates[:maxCandidates] {
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
		return r.retrieveWithTraversal(ctx, candidates, topK)
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

// retrieveWithExploration retrieves documents for exploration queries
// Used when no specific target labels are provided - returns node info with neighbor context
func (r *GraphRAGRetriever) retrieveWithExploration(ctx context.Context, candidates []tools.NodeCandidate, topK int) ([]*schema.Document, error) {
	r.emitDebug(DebugEvent{
		Type:    DebugEventStep,
		Message: fmt.Sprintf("Exploration mode: processing %d candidates", len(candidates)),
	})

	documents := make([]*schema.Document, 0)
	maxCandidates := min(topK, len(candidates))

	for _, candidate := range candidates[:maxCandidates] {
		// Build document with node info and neighbor context
		doc := r.candidateToExplorationDocument(candidate)
		documents = append(documents, doc)
	}

	return documents, nil
}

// candidateToExplorationDocument creates a document for exploration queries
// Includes node properties and neighbor relationships for comprehensive context
func (r *GraphRAGRetriever) candidateToExplorationDocument(candidate tools.NodeCandidate) *schema.Document {
	var content strings.Builder

	// Header with node info
	content.WriteString(fmt.Sprintf("# [%s] %s\n\n", strings.Join(candidate.Labels, ", "), candidate.Name))

	// Properties section
	if len(candidate.Properties) > 0 {
		content.WriteString("## 속성 (Properties)\n")
		for k, v := range candidate.Properties {
			// Skip internal properties
			if strings.HasPrefix(k, "_") || k == "uuid" || k == "embedding" {
				continue
			}
			content.WriteString(fmt.Sprintf("- **%s**: %v\n", k, v))
		}
		content.WriteString("\n")
	}

	// Neighbor relationships section
	if len(candidate.NeighborSummary) > 0 {
		content.WriteString("## 연결된 노드 (Related Nodes)\n")
		for _, neighbor := range candidate.NeighborSummary {
			direction := "→"
			if !neighbor.Outgoing {
				direction = "←"
			}
			content.WriteString(fmt.Sprintf("- %s [%s] **%s** ([%s])\n",
				direction,
				neighbor.Relationship,
				neighbor.Name,
				strings.Join(neighbor.Labels, ", ")))
		}
		content.WriteString("\n")
	}

	// Variant info for vehicles
	if candidate.VariantInfo != nil && !candidate.VariantInfo.IsEmpty() {
		content.WriteString("## 변형 정보 (Variant Info)\n")
		content.WriteString(fmt.Sprintf("- %s\n", candidate.VariantInfo.FormatDisplay()))
	}

	return &schema.Document{
		ID:      candidate.UUID,
		Content: content.String(),
		MetaData: map[string]any{
			"labels":     candidate.Labels,
			"name":       candidate.Name,
			"score":      candidate.Score,
			"source":     "graph_rag_exploration",
			"properties": candidate.Properties,
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

		// Extract properties and variant info
		if props, ok := r["properties"].(map[string]any); ok {
			candidate.Properties = props
			candidate.VariantInfo = db.ExtractVariantInfo(props)
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

		// Extract properties and variant info
		if props, ok := r["properties"].(map[string]any); ok {
			candidate.Properties = props
			candidate.VariantInfo = db.ExtractVariantInfo(props)
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
	root, children, err := r.neo4jClient.GetPerformanceScoreDetails(ctx, targetNodeUUID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("failed to get hierarchical children: %w", err)
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventHierarchy,
		Message: fmt.Sprintf("Retrieved %d hierarchical nodes", len(children)),
		Data:    map[string]any{"children_count": len(children)},
	})

	// Step 3: Build hierarchical tree from flat list (include root in nodes)
	allNodes := append([]*db.HierarchicalNode{root}, children...)
	tree := r.buildHierarchicalTree(root.UUID, allNodes)

	// Step 4: Convert to structured document
	doc := r.hierarchyToDocument(sourceNode, tree, targetLabel)

	return []*schema.Document{doc}, nil
}

// RetrieveWithHierarchyByUUID performs hierarchical retrieval starting from a known source UUID
// Used after clarification when source node is already selected by the user
func (r *GraphRAGRetriever) RetrieveWithHierarchyByUUID(
	ctx context.Context,
	sourceUUID string,
	sourceName string,
	targetLabels []string,
	maxDepth int,
) ([]*schema.Document, error) {
	if maxDepth <= 0 {
		maxDepth = 4 // Default: 4 levels deep for score hierarchies
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventHierarchy,
		Message: fmt.Sprintf("Starting hierarchical retrieval by UUID from %s (%s) to %v (depth=%d)", sourceName, sourceUUID[:8], targetLabels, maxDepth),
		Data:    map[string]any{"source_uuid": sourceUUID, "source_name": sourceName, "targets": targetLabels, "max_depth": maxDepth},
	})

	// Create a minimal source node candidate
	sourceNode := tools.NodeCandidate{
		UUID: sourceUUID,
		Name: sourceName,
	}

	var documents []*schema.Document

	for _, targetLabel := range targetLabels {
		// Step 1: Find path to target label to get target node UUID
		paths, err := r.neo4jClient.ShortestPathToLabel(ctx, sourceUUID, targetLabel, 6, 1)
		if err != nil || len(paths) == 0 {
			r.emitDebug(DebugEvent{
				Type:    DebugEventShortestPath,
				Message: fmt.Sprintf("No path found to %s, trying next label", targetLabel),
			})
			continue
		}

		targetNodeUUID := paths[0].EndNodeUUID

		r.emitDebug(DebugEvent{
			Type:    DebugEventShortestPath,
			Message: fmt.Sprintf("Found path to %s: %s", targetLabel, paths[0].PathChain),
			Data:    map[string]any{"path": paths[0]},
		})

		// Step 2: Get hierarchical children from target node
		root, children, err := r.neo4jClient.GetPerformanceScoreDetails(ctx, targetNodeUUID, maxDepth)
		if err != nil {
			r.emitDebug(DebugEvent{
				Type:    DebugEventHierarchy,
				Message: fmt.Sprintf("Failed to get hierarchical children: %v, falling back to path document", err),
			})
			// Fallback to simple path document
			doc := r.pathToDocument(sourceNode, paths[0], targetLabel)
			documents = append(documents, doc)
			continue
		}

		r.emitDebug(DebugEvent{
			Type:    DebugEventHierarchy,
			Message: fmt.Sprintf("Retrieved %d hierarchical nodes for %s", len(children), targetLabel),
			Data:    map[string]any{"children_count": len(children)},
		})

		// Step 3: Build hierarchical tree from flat list (include root in nodes)
		allNodes := append([]*db.HierarchicalNode{root}, children...)
		tree := r.buildHierarchicalTree(root.UUID, allNodes)

		// Step 4: Convert to structured document with full hierarchy
		doc := r.hierarchyToDocument(sourceNode, tree, targetLabel)
		documents = append(documents, doc)
	}

	if len(documents) == 0 {
		return nil, fmt.Errorf("no hierarchical data found for labels: %v", targetLabels)
	}

	return documents, nil
}

// buildHierarchicalTree builds a tree structure from flat hierarchical nodes
// Uses ParentUUID for accurate parent-child relationships from Neo4j graph
func (r *GraphRAGRetriever) buildHierarchicalTree(rootUUID string, nodes []*db.HierarchicalNode) *db.HierarchicalNode {
	// Create a map for quick lookup
	nodeMap := make(map[string]*db.HierarchicalNode)
	var root *db.HierarchicalNode

	// First pass: register all nodes in map and find root
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

	// Second pass: build relationships using ParentUUID
	for _, node := range nodes {
		if node.UUID == rootUUID {
			continue // Skip root node
		}

		// Use ParentUUID if available (preferred - accurate from Neo4j)
		if node.ParentUUID != "" {
			if parent, ok := nodeMap[node.ParentUUID]; ok {
				parent.Children = append(parent.Children, node)
				continue
			}
		}

		// Fallback: attach depth 1 nodes to root
		if node.Depth == 1 && root != nil {
			root.Children = append(root.Children, node)
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

// enrichCandidatesWithVariant enriches candidates with variant info
// Fetches engine info from relationships if missing from node properties
// This is essential for distinguishing between different vehicle variants (e.g., Tucson 1.6T vs Tucson 2.0D)
func (r *GraphRAGRetriever) enrichCandidatesWithVariant(ctx context.Context, candidates []tools.NodeCandidate) []tools.NodeCandidate {
	// Collect UUIDs that need engine info (vehicle types without engine in properties)
	needsEngine := make([]string, 0)
	for _, c := range candidates {
		// Only check for vehicle-type nodes
		isVehicle := false
		for _, label := range c.Labels {
			if label == "Vehicle" || label == "CompetitorVehicle" || label == "SimilarVehicle" {
				isVehicle = true
				break
			}
		}

		if !isVehicle {
			continue
		}

		// Check if engine info is missing
		if c.VariantInfo == nil || c.VariantInfo.Engine == "" {
			needsEngine = append(needsEngine, c.UUID)
		}
	}

	if len(needsEngine) == 0 {
		return candidates
	}

	// Batch fetch engine info from relationships
	engineMap, err := r.neo4jClient.EnrichCandidatesWithEngine(ctx, needsEngine)
	if err != nil {
		// Log error but continue with existing data
		r.emitDebug(DebugEvent{
			Type:    DebugEventStep,
			Message: fmt.Sprintf("Failed to enrich candidates with engine info: %v", err),
		})
		return candidates
	}

	// Enrich candidates with engine info from relationships
	for i := range candidates {
		if engineName, ok := engineMap[candidates[i].UUID]; ok {
			if candidates[i].VariantInfo == nil {
				candidates[i].VariantInfo = &db.VariantInfo{}
			}
			if candidates[i].VariantInfo.Engine == "" {
				candidates[i].VariantInfo.Engine = engineName
			}
		}
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventStep,
		Message: fmt.Sprintf("Enriched %d candidates with engine info from relationships", len(engineMap)),
		Data:    map[string]any{"enriched_count": len(engineMap)},
	})

	return candidates
}

// =============================================================================
// Label Listing Methods (for "list all X" queries)
// =============================================================================

// retrieveWithLabelListing handles "list all nodes of label X" queries
// This method is called when analysis.IsLabelListing is true
func (r *GraphRAGRetriever) retrieveWithLabelListing(
	ctx context.Context,
	query string,
	analysis *tools.QueryAnalysisOutput,
	topK int,
) ([]*schema.Document, error) {
	// Determine limit
	limit := analysis.ListingLimit
	if limit <= 0 {
		limit = 50
	}
	if limit > topK*2 {
		limit = topK * 2
	}

	// If no labels resolved, this needs clarification
	// Return empty documents and let clarification orchestrator handle it
	if len(analysis.ListingLabels) == 0 {
		r.emitDebug(DebugEvent{
			Type:    DebugEventClarification,
			Message: "Label listing query but no labels resolved - needs clarification",
			Data:    map[string]any{"query": query},
		})

		// Return informative message indicating clarification needed
		return []*schema.Document{{
			ID:      "label_listing_clarification_needed",
			Content: "검색하려는 항목의 유형을 선택해주세요.",
			MetaData: map[string]any{
				"source":              "label_listing",
				"needs_clarification": true,
				"query":               query,
			},
		}}, nil
	}

	documents := make([]*schema.Document, 0)

	// Query each label
	for _, label := range analysis.ListingLabels {
		r.emitDebug(DebugEvent{
			Type:    DebugEventQuery,
			Message: fmt.Sprintf("Listing nodes with label '%s' (limit=%d)", label, limit),
			Data:    map[string]any{"label": label, "limit": limit},
		})

		results, total, err := r.neo4jClient.ListNodesByLabel(ctx, label, limit, "name")
		if err != nil {
			r.emitDebug(DebugEvent{
				Type:    DebugEventStep,
				Message: fmt.Sprintf("Failed to list label '%s': %v", label, err),
			})
			continue
		}

		r.emitDebug(DebugEvent{
			Type:    DebugEventRetrieval,
			Message: fmt.Sprintf("Found %d nodes for label '%s' (total: %d)", len(results), label, total),
			Data:    map[string]any{"label": label, "count": len(results), "total": total},
		})

		// Format as document
		doc := r.formatLabelListDocument(results, label, total, limit)
		documents = append(documents, doc)
	}

	if len(documents) == 0 {
		return []*schema.Document{{
			ID:      "no_results",
			Content: fmt.Sprintf("해당 라벨에 대한 데이터가 없습니다."),
			MetaData: map[string]any{
				"source": "label_listing_empty",
			},
		}}, nil
	}

	return documents, nil
}

// formatLabelListDocument formats label listing results as a markdown document
func (r *GraphRAGRetriever) formatLabelListDocument(
	results []map[string]any,
	label string,
	total int,
	limit int,
) *schema.Document {
	displayName := r.getLabelDisplayName(label)

	var content strings.Builder

	// Header
	content.WriteString(fmt.Sprintf("# %s 목록\n\n", displayName))
	content.WriteString(fmt.Sprintf("**총 %d개**의 %s이(가) 등록되어 있습니다.\n\n", total, displayName))

	if len(results) == 0 {
		content.WriteString("_데이터가 없습니다._\n")
	} else {
		// Table header
		content.WriteString("| # | 이름 | 상세 정보 |\n")
		content.WriteString("|---|------|----------|\n")

		// Table rows
		for i, node := range results {
			name := ""
			if n, ok := node["name"].(string); ok {
				name = n
			}

			// Extract variant info for display
			details := ""
			if props, ok := node["properties"].(map[string]any); ok {
				variantInfo := db.ExtractVariantInfo(props)
				if variantInfo != nil && !variantInfo.IsEmpty() {
					details = variantInfo.FormatDisplay()
				}
			}

			content.WriteString(fmt.Sprintf("| %d | %s | %s |\n", i+1, name, details))
		}

		// Show "more" indicator
		if total > len(results) {
			content.WriteString(fmt.Sprintf("\n_... 외 %d개 더 있음_\n", total-len(results)))
		}
	}

	return &schema.Document{
		ID:      fmt.Sprintf("label_list_%s", label),
		Content: content.String(),
		MetaData: map[string]any{
			"source":      "label_listing",
			"label":       label,
			"total_count": total,
			"shown_count": len(results),
			"has_more":    total > len(results),
		},
	}
}

// RetrieveWithLabelListingDirect provides direct access to label listing for use after clarification
// This is called by clarification orchestrator after user selects a label
func (r *GraphRAGRetriever) RetrieveWithLabelListingDirect(
	ctx context.Context,
	labels []string,
	limit int,
) ([]*schema.Document, error) {
	if limit <= 0 {
		limit = 50
	}

	analysis := &tools.QueryAnalysisOutput{
		IsLabelListing: true,
		ListingLabels:  labels,
		ListingLimit:   limit,
	}

	return r.retrieveWithLabelListing(ctx, "", analysis, limit)
}

// RetrieveWithConditionAndTargets provides access to conditional retrieval with explicit target labels
// This is called by clarification orchestrator for compound queries like "Honda가 생산한 경쟁차 List"
func (r *GraphRAGRetriever) RetrieveWithConditionAndTargets(
	ctx context.Context,
	query string,
	anchor *tools.ConditionalAnchor,
	filters []tools.ConditionalFilter,
	targetLabels []string,
	topK int,
) ([]*schema.Document, error) {
	if r.conditionalRetriever == nil {
		return nil, fmt.Errorf("conditional retriever not configured")
	}

	r.emitDebug(DebugEvent{
		Type:    DebugEventStep,
		Message: "RetrieveWithConditionAndTargets called",
		Data: map[string]any{
			"anchor":        anchor.Keywords,
			"condition":     anchor.ConditionType,
			"target_labels": targetLabels,
			"filters":       filters,
		},
	})

	return r.conditionalRetriever.RetrieveWithConditionAndTargetLabels(
		ctx, query, anchor, filters, targetLabels, topK)
}

// Ensure GraphRAGRetriever implements the Retriever interface
var _ retriever.Retriever = (*GraphRAGRetriever)(nil)
