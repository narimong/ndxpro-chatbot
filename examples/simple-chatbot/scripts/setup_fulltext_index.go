// +build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func main() {
	ctx := context.Background()

	// Connection settings
	uri := "bolt://localhost:7687"
	username := "neo4j"
	password := "ndxpro123!"

	if envURI := os.Getenv("NEO4J_URI"); envURI != "" {
		uri = envURI
	}
	if envUser := os.Getenv("NEO4J_USERNAME"); envUser != "" {
		username = envUser
	}
	if envPass := os.Getenv("NEO4J_PASSWORD"); envPass != "" {
		password = envPass
	}

	fmt.Printf("Connecting to Neo4j at %s...\n", uri)

	driver, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(username, password, ""))
	if err != nil {
		log.Fatalf("Failed to create driver: %v", err)
	}
	defer driver.Close(ctx)

	if err := driver.VerifyConnectivity(ctx); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	fmt.Println("Connected successfully!")

	session := driver.NewSession(ctx, neo4j.SessionConfig{})
	defer session.Close(ctx)

	// Step 1: Get all labels that have 'name' property
	fmt.Println("\nStep 1: Checking labels with 'name' property...")

	labelsResult, err := session.Run(ctx, "CALL db.labels() YIELD label RETURN collect(label) as labels", nil)
	if err != nil {
		log.Fatalf("Failed to get labels: %v", err)
	}

	var allLabels []string
	if labelsResult.Next(ctx) {
		record := labelsResult.Record()
		if labels, ok := record.Get("labels"); ok {
			for _, l := range labels.([]any) {
				allLabels = append(allLabels, l.(string))
			}
		}
	}
	fmt.Printf("Found %d labels in database\n", len(allLabels))

	// Step 2: Check which labels have 'name' property
	var labelsWithName []string
	for _, label := range allLabels {
		checkQuery := fmt.Sprintf("MATCH (n:%s) WHERE n.name IS NOT NULL RETURN count(n) as cnt LIMIT 1", label)
		result, err := session.Run(ctx, checkQuery, nil)
		if err != nil {
			continue
		}
		if result.Next(ctx) {
			record := result.Record()
			if cnt, ok := record.Get("cnt"); ok {
				if cnt.(int64) > 0 {
					labelsWithName = append(labelsWithName, label)
				}
			}
		}
	}
	fmt.Printf("Labels with 'name' property: %d\n", len(labelsWithName))

	// Step 3: Create Full-Text Index
	fmt.Println("\nStep 2: Creating Full-Text Index...")

	// Build label string
	labelStr := ""
	for i, label := range labelsWithName {
		if i > 0 {
			labelStr += "|"
		}
		labelStr += label
	}

	if labelStr == "" {
		// Fallback to known labels
		labelStr = "Vehicle|Engine|Transmission|Tire|Trim|Part|System|SubSystem|Manufacturer|Segment|VehicleType|FuelType|ElectrifiedType|Task|Requirement|Configuration"
	}

	createIndexQuery := fmt.Sprintf(`
		CREATE FULLTEXT INDEX node_names IF NOT EXISTS
		FOR (n:%s)
		ON EACH [n.name]
	`, labelStr)

	_, err = session.Run(ctx, createIndexQuery, nil)
	if err != nil {
		log.Fatalf("Failed to create Full-Text index: %v", err)
	}
	fmt.Println("Full-Text index 'node_names' created (or already exists)")

	// Step 4: Verify index
	fmt.Println("\nStep 3: Verifying index...")
	indexResult, err := session.Run(ctx, "SHOW INDEXES WHERE name = 'node_names'", nil)
	if err != nil {
		log.Printf("Warning: Could not verify index: %v", err)
	} else {
		if indexResult.Next(ctx) {
			record := indexResult.Record()
			state, _ := record.Get("state")
			indexType, _ := record.Get("type")
			fmt.Printf("Index found: type=%v, state=%v\n", indexType, state)
		} else {
			fmt.Println("Warning: Index not found in SHOW INDEXES")
		}
	}

	// Step 5: Test Full-Text search
	fmt.Println("\nStep 4: Testing Full-Text search with 'NE2'...")
	testQuery := `
		CALL db.index.fulltext.queryNodes('node_names', 'NE2')
		YIELD node, score
		RETURN node.uuid AS uuid, labels(node) AS labels, node.name AS name, score
		LIMIT 5
	`
	testResult, err := session.Run(ctx, testQuery, nil)
	if err != nil {
		log.Printf("Full-Text search test failed: %v", err)
		fmt.Println("\nNote: The index might still be building. Wait a moment and try again.")
	} else {
		count := 0
		fmt.Println("\nSearch results:")
		for testResult.Next(ctx) {
			record := testResult.Record()
			uuid, _ := record.Get("uuid")
			labels, _ := record.Get("labels")
			name, _ := record.Get("name")
			score, _ := record.Get("score")
			fmt.Printf("  - [%v] %v (uuid: %v, score: %.4f)\n", labels, name, uuid, score)
			count++
		}
		if count == 0 {
			fmt.Println("  No results found for 'NE2'")
		} else {
			fmt.Printf("\nTotal: %d results\n", count)
		}
	}

	fmt.Println("\n✅ Full-Text index setup complete!")
}
