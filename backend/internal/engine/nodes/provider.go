package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/agentmesh/backend/internal/models"
)

var openAIBaseURL = "https://api.openai.com"
var groqBaseURL = "https://api.groq.com/openai"
var mistralBaseURL = "https://api.mistral.ai"

func SetOpenAIBaseURL(u string) { openAIBaseURL = u }

// ExecuteAgent runs the agent node: calls the attached LLM with function calling
// support so the agent decides whether and how to invoke its attached tools.
func ExecuteAgent(ctx context.Context, node models.WorkflowNode, attach models.AttachConfig, aw models.AgentWallet, signer WalletSigner, rc RunContexter, checkBalance BalanceChecker, relayCfg X402RelayConfig) (any, error) {
	if attach.Provider == nil {
		return rc.UserInput(), nil
	}
	p := attach.Provider
	switch p.Template {
	case "openai", "groq", "mistral":
		return callOpenAICompat(ctx, node, *p, attach.Tools, aw, signer, rc, checkBalance, relayCfg)
	case "anthropic":
		return callAnthropic(ctx, node, *p, rc)
	case "gemini":
		return callGemini(ctx, node, *p, attach.Tools, aw, signer, rc, checkBalance, relayCfg)
	default:
		return callOpenAICompat(ctx, node, *p, attach.Tools, aw, signer, rc, checkBalance, relayCfg)
	}
}

// ─── function declaration helpers ────────────────────────────────────────────

type funcDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// sanitizeFuncName converts any string into a valid LLM function name.
// Rules: starts with [a-zA-Z_], contains only [a-zA-Z0-9_-], max 64 chars.
func sanitizeFuncName(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsLetter(r) || r == '_' {
			b.WriteRune(r)
		} else if i > 0 && (unicode.IsDigit(r) || r == '-') {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			b.WriteRune('_')
		}
	}
	result := b.String()
	// Trim trailing underscores
	result = strings.TrimRight(result, "_")
	if len(result) > 64 {
		result = result[:64]
	}
	if result == "" {
		result = "tool"
	}
	return result
}

func toolFuncName(t models.WorkflowNode) string {
	name := sanitizeFuncName(t.Name)
	if name == "" || name == "tool" {
		name = sanitizeFuncName(t.ID)
	}
	return name
}

func buildFuncDecls(tools []models.WorkflowNode) []funcDecl {
	decls := make([]funcDecl, 0, len(tools))
	for _, t := range tools {
		name := toolFuncName(t)
		desc := t.Description
		if desc == "" {
			desc = "External tool: " + t.Name
			if t.Endpoint != "" {
				desc += ". Endpoint: " + t.Endpoint
			}
		}

		properties := map[string]any{}
		required := []string{}

		for _, p := range t.DiscoveredParams {
			pType := "string"
			switch strings.ToLower(p.Type) {
			case "number", "integer", "float", "int":
				pType = "number"
			case "boolean", "bool":
				pType = "boolean"
			}
			prop := map[string]any{"type": pType, "description": p.Description}
			if p.Default != "" {
				prop["default"] = p.Default
			}
			properties[p.Name] = prop
			if p.Required {
				required = append(required, p.Name)
			}
		}

		params := map[string]any{"type": "OBJECT", "properties": properties}
		if len(required) > 0 {
			params["required"] = required
		}

		decls = append(decls, funcDecl{Name: name, Description: desc, Parameters: params})
	}
	return decls
}

// ─── tool execution helper ────────────────────────────────────────────────────

