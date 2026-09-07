package engine

import (
	"testing"

	"github.com/agentmesh/backend/internal/models"
)

func TestEstimateRunCost(t *testing.T) {
	const byok = 500_000    // models.ByokFlatFeeUSDMicros
	const x402Fee = 1_500_000 // models.X402PlatformFeeUSDMicros

	tests := []struct {
		name         string
		nodes        []models.WorkflowNode
		edges        []models.WorkflowEdge
		wantLow      int64
		wantHigh     int64
		wantUnpriced bool
		wantLines    int
	}{
		{
			name: "trigger and end only cost nothing",
			nodes: []models.WorkflowNode{
				{ID: "t", Type: models.NodeTypeTrigger},
				{ID: "e", Type: models.NodeTypeEnd},
			},
			wantLow: 0, wantHigh: 0, wantLines: 0,
		},
		{
			name: "one BYOK agent is a flat fee, low equals high",
			nodes: []models.WorkflowNode{
				{ID: "a", Type: models.NodeTypeAgent},
			},
			wantLow: byok, wantHigh: byok, wantLines: 1,
		},
		{
			name: "platform-key economy agent uses the tier fee",
			nodes: []models.WorkflowNode{
				{ID: "a", Type: models.NodeTypeAgent, KeyMode: "platform", Template: "openai", Model: "gpt-4o-mini"},
			},
			wantLow: models.PlatformKeyEconomyFeeUSDMicros, wantHigh: models.PlatformKeyEconomyFeeUSDMicros, wantLines: 1,
		},
		{
			name: "http tool and a connector each add a flat fee",
			nodes: []models.WorkflowNode{
				{ID: "h", Type: models.NodeTypeTool, Template: "http"},
				{ID: "g", Type: models.NodeTypeGoogle},
			},
			wantLow: 2 * byok, wantHigh: 2 * byok, wantLines: 2,
		},
		{
			name: "calc tool is free",
			nodes: []models.WorkflowNode{
				{ID: "c", Type: models.NodeTypeTool, Template: "calc"},
			},
			wantLow: 0, wantHigh: 0, wantLines: 0,
		},
		{
			name: "standalone priced x402 node has a fixed cost",
			nodes: []models.WorkflowNode{
				{ID: "x", Type: models.NodeTypeTool402, Price: "0.01"},
			},
			wantLow: x402Fee + 10_000, wantHigh: x402Fee + 10_000, wantLines: 1,
		},
		{
			name: "standalone unpriced x402 node spreads fee..fee+ceiling",
			nodes: []models.WorkflowNode{
				{ID: "x", Type: models.NodeTypeTool402},
			},
			wantLow: x402Fee, wantHigh: x402Fee + x402EstimateCeilingUSDMicros,
			wantUnpriced: true, wantLines: 1,
		},
		{
			name: "x402 tool attached to an agent bills 1x..typical under the agent",
			nodes: []models.WorkflowNode{
				{ID: "a", Type: models.NodeTypeAgent},
				{ID: "x", Type: models.NodeTypeTool402, Price: "0.02"},
			},
			edges: []models.WorkflowEdge{
				{From: "x", To: "a", Kind: models.EdgeKindAttach, ToPort: "tools"},
			},
			wantLow:   byok + (x402Fee + 20_000),
			wantHigh:  byok + (x402Fee+20_000)*typicalAgentToolCalls,
			wantLines: 1, // the tool402 folds into the agent line, not its own
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateRunCost(models.Workflow{Nodes: tt.nodes, Edges: tt.edges})
			if got.LowUSDMicros != tt.wantLow {
				t.Errorf("low = %d, want %d", got.LowUSDMicros, tt.wantLow)
			}
			if got.HighUSDMicros != tt.wantHigh {
				t.Errorf("high = %d, want %d", got.HighUSDMicros, tt.wantHigh)
			}
			if got.HasUnpricedX402 != tt.wantUnpriced {
				t.Errorf("hasUnpricedX402 = %v, want %v", got.HasUnpricedX402, tt.wantUnpriced)
			}
			if len(got.Lines) != tt.wantLines {
				t.Errorf("lines = %d, want %d", len(got.Lines), tt.wantLines)
			}
		})
	}
}

func TestParsePriceUSDMicros(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0.01", 10_000, true},
		{"$0.20", 200_000, true},
		{"  1.5 ", 1_500_000, true},
		{"", 0, false},
		{"free", 0, false},
		{"-1", 0, false},
	}
	for _, tt := range tests {
		got, ok := parsePriceUSDMicros(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("parsePriceUSDMicros(%q) = (%d, %v), want (%d, %v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}
