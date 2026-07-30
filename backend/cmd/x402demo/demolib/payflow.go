package demolib

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/agentmesh/backend/internal/wallet"
)

// PaymentResponseHeader is the real x402 v2 header a resource server sets,
// after a successful settle, carrying the base64-JSON-encoded SettleResponse
// — the same mechanism the official @x402/core SDK uses
// (encodePaymentResponseHeader / X-PAYMENT-RESPONSE), so any generic x402
// client (not just this demo's own) can read the real tx id straight off
// the response headers without us inventing a bespoke field.
const PaymentResponseHeader = "X-Payment-Response"

// PaymentSignatureHeader is what a client sends its signed payment back as.
const PaymentSignatureHeader = "X-Payment"

// EncodeSettleResponseHeader base64-JSON-encodes a SettleResponse for the
// X-Payment-Response header — call this after a successful facilitator
// Settle, before writing the paid response.
func EncodeSettleResponseHeader(s SettleResponse) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func DecodeSettleResponseHeader(header string) (SettleResponse, error) {
	var out SettleResponse
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return out, fmt.Errorf("decode X-Payment-Response: %w", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("unmarshal X-Payment-Response: %w", err)
	}
	return out, nil
}

// PaidCallResult is what PayAndFetch returns: the final paid response body
// and the real on-chain settlement it required.
type PaidCallResult struct {
	StatusCode int
	Body       []byte
	Settlement SettleResponse
}

var payHTTPClient = &http.Client{Timeout: 30 * time.Second}

// PayAndFetch is the client half of the x402 exact/AVM flow, used by both
// the standalone demo client (paying the orchestrator) and the orchestrator
// itself (paying the merchant, acting as a client for that hop):
//
//  1. POST requestBody to url unpaid.
//  2. Expect 402; decode the PaymentRequired challenge (body or the
//     Payment-Required header, whichever is present — mirrors real servers
//     which may set either or both).
//  3. Sign a real USDC payment group from encMnemonic against the first
//     accepted PaymentRequirements, via wallet.Service.SignUSDCPaymentGroup
//     (the same signing primitive AgentMesh's production relay uses).
//  4. Retry the identical request with the signed payload as X-Payment.
//  5. Decode the real settlement off the X-Payment-Response header.
func PayAndFetch(ctx context.Context, walletSvc *wallet.Service, encMnemonic string, cfg Config, method, url string, requestBody any) (PaidCallResult, error) {
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return PaidCallResult{}, fmt.Errorf("marshal request body: %w", err)
	}

	resp, err := doJSON(ctx, method, url, bodyBytes, nil)
	if err != nil {
		return PaidCallResult{}, fmt.Errorf("initial unpaid request: %w", err)
	}
	if resp.StatusCode != http.StatusPaymentRequired {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return PaidCallResult{}, fmt.Errorf("expected 402, got %d: %s", resp.StatusCode, string(respBody))
	}

	challengeBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var challenge PaymentRequired
	if err := json.Unmarshal(challengeBytes, &challenge); err != nil || len(challenge.Accepts) == 0 {
		return PaidCallResult{}, fmt.Errorf("invalid 402 challenge body: %w", err)
	}
	reqs := challenge.Accepts[0]

	if reqs.Asset != fmt.Sprint(cfg.USDCAssetID) {
		return PaidCallResult{}, fmt.Errorf("challenge quoted unexpected asset %q, want %d", reqs.Asset, cfg.USDCAssetID)
	}
	amount, err := parseUint(reqs.Amount)
	if err != nil {
		return PaidCallResult{}, fmt.Errorf("invalid amount %q in challenge: %w", reqs.Amount, err)
	}
	feePayer, _ := reqs.Extra["feePayer"].(string)
	if feePayer == "" {
		feePayer = cfg.FeePayer
	}

	group, idx, err := walletSvc.SignUSDCPaymentGroup(ctx, encMnemonic, reqs.PayTo, cfg.USDCAssetID, amount, feePayer)
	if err != nil {
		return PaidCallResult{}, fmt.Errorf("sign payment group: %w", err)
	}

	payload := PaymentPayload{
		X402Version: 2,
		Resource:    &challenge.Resource,
		Accepted:    reqs,
		Payload:     AvmExactPayload{PaymentGroup: group, PaymentIndex: idx},
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return PaidCallResult{}, fmt.Errorf("marshal payment payload: %w", err)
	}
	paymentHeader := base64.StdEncoding.EncodeToString(payloadJSON)

	paidResp, err := doJSON(ctx, method, url, bodyBytes, map[string]string{PaymentSignatureHeader: paymentHeader})
	if err != nil {
		return PaidCallResult{}, fmt.Errorf("paid retry request: %w", err)
	}
	defer paidResp.Body.Close()
	finalBody, _ := io.ReadAll(paidResp.Body)

	result := PaidCallResult{StatusCode: paidResp.StatusCode, Body: finalBody}
	if h := paidResp.Header.Get(PaymentResponseHeader); h != "" {
		settlement, err := DecodeSettleResponseHeader(h)
		if err != nil {
			return result, fmt.Errorf("paid response missing valid settlement header: %w", err)
		}
		result.Settlement = settlement
	} else if paidResp.StatusCode < 300 {
		return result, fmt.Errorf("paid response (status %d) carried no %s header — cannot confirm real settlement", paidResp.StatusCode, PaymentResponseHeader)
	}
	return result, nil
}

func doJSON(ctx context.Context, method, url string, body []byte, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return payHTTPClient.Do(req)
}

func parseUint(s string) (uint64, error) {
	var n uint64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}
