package nodes

import (
	"fmt"
	"time"

	"github.com/agentmesh/backend/internal/models"
)

var graphNodeTypes = map[string]bool{
	"trigger": true, "agent": true, "provider": true, "tool": true,
	"tool402": true, "action": true, "end": true, "tendril": true,
}

var graphEdgeKinds = map[string]bool{"flow": true, "attach": true}

// nodeFieldSetters maps the config keys a build-mode tool call may set onto
// the corresponding WorkflowNode string field. Deliberately excludes
// DiscoveredParams/CustomParams/Secrets/Config/tendril* fields -- those are
// advanced per-node authoring surfaces out of scope for a first cut of
// chat-built graphs.
var nodeFieldSetters = map[string]func(n *models.WorkflowNode, v string){
	"systemPrompt":  func(n *models.WorkflowNode, v string) { n.SystemPrompt = v },
	"model":         func(n *models.WorkflowNode, v string) { n.Model = v },
	"keyMode":       func(n *models.WorkflowNode, v string) { n.KeyMode = v },
	"apiKey":        func(n *models.WorkflowNode, v string) { n.APIKey = v },
	"url":           func(n *models.WorkflowNode, v string) { n.URL = v },
	"method":        func(n *models.WorkflowNode, v string) { n.Method = v },
	"endpoint":      func(n *models.WorkflowNode, v string) { n.Endpoint = v },
	"price":         func(n *models.WorkflowNode, v string) { n.Price = v },
	"unit":          func(n *models.WorkflowNode, v string) { n.Unit = v },
	"provider":      func(n *models.WorkflowNode, v string) { n.Provider = v },
	"description":   func(n *models.WorkflowNode, v string) { n.Description = v },
	"emailTo":       func(n *models.WorkflowNode, v string) { n.EmailTo = v },
	"emailFrom":     func(n *models.WorkflowNode, v string) { n.EmailFrom = v },
	"emailSubject":  func(n *models.WorkflowNode, v string) { n.EmailSubject = v },
	"emailBody":     func(n *models.WorkflowNode, v string) { n.EmailBody = v },
	"emailProvider": func(n *models.WorkflowNode, v string) { n.EmailProvider = v },
}

func newGraphID(prefix string) string {
	return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
}

// applyGraphOp mutates graph in place per a single tool call the build-mode
// meta-agent requested, and returns a short human-readable result string fed
// back to the model as the tool's functionResponse.
func applyGraphOp(graph *models.WorkflowGraph, funcName string, args map[string]any) (string, error) {
	switch funcName {
	case "add_node":
		return addGraphNode(graph, args)
	case "update_node":
		return updateGraphNode(graph, args)
	case "remove_node":
		return removeGraphNode(graph, args)
	case "add_edge":
		return addGraphEdge(graph, args)
	case "remove_edge":
		return removeGraphEdge(graph, args)
	default:
		return "", fmt.Errorf("unknown graph tool %q", funcName)
	}
}

func argString(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

func argFields(args map[string]any) (map[string]string, error) {
	out := map[string]string{}
	raw, ok := args["fields"].(map[string]any)
	if !ok {
		return out, nil
	}
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		} else {
			return nil, fmt.Errorf("field %q must be a string, got %T", k, v)
		}
	}
	return out, nil
}

func addGraphNode(graph *models.WorkflowGraph, args map[string]any) (string, error) {
	nodeType := argString(args, "type")
	if !graphNodeTypes[nodeType] {
		return "", fmt.Errorf("add_node: invalid type %q", nodeType)
	}
	fields, err := argFields(args)
	if err != nil {
		return "", err
	}
	id := newGraphID("n_")
	node := models.WorkflowNode{
		ID:       id,
		Type:     models.NodeType(nodeType),
		Template: argString(args, "template"),
		Name:     argString(args, "name"),
		X:        80 + 240*float64(len(graph.Nodes)%4),
		Y:        120 + 160*float64(len(graph.Nodes)/4),
	}
	for k, v := range fields {
		if set, ok := nodeFieldSetters[k]; ok {
			set(&node, v)
		}
	}
	graph.Nodes = append(graph.Nodes, node)
	return fmt.Sprintf("added node %s (%s/%s)", id, nodeType, node.Template), nil
}

func updateGraphNode(graph *models.WorkflowGraph, args map[string]any) (string, error) {
	id := argString(args, "id")
	for i := range graph.Nodes {
		if graph.Nodes[i].ID != id {
			continue
		}
		if name := argString(args, "name"); name != "" {
			graph.Nodes[i].Name = name
		}
		if template := argString(args, "template"); template != "" {
			graph.Nodes[i].Template = template
		}
		fields, err := argFields(args)
		if err != nil {
			return "", err
		}
		for k, v := range fields {
			if set, ok := nodeFieldSetters[k]; ok {
				set(&graph.Nodes[i], v)
			}
		}
		return fmt.Sprintf("updated node %s", id), nil
	}
	return "", fmt.Errorf("update_node: node %q not found", id)
}

func removeGraphNode(graph *models.WorkflowGraph, args map[string]any) (string, error) {
	id := argString(args, "id")
	found := false
	nodes := graph.Nodes[:0]
	for _, n := range graph.Nodes {
		if n.ID == id {
			found = true
			continue
		}
		nodes = append(nodes, n)
	}
	if !found {
		return "", fmt.Errorf("remove_node: node %q not found", id)
	}
	graph.Nodes = nodes
	edges := graph.Edges[:0]
	for _, e := range graph.Edges {
		if e.From == id || e.To == id {
			continue
		}
		edges = append(edges, e)
	}
	graph.Edges = edges
	return fmt.Sprintf("removed node %s", id), nil
}

func addGraphEdge(graph *models.WorkflowGraph, args map[string]any) (string, error) {
	from := argString(args, "from")
	to := argString(args, "to")
	kind := argString(args, "kind")
	if kind == "" {
		kind = "flow"
	}
	if !graphEdgeKinds[kind] {
		return "", fmt.Errorf("add_edge: invalid kind %q", kind)
	}
	if !graphHasNode(graph, from) {
		return "", fmt.Errorf("add_edge: node %q not found", from)
	}
	if !graphHasNode(graph, to) {
		return "", fmt.Errorf("add_edge: node %q not found", to)
	}
	edge := models.WorkflowEdge{
		ID:     newGraphID("e_"),
		From:   from,
		To:     to,
		Kind:   models.EdgeKind(kind),
		ToPort: argString(args, "toPort"),
	}
	graph.Edges = append(graph.Edges, edge)
	return fmt.Sprintf("added edge %s (%s -> %s)", edge.ID, from, to), nil
}

func removeGraphEdge(graph *models.WorkflowGraph, args map[string]any) (string, error) {
	id := argString(args, "id")
	found := false
	edges := graph.Edges[:0]
	for _, e := range graph.Edges {
		if e.ID == id {
			found = true
			continue
		}
		edges = append(edges, e)
	}
	if !found {
		return "", fmt.Errorf("remove_edge: edge %q not found", id)
	}
	graph.Edges = edges
	return fmt.Sprintf("removed edge %s", id), nil
}

func graphHasNode(graph *models.WorkflowGraph, id string) bool {
	for _, n := range graph.Nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}
