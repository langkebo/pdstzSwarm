// Command graph_demo is a 30-line demo of the P4 knowledge-graph
// package. It seeds three entities and a relation, then queries
// the graph and dumps the result as JSON. The output is meant to
// be human-readable, so the demo skips any dependency on a
// running Postgres (it uses the in-memory MockNeo4jGraphStore).
//
// Run with:
//
//	go run ./cmd/graph_demo
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/uuid"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/graph"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store := graph.NewMockNeo4jGraphStore()
	defer store.Close()

	ext := graph.NewRuleBasedExtractor(logger)
	campaignID := uuid.New()
	ctx := context.Background()

	// One episode: a free-form pentest finding.
	ep := graph.Episode{
		Source:     "demo",
		SourceID:   "demo-1",
		CampaignID: campaignID,
		Text: `CVE-2024-1234 affects 10.0.0.5.
		       nmap reports http on port 80.
		       10.0.0.5 exposes /admin/login.`,
	}
	if err := store.AddEpisode(ctx, ep, ext); err != nil {
		fmt.Fprintf(os.Stderr, "AddEpisode: %v\n", err)
		os.Exit(1)
	}

	// Query back: every CVE in the demo campaign.
	cves, err := store.QueryEntities(ctx, graph.EntityQuery{
		Type:       graph.EntityTypeCVE,
		CampaignID: &campaignID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "QueryEntities: %v\n", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cves); err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
		os.Exit(1)
	}

	// Stats.
	stats, _ := store.Stats(ctx)
	fmt.Fprintf(os.Stderr, "\ngraph: %d entities, %d relations, by-type=%v\n",
		stats.EntityCount, stats.RelationCount, stats.ByType)
}
