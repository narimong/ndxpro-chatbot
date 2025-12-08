// +build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"agent-chatbot/api/service/db"
	"agent-chatbot/api/service/tools"
)

func main() {
	ctx := context.Background()

	// Connect to Neo4j
	fmt.Println("Connecting to Neo4j...")
	neo4jClient, err := db.NewNeo4jClient(ctx, "bolt://localhost:7687", "neo4j", "ndxpro123!")
	if err != nil {
		log.Fatalf("Failed to connect to Neo4j: %v", err)
	}
	defer neo4jClient.Close(ctx)
	fmt.Println("Connected to Neo4j!")

	// Test 1: Full-Text Search Tool
	fmt.Println("\n=== Test 1: Full-Text Search Tool ===")
	fullTextTool := tools.NewNeo4jFullTextSearchTool(neo4jClient)

	searchInput := `{"query": "NE2", "top_k": 5}`
	result, err := fullTextTool.InvokableRun(ctx, searchInput)
	if err != nil {
		log.Fatalf("Full-text search failed: %v", err)
	}
	fmt.Printf("Search Result:\n%s\n", result)

	// Test 2: Schema Discovery Tool
	fmt.Println("\n=== Test 2: Schema Discovery Tool ===")
	schemaTool := tools.NewNeo4jSchemaDiscoveryTool(neo4jClient)

	schemaInput := `{"include_properties": false, "include_counts": false}`
	schemaResult, err := schemaTool.InvokableRun(ctx, schemaInput)
	if err != nil {
		log.Fatalf("Schema discovery failed: %v", err)
	}

	// Pretty print schema (truncated)
	var schemaData map[string]any
	json.Unmarshal([]byte(schemaResult), &schemaData)
	if labels, ok := schemaData["labels"].([]any); ok {
		fmt.Printf("Labels (%d total): ", len(labels))
		maxShow := 10
		if len(labels) < maxShow {
			maxShow = len(labels)
		}
		for i := 0; i < maxShow; i++ {
			fmt.Printf("%v, ", labels[i])
		}
		if len(labels) > 10 {
			fmt.Printf("...")
		}
		fmt.Println()
	}
	if rels, ok := schemaData["relationships"].([]any); ok {
		fmt.Printf("Relationships (%d total): %v\n", len(rels), rels)
	}

	// Test 3: Graph Traversal Tool
	fmt.Println("\n=== Test 3: Graph Traversal Tool ===")

	// First get a UUID from the search result
	var searchResultData tools.FullTextSearchResult
	json.Unmarshal([]byte(result), &searchResultData)

	if len(searchResultData.Candidates) > 0 {
		uuid := searchResultData.Candidates[0].UUID
		fmt.Printf("Testing traversal from node UUID: %s\n", uuid)

		graphTool := tools.NewNeo4jGraphTraversalTool(neo4jClient)

		// Test INCOMING direction (NE2 has incoming relationships)
		fmt.Println("\n--- INCOMING direction ---")
		traversalInput := fmt.Sprintf(`{"start_uuid": "%s", "direction": "INCOMING", "max_depth": 2}`, uuid)

		traversalResult, err := graphTool.InvokableRun(ctx, traversalInput)
		if err != nil {
			log.Printf("Graph traversal failed: %v", err)
		} else {
			var traversalData tools.GraphTraversalOutput
			json.Unmarshal([]byte(traversalResult), &traversalData)
			fmt.Printf("Start Node: [%v] %s\n", traversalData.StartNode.Labels, traversalData.StartNode.Name)
			fmt.Printf("Found %d paths:\n", len(traversalData.Paths))
			for i, path := range traversalData.Paths {
				if i >= 10 { // Limit output
					fmt.Printf("  ... and %d more\n", len(traversalData.Paths)-10)
					break
				}
				fmt.Printf("  <-[%s]- [%v] %s\n", path.Relationship, path.EndNode.Labels, path.EndNode.Name)
			}
		}

		// Test BOTH direction
		fmt.Println("\n--- BOTH directions ---")
		traversalInput2 := fmt.Sprintf(`{"start_uuid": "%s", "direction": "BOTH", "max_depth": 1}`, uuid)

		traversalResult2, err := graphTool.InvokableRun(ctx, traversalInput2)
		if err != nil {
			log.Printf("Graph traversal failed: %v", err)
		} else {
			var traversalData2 tools.GraphTraversalOutput
			json.Unmarshal([]byte(traversalResult2), &traversalData2)
			fmt.Printf("Found %d paths (both directions):\n", len(traversalData2.Paths))
			for i, path := range traversalData2.Paths {
				if i >= 10 { // Limit output
					fmt.Printf("  ... and %d more\n", len(traversalData2.Paths)-10)
					break
				}
				fmt.Printf("  -[%s]- [%v] %s\n", path.Relationship, path.EndNode.Labels, path.EndNode.Name)
			}
		}
	}

	fmt.Println("\n✅ Neo4j Tools test completed!")
}
