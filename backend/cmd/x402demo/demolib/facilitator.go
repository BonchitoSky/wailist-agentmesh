package demolib

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// FacilitatorClient talks to the real GoPlausible facilitator using the
// wire dialect this package matches (see wire.go's doc comment) — verified
// live against facilitator.goplausible.xyz on 2026-07-30.
type FacilitatorClient struct {
	baseURL string
	client  *http.Client
}

func NewFacilitatorClient(baseURL string) *FacilitatorClient {
	return &FacilitatorClient{baseURL: baseURL, client: &http.Client{Timeout: 20 * time.Second}}
}

func (c *FacilitatorClient) Verify(ctx context.Context, payload PaymentPayload, reqs PaymentRequirements) (VerifyResponse, error) {
	var out VerifyResponse
	err := c.post(ctx, "/verify", VerifyRequest{X402Version: 2, PaymentPayload: payload, PaymentRequirements: reqs}, &out)
	return out, err
}

func (c *FacilitatorClient) Settle(ctx context.Context, payload PaymentPayload, reqs PaymentRequirements) (SettleResponse, error) {
	var out SettleResponse
	err := c.post(ctx, "/settle", SettleRequest{X402Version: 2, PaymentPayload: payload, PaymentRequirements: reqs}, &out)
	return out, err
}

func (c *FacilitatorClient) post(ctx context.Context, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("facilitator %s: server error %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