func executeFunctionCall(ctx context.Context, funcName string, args map[string]any, tools []models.WorkflowNode, aw models.AgentWallet, signer WalletSigner, rc RunContexter, checkBalance BalanceChecker, relayCfg X402RelayConfig) (any, models.WorkflowNode, *ToolPaymentInfo, error) {
	for _, t := range tools {
		if toolFuncName(t) != funcName {
			continue
		}
		toolNode := t
		if checkBalance != nil {
			// Cheap, conservative guard before any network call to the tool's
			// endpoint: reject outright if the balance can't even cover the
			// worst-case flat-fee floor, so an unfunded caller can't drive
			// unbounded outbound HTTP requests (SSRF/DoS-amplification risk)
			// merely by attaching tool nodes it can never pay for. The real,
			// exact-amount gate for tool402 relay-dialect payments still runs
			// separately inside ExecuteTool402V2 once the true cost is known
			// (which can be less than this floor) — this is a floor, not the
			// final word.
			var feeAmount int64
			switch {
			case toolNode.Type == models.NodeTypeTool402:
				feeAmount = models.X402PlatformFeeUSDMicros
			case BillableFlatFee(toolNode.Type, toolNode.Template):
				feeAmount = models.ByokFlatFeeUSDMicros
			}
			if feeAmount > 0 {
				if err := checkBalance(ctx, feeAmount); err != nil {
					return nil, toolNode, nil, &ErrBalanceBlocked{Err: err}
				}
			}
		}
		// Append LLM-chosen args as query params onto the endpoint URL
		if len(args) > 0 && toolNode.Endpoint != "" {
			u, err := url.Parse(toolNode.Endpoint)
			if err == nil {
				q := u.Query()
				for k, v := range args {
					q.Set(k, fmt.Sprintf("%v", v))
				}
				u.RawQuery = q.Encode()
				toolNode.Endpoint = u.String()
			}
		}
		if t.Type == models.NodeTypeTool402 {
			paymentResult, err := ExecuteTool402V2(ctx, toolNode, rc, aw, signer, relayCfg)
			if err != nil {
				return nil, toolNode, nil, err
			}
			var payment *ToolPaymentInfo
			if paymentResult.SettledUSDMicros > 0 {
				payment = &ToolPaymentInfo{
					NodeID: toolNode.ID, NodeName: toolNode.Name,
					SettledUSDMicros: paymentResult.SettledUSDMicros,
					DebitKind:        paymentResult.DebitKind,
				}
				if m, ok := paymentResult.Response.(map[string]any); ok {
					payment.TxID, _ = m["txId"].(string)
					payment.ExplorerURL, _ = m["explorerURL"].(string)
				}
			}
			return paymentResult.Response, toolNode, payment, nil
		}
		result, err := ExecuteTool(ctx, toolNode, rc)
		return result, toolNode, nil, err
	}
	return nil, models.WorkflowNode{}, nil, fmt.Errorf("tool %q not found in attached tools", funcName)
}

// ─── low-level HTTP helper ────────────────────────────────────────────────────

// postLLMJSON posts a JSON payload to an LLM provider API and decodes the response body.
// Distinct from the connector-oriented postJSON in connector_helpers.go, which returns a
// caller-supplied sentinel instead of the decoded body.
func postLLMJSON(ctx context.Context, apiURL string, headers map[string]string, payload any) (map[string]any, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("LLM API %d: %s", resp.StatusCode, string(b))
	}
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	return result, nil
}

// ─── Gemini ───────────────────────────────────────────────────────────────────

const maxToolIterations = 15

// ToolPaymentInfo carries what an agent-attached tool402 call actually
// charged, so the agent loop can bill the real settled amount (relay
// dialect) or the flat fee (legacy dialect) instead of assuming a fixed
// amount from the tool result's shape.
type ToolPaymentInfo struct {
	NodeID           string
	NodeName         string
	SettledUSDMicros int64
	DebitKind        string
	// TxID/ExplorerURL are populated only for the legacy direct-pay dialect
	// (its result map already carries them); relay-dialect payments settle
	// through the facilitator with no single client-visible txid to surface
	// here, so these stay empty for that path.
	TxID        string
	ExplorerURL string
}

// paymentReceipt turns a ToolPaymentInfo into the map shape surfaced in an
// agent's "x402Payments" output field. txId/explorerURL keys are included
// only when populated (legacy dialect), keeping the existing frontend
// contract for those fields unchanged while adding the new
// settledUsdMicros/debitKind fields runner.go needs for correct billing.
func paymentReceipt(p *ToolPaymentInfo) map[string]any {
	receipt := map[string]any{
		"nodeId":           p.NodeID,
		"nodeName":         p.NodeName,
		"settledUsdMicros": p.SettledUSDMicros,
		"debitKind":        p.DebitKind,
	}
	if p.TxID != "" {
		receipt["txId"] = p.TxID
	}
	if p.ExplorerURL != "" {
		receipt["explorerURL"] = p.ExplorerURL
	}
	return receipt
}

