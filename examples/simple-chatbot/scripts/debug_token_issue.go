// +build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"simple-chatbot/api/service/db"
	"simple-chatbot/api/service/tools"
)

func main() {
	ctx := context.Background()

	// Connect to Neo4j
	fmt.Println("=== Connecting to Neo4j ===")
	neo4jClient, err := db.NewNeo4jClient(ctx, "bolt://localhost:7687", "neo4j", "ndxpro123!")
	if err != nil {
		log.Fatalf("Failed to connect to Neo4j: %v", err)
	}
	defer neo4jClient.Close(ctx)
	fmt.Println("Connected!")

	// Test query that causes token error
	query := "POLO"
	fmt.Printf("\n=== Testing query: '%s' ===\n", query)

	// Step 1: Full-Text Search
	fmt.Println("\n--- Step 1: Full-Text Search ---")
	fullTextTool := tools.NewNeo4jFullTextSearchTool(neo4jClient)

	searchInput := fmt.Sprintf(`{"query": "%s", "top_k": 20}`, query)
	searchResult, err := fullTextTool.InvokableRun(ctx, searchInput)
	if err != nil {
		log.Fatalf("Full-text search failed: %v", err)
	}

	// Analyze search result size
	fmt.Printf("Search result JSON size: %d bytes\n", len(searchResult))
	fmt.Printf("Search result JSON tokens (approx): %d tokens\n", len(searchResult)/4)

	var searchData tools.FullTextSearchResult
	json.Unmarshal([]byte(searchResult), &searchData)
	fmt.Printf("Candidates found: %d\n", len(searchData.Candidates))

	// Print each candidate with Properties size
	totalPropsSize := 0
	for i, c := range searchData.Candidates {
		propsJSON, _ := json.Marshal(c.Properties)
		propsSize := len(propsJSON)
		totalPropsSize += propsSize
		fmt.Printf("  %d. [%v] %s - Properties size: %d bytes\n", i+1, c.Labels, c.Name, propsSize)

		// Show properties content for first 3
		if i < 3 {
			fmt.Printf("      Properties: %s\n", string(propsJSON))
		}
	}
	fmt.Printf("\nTotal Properties size: %d bytes (~%d tokens)\n", totalPropsSize, totalPropsSize/4)

	// Step 2: Simulate Re-ranker prompt
	fmt.Println("\n--- Step 2: Simulated Re-ranker Prompt ---")
	rerankerPrompt := buildRerankerPrompt(query, searchData.Candidates, 3)
	fmt.Printf("Re-ranker prompt size: %d bytes\n", len(rerankerPrompt))
	fmt.Printf("Re-ranker prompt tokens (approx): %d tokens\n", len(rerankerPrompt)/4)

	// Print first 2000 chars of prompt
	if len(rerankerPrompt) > 2000 {
		fmt.Printf("\nPrompt preview (first 2000 chars):\n%s\n...(truncated)...\n", rerankerPrompt[:2000])
	} else {
		fmt.Printf("\nFull prompt:\n%s\n", rerankerPrompt)
	}

	// Step 3: Graph Traversal for first candidate
	if len(searchData.Candidates) > 0 {
		fmt.Println("\n--- Step 3: Graph Traversal (FIXED: OUTGOING, depth=1) ---")
		uuid := searchData.Candidates[0].UUID
		graphTool := tools.NewNeo4jGraphTraversalTool(neo4jClient)

		// FIXED: Changed from BOTH/depth=2 to OUTGOING/depth=1 to prevent token overflow
		traversalInput := fmt.Sprintf(`{"start_uuid": "%s", "direction": "OUTGOING", "max_depth": 1}`, uuid)
		traversalResult, err := graphTool.InvokableRun(ctx, traversalInput)
		if err != nil {
			log.Printf("Graph traversal failed: %v", err)
		} else {
			fmt.Printf("Traversal result size: %d bytes\n", len(traversalResult))
			fmt.Printf("Traversal result tokens (approx): %d tokens\n", len(traversalResult)/4)

			var traversalData tools.GraphTraversalOutput
			json.Unmarshal([]byte(traversalResult), &traversalData)
			fmt.Printf("Paths found: %d\n", len(traversalData.Paths))

			// Calculate total path properties size
			totalPathPropsSize := 0
			for _, p := range traversalData.Paths {
				propsJSON, _ := json.Marshal(p.EndNode.Properties)
				totalPathPropsSize += len(propsJSON)
			}
			fmt.Printf("Total path properties size: %d bytes (~%d tokens)\n", totalPathPropsSize, totalPathPropsSize/4)
		}
	}

	// Step 4: Check properties of POLO nodes specifically
	fmt.Println("\n--- Step 4: Detailed POLO Node Analysis ---")
	poloQuery := `
		CALL db.index.fulltext.queryNodes('node_names', 'POLO')
		YIELD node, score
		RETURN node.uuid AS uuid, labels(node) AS labels, node.name AS name, properties(node) AS properties
		LIMIT 5
	`
	results, err := neo4jClient.ExecuteQuery(ctx, poloQuery, nil)
	if err != nil {
		log.Printf("Query failed: %v", err)
	} else {
		for i, r := range results {
			propsJSON, _ := json.Marshal(r["properties"])
			fmt.Printf("\nNode %d:\n", i+1)
			fmt.Printf("  UUID: %v\n", r["uuid"])
			fmt.Printf("  Labels: %v\n", r["labels"])
			fmt.Printf("  Name: %v\n", r["name"])
			fmt.Printf("  Properties size: %d bytes\n", len(propsJSON))
			fmt.Printf("  Properties: %s\n", string(propsJSON))
		}
	}

	fmt.Println("\n=== Analysis Complete ===")
}

func buildRerankerPrompt(userQuery string, candidates []tools.NodeCandidate, topN int) string {
	var sb []byte

	sb = append(sb, fmt.Sprintf("User Question: %s\n\nCandidate Nodes:\n", userQuery)...)

	for i, c := range candidates {
		propsJSON, _ := json.Marshal(c.Properties)
		sb = append(sb, fmt.Sprintf("%d. [%s] %s\n", i, joinLabels(c.Labels), c.Name)...)
		sb = append(sb, fmt.Sprintf("   UUID: %s\n", c.UUID)...)
		sb = append(sb, fmt.Sprintf("   Properties: %s\n", string(propsJSON))...)
		sb = append(sb, fmt.Sprintf("   Search Score: %.4f\n\n", c.Score)...)
	}

	sb = append(sb, fmt.Sprintf(`Task:
1. Analyze which node(s) best match the user's intent
2. Consider exact name matches, label relevance, and property values
3. Return the indices of the top %d most relevant nodes

Return as JSON: {"selected_indices": [0, 2], "reasoning": "..."}`, topN)...)

	return string(sb)
}

func joinLabels(labels []string) string {
	result := ""
	for i, l := range labels {
		if i > 0 {
			result += ","
		}
		result += l
	}
	return result
}
