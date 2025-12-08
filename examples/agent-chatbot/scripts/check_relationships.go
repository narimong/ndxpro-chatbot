// +build ignore

package main

import (
	"context"
	"fmt"
	"log"

	"agent-chatbot/api/service/db"
)

func main() {
	ctx := context.Background()

	client, err := db.NewNeo4jClient(ctx, "bolt://localhost:7687", "neo4j", "ndxpro123!")
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close(ctx)

	// Check relationships for NE2 (both directions)
	fmt.Println("=== Checking relationships for NE2 (BOTH directions) ===")

	query := `
		MATCH (v:Vehicle {name: 'NE2'})-[r]-(n)
		RETURN type(r) as relType, labels(n) as labels, n.name as name
		LIMIT 20
	`
	results, err := client.ExecuteQuery(ctx, query, nil)
	if err != nil {
		log.Fatalf("Query failed: %v", err)
	}

	fmt.Printf("Found %d relationships:\n", len(results))
	for _, r := range results {
		fmt.Printf("  -[%v]- [%v] %v\n", r["relType"], r["labels"], r["name"])
	}

	// Check incoming relationships
	fmt.Println("\n=== Checking INCOMING relationships for NE2 ===")
	inQuery := `
		MATCH (v:Vehicle {name: 'NE2'})<-[r]-(n)
		RETURN type(r) as relType, labels(n) as labels, n.name as name
		LIMIT 20
	`
	inResults, err := client.ExecuteQuery(ctx, inQuery, nil)
	if err != nil {
		log.Fatalf("Query failed: %v", err)
	}

	fmt.Printf("Found %d incoming relationships:\n", len(inResults))
	for _, r := range inResults {
		fmt.Printf("  <-[%v]- [%v] %v\n", r["relType"], r["labels"], r["name"])
	}

	// Check total relationships count in DB
	fmt.Println("\n=== Total relationships in database ===")
	countQuery := `MATCH ()-[r]->() RETURN count(r) as count`
	countResults, err := client.ExecuteQuery(ctx, countQuery, nil)
	if err != nil {
		log.Fatalf("Count query failed: %v", err)
	}
	if len(countResults) > 0 {
		fmt.Printf("Total relationships: %v\n", countResults[0]["count"])
	}

	// Sample some relationships
	fmt.Println("\n=== Sample relationships ===")
	sampleQuery := `MATCH (a)-[r]->(b) RETURN labels(a) as from, type(r) as rel, labels(b) as to LIMIT 10`
	sampleResults, err := client.ExecuteQuery(ctx, sampleQuery, nil)
	if err != nil {
		log.Fatalf("Sample query failed: %v", err)
	}
	for _, r := range sampleResults {
		fmt.Printf("  [%v] -[%v]-> [%v]\n", r["from"], r["rel"], r["to"])
	}
}