func callGemini(ctx context.Context, agent models.WorkflowNode, provider models.WorkflowNode, tools []models.WorkflowNode, aw models.AgentWallet, signer WalletSigner, rc RunContexter, checkBalance BalanceChecker, relayCfg X402RelayConfig) (any, error) {
	model := provider.Model
	if model == "" {
		model = "gemini-2.5-flash"
	}
	apiURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", model)
	apiHeaders := map[string]string{"x-goog-api-key": provider.APIKey}

	contents := []map[string]any{
		{"role": "user", "parts": []map[string]any{{"text": rc.UserInput()}}},
	}

	payload := map[string]any{"contents": contents}
	if agent.SystemPrompt != "" {
		payload["systemInstruction"] = map[string]any{
			"parts": []map[string]string{{"text": agent.SystemPrompt}},
		}
	}
	decls := buildFuncDecls(tools)
	if len(decls) > 0 {
		payload["tools"] = []map[string]any{{"functionDeclarations": decls}}
	}

	var x402Payments []map[string]any
	var billedFlatFeeNodeIds []string

	// Agentic loop — keep calling until the model returns text (no function call).
	for iter := 0; iter < maxToolIterations; iter++ {
		resp, err := postLLMJSON(ctx, apiURL, apiHeaders, payload)
		if err != nil {
			return nil, err
		}

		// Collect all function calls the model wants to make in this turn
		// (Gemini can return multiple functionCall parts at once for parallel use).
		calls := extractGeminiFunctionCalls(resp)
		if len(calls) == 0 {
			text, textErr := extractGeminiText(resp)
			if textErr != nil {
				return nil, textErr
			}
			if len(x402Payments) > 0 || len(billedFlatFeeNodeIds) > 0 {
				out := map[string]any{"message": text}
				if len(x402Payments) > 0 {
					out["x402Payments"] = x402Payments
				}
				if len(billedFlatFeeNodeIds) > 0 {
					out["billedFlatFeeNodeIds"] = billedFlatFeeNodeIds
				}
				return out, nil
			}
			return text, nil
		}

		// Build the model turn (all function calls in one "model" message)
		modelParts := make([]map[string]any, len(calls))
		for i, c := range calls {
			modelParts[i] = map[string]any{"functionCall": map[string]any{"name": c.name, "args": c.args}}
		}
		contents = append(contents, map[string]any{"role": "model", "parts": modelParts})

		// Execute every requested function call and collect responses
		responseParts := make([]map[string]any, 0, len(calls))
		for _, c := range calls {
			result, toolNode, payment, execErr := executeFunctionCall(ctx, c.name, c.args, tools, aw, signer, rc, checkBalance, relayCfg)
			if execErr != nil {
				var blocked *ErrBalanceBlocked
				if errors.As(execErr, &blocked) {
					return nil, execErr
				}
			}
			resultStr := ""
			if execErr != nil {
				resultStr = "error: " + execErr.Error()
			} else {
				if payment != nil {
					x402Payments = append(x402Payments, paymentReceipt(payment))
				}
				if BillableFlatFee(toolNode.Type, toolNode.Template) {
					billedFlatFeeNodeIds = append(billedFlatFeeNodeIds, toolNode.ID)
				}
				b, _ := json.Marshal(result)
				resultStr = string(b)
			}
			responseParts = append(responseParts, map[string]any{
				"functionResponse": map[string]any{
					"name":     c.name,
					"response": map[string]any{"result": resultStr},
				},
			})
		}

		// Feed all results back in one "user" turn
		contents = append(contents, map[string]any{"role": "user", "parts": responseParts})
		payload["contents"] = contents
	}

	return nil, fmt.Errorf("agent exceeded maximum tool call iterations (%d)", maxToolIterations)
}

func extractGeminiText(resp map[string]any) (string, error) {
	candidates, _ := resp["candidates"].([]any)
	if len(candidates) == 0 {
		return "", fmt.Errorf("empty response from Gemini")
	}
	content, _ := candidates[0].(map[string]any)["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	for _, p := range parts {
		part, _ := p.(map[string]any)
		if text, ok := part["text"].(string); ok {
			return text, nil
		}
	}
	return "", fmt.Errorf("no text part in Gemini response")
}

type geminiFuncCall struct {
	name string
	args map[string]any
}

