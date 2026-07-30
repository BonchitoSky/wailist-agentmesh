package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	cashfreeProdBaseURL    = "https://api.cashfree.com/pg"
	cashfreeSandboxBaseURL = "https://sandbox.cashfree.com/pg"
	cashfreeAPIVersion     = "2023-08-01"
)

type CashfreeClient struct {
	AppID     string
	SecretKey string
	baseURL   string
	client    *http.Client
}

func NewCashfreeClient(appID, secretKey string) *CashfreeClient {
	return &CashfreeClient{
		AppID:     appID,
		SecretKey: secretKey,
		baseURL:   cashfreeProdBaseURL,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *CashfreeClient) UseSandbox() {
	c.baseURL = cashfreeSandboxBaseURL
}

// SetBaseURLForTest points the client at a test server.
func (c *CashfreeClient) SetBaseURLForTest(url string) {
	if url == "" {
		c.baseURL = cashfreeProdBaseURL
	} else {
		c.baseURL = url
	}
}

type CashfreeOrder struct {
	OrderID          string `json:"order_id"`
	PaymentSessionID string `json:"payment_session_id"`
	OrderAmount      float64 `json:"order_amount"`
	OrderCurrency    string  `json:"order_currency"`
	OrderStatus      string  `json:"order_status"`
}

// CreateOrder creates a Cashfree order. amountPaise is in INR minor units (paise).
// orderID must be unique; customerID, customerEmail, customerPhone identify the payer.
func (c *CashfreeClient) CreateOrder(
	ctx context.Context,
	amountPaise int64,
	orderID, customerID, customerEmail, customerPhone string,
) (CashfreeOrder, error) {
	var out CashfreeOrder
	amountINR := float64(amountPaise) / 100.0

	payload := map[string]any{
		"order_id":       orderID,
		"order_amount":   amountINR,
		"order_currency": "INR",
		"customer_details": map[string]string{
			"customer_id":    customerID,
			"customer_email": customerEmail,
			"customer_phone": customerPhone,
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/orders", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return out, fmt.Errorf("cashfree: request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnauthorized {
		return out, fmt.Errorf("cashfree: authentication failed")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return out, fmt.Errorf("cashfree: order create failed with status %d: %s", resp.StatusCode, respBody)
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return out, fmt.Errorf("cashfree: parse order response: %w", err)
	}
	return out, nil
}

// GetOrderStatus fetches the current status of a Cashfree order.
// Returned status values: "ACTIVE", "PAID", "EXPIRED", "CANCELLED".
func (c *CashfreeClient) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/orders/"+orderID, nil)
	if err != nil {
		return "", err
	}
	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cashfree: get order failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cashfree: get order failed with status %d: %s", resp.StatusCode, respBody)
	}
	var order CashfreeOrder
	if err := json.Unmarshal(respBody, &order); err != nil {
		return "", fmt.Errorf("cashfree: parse order response: %w", err)
	}
	return order.OrderStatus, nil
}

// VerifyWebhookSignature validates the HMAC-SHA256 signature Cashfree sends
// in the x-webhook-signature header, computed as HMAC-SHA256(timestamp + rawBody)
// using the webhook secret, then base64-encoded.
// See https://docs.cashfree.com/docs/webhook-authentication
func (c *CashfreeClient) VerifyWebhookSignature(body []byte, signature, timestamp string) bool {
	mac := hmac.New(sha256.New, []byte(c.SecretKey))
	mac.Write([]byte(timestamp))
	mac.Write(body)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func (c *CashfreeClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-version", cashfreeAPIVersion)
	req.Header.Set("x-client-id", c.AppID)
	req.Header.Set("x-client-secret", c.SecretKey)
}