// extractGeminiFunctionCalls returns ALL functionCall parts in the first candidate.
// Gemini may request multiple parallel tool calls in a single turn.
func extractGeminiFunctionCalls(resp map[string]any) []geminiFuncCall {
	candidates, _ := resp["candidates"].([]any)
	if len(candidates) == 0 {
		return nil
	}
	content, _ := candidates[0].(map[string]any)["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	var calls []geminiFuncCall
	for _, p := range parts {
		part, _ := p.(map[string]any)
		if fc, ok := part["functionCall"].(map[string]any); ok {
			name, _ := fc["name"].(string)
			args, _ := fc["args"].(map[string]any)
			calls = append(calls, geminiFuncCall{name: name, args: args})
		}
	}
	return calls
}

// ─── OpenAI / Groq / Mistral ──────────────────────────────────────────────────

func callOpenAICompat(ctx context.Context, agent models.WorkflowNode, provider models.WorkflowNode, tools []models.WorkflowNode, aw models.AgentWallet, signer WalletSigner, rc RunContexter, checkBalance BalanceChecker, relayCfg X402RelayConfig) (any, error) {
	baseURL := openAIBaseURL
	switch provider.Template {
	case "groq":
		baseURL = groqBaseURL
	case "mistral":
		baseURL = mistralBaseURL
	}
	model := provider.Model
	if model == "" {
		model = "gpt-4o"
	}

	messages := []map[string]any{}
	if agent.SystemPrompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": agent.SystemPrompt})
	}
	messages = append(messages, map[string]any{"role": "user", "content": rc.UserInput()})

	payload := map[string]any{"model": model, "messages": messages}

	decls := buildFuncDecls(tools)
	if len(decls) > 0 {
		oaiTools := make([]map[string]any, len(decls))
		for i, d := range decls {
			oaiTools[i] = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        d.Name,
					"description": d.Description,
					"parameters":  d.Parameters,
				},
			}
		}
		payload["tools"] = oaiTools
	}

	headers := map[string]string{"Authorization": "Bearer " + provider.APIKey}

	var x402Payments []map[string]any
	var billedFlatFeeNodeIds []string

	// Agentic loop — repeat until the model returns content with no tool calls.
	for iter := 0; iter < maxToolIterations; iter++ {
		payload["messages"] = messages
		resp, err := postLLMJSON(ctx, baseURL+"/v1/chat/completions", headers, payload)
		if err != nil {
			return nil, err
		}

		choices, _ := resp["choices"].([]any)
		if len(choices) == 0 {
			return nil, fmt.Errorf("empty choices from LLM")
		}
		choice, _ := choices[0].(map[string]any)
		msg, _ := choice["message"].(map[string]any)

		toolCalls, _ := msg["tool_calls"].([]any)
		if len(toolCalls) == 0 {
			// No tool calls — return the final answer
			content, _ := msg["content"].(string)
			if len(x402Payments) > 0 || len(billedFlatFeeNodeIds) > 0 {
				out := map[string]any{"message": content}
				if len(x402Payments) > 0 {
					out["x402Payments"] = x402Payments
				}
				if len(billedFlatFeeNodeIds) > 0 {
					out["billedFlatFeeNodeIds"] = billedFlatFeeNodeIds
				}
				return out, nil
			}
			return content, nil
		}

		// Build the assistant message with all tool calls
		assistantMsg := map[string]any{"role": "assistant", "tool_calls": toolCalls}
		if content, _ := msg["content"].(string); content != "" {
			assistantMsg["content"] = content
		}
		messages = append(messages, assistantMsg)

		// Execute every tool call and append results
		for _, raw := range toolCalls {
			tc, _ := raw.(map[string]any)
			tcFunc, _ := tc["function"].(map[string]any)
			tcName, _ := tcFunc["name"].(string)
			tcArgsStr, _ := tcFunc["arguments"].(string)
			tcID, _ := tc["id"].(string)

			var tcArgs map[string]any
			json.Unmarshal([]byte(tcArgsStr), &tcArgs)

			toolResult, toolNode, payment, toolErr := executeFunctionCall(ctx, tcName, tcArgs, tools, aw, signer, rc, checkBalance, relayCfg)
			if toolErr != nil {
				var blocked *ErrBalanceBlocked
				if errors.As(toolErr, &blocked) {
					return nil, toolErr
				}
			}
			resultStr := ""
			if toolErr != nil {
				resultStr = "error: " + toolErr.Error()
			} else {
				if payment != nil {
					x402Payments = append(x402Payments, paymentReceipt(payment))
				}
				if BillableFlatFee(toolNode.Type, toolNode.Template) {
					billedFlatFeeNodeIds = append(billedFlatFeeNodeIds, toolNode.ID)
				}
				b, _ := json.Marshal(toolResult)
				resultStr = string(b)
			}

			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": tcID,
				"content":      resultStr,
			})
		}
	}

	return nil, fmt.Errorf("agent exceeded maximum tool call iterations (%d)", maxToolIterations)
}

// ─── Anthropic ────────────────────────────────────────────────────────────────

func callAnthropic(ctx context.Context, agent models.WorkflowNode, provider models.WorkflowNode, rc RunContexter) (string, error) {
	model := provider.Model
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	type anthMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payload := map[string]any{
		"model":      model,
		"max_tokens": 4096,
		"messages":   []anthMsg{{Role: "user", Content: rc.UserInput()}},
	}
	if agent.SystemPrompt != "" {
		payload["system"] = agent.SystemPrompt
	}

	headers := map[string]string{
		"x-api-key":         provider.APIKey,
		"anthropic-version": "2023-06-01",
	}
	resp, err := postLLMJSON(ctx, "https://api.anthropic.com/v1/messages", headers, payload)
	if err != nil {
		return "", err
	}

	contentArr, _ := resp["content"].([]any)
	for _, c := range contentArr {
		cm, _ := c.(map[string]any)
		if cm["type"] == "text" {
			text, _ := cm["text"].(string)
			return text, nil
		}
	}
	return "", fmt.Errorf("no text block in Anthropic response")
}
