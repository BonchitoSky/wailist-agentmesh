// Package demolib holds everything shared by the x402-demo's three
// processes (merchant, orchestrator, client): wire types, a facilitator
// client, config, the resume scorer, and a wallet-signing helper.
//
// The wire types here deliberately do NOT reuse
// internal/x402.PaymentRequirements/PaymentPayload (AgentMesh's own
// production relay dialect: flat scheme/network, maxAmountRequired). They
// instead mirror, field-for-field, what a real live x402-global-challenge
// entry actually sends on the wire — verified by decoding Prism's
// (https://prism-99h2.onrender.com/resume-screen-accurate) real mainnet 402
// challenge on 2026-07-30, and cross-checked against the official
// @x402/core v2.20.0 TypeScript SDK's PaymentRequirements/PaymentPayload
// types. Both AgentMesh's own dialect and this one were confirmed to parse
// successfully against the real facilitator (facilitator.goplausible.xyz)
// in manual /verify probes the same day, so this isn't "the AgentMesh
// dialect was broken" — it's "match the reference entry's bytes exactly,
// since that's the concrete example we're targeting."
package demolib

// ResourceInfo describes the protected resource a 402 challenge is for.
type ResourceInfo struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType"`
}

// PaymentRequirements is one accepted way to pay for a resource. Field
// names and shape match the real wire format (amount, not
// maxAmountRequired) confirmed live against Prism's endpoint and the
// facilitator itself.
type PaymentRequirements struct {
	Scheme            string         `json:"scheme"`
	Network           string         `json:"network"`
	Amount            string         `json:"amount"`
	Asset             string         `json:"asset"`
	PayTo             string         `json:"payTo"`
	MaxTimeoutSeconds int            `json:"maxTimeoutSeconds"`
	Extra             map[string]any `json:"extra,omitempty"`
}

// PaymentRequired is the full 402 challenge body.
type PaymentRequired struct {
	X402Version int                   `json:"x402Version"`
	Error       string                `json:"error,omitempty"`
	Resource    ResourceInfo          `json:"resource"`
	Accepts     []PaymentRequirements `json:"accepts"`
	Extensions  map[string]any        `json:"extensions,omitempty"`
}

// AvmExactPayload is the AVM "exact" scheme's payload shape: a signed
// 2-txn atomic group (msgpack+base64 each) plus which index is the real
// payment (see wallet.Service.SignUSDCPaymentGroup).
type AvmExactPayload struct {
	PaymentGroup []string `json:"paymentGroup"`
	PaymentIndex int      `json:"paymentIndex"`
}

// PaymentPayload is what a client sends back (as the X-Payment header,
// base64-JSON-encoded) once it has signed a payment against one of a 402
// challenge's PaymentRequirements. Note `Accepted`, not a flat
// scheme/network pair — this is the field the real facilitator and Prism's
// own dialect use to carry which PaymentRequirements the payload satisfies.
type PaymentPayload struct {
	X402Version int                 `json:"x402Version"`
	Resource    *ResourceInfo       `json:"resource,omitempty"`
	Accepted    PaymentRequirements `json:"accepted"`
	Payload     AvmExactPayload     `json:"payload"`
	Extensions  map[string]any      `json:"extensions,omitempty"`
}

// VerifyRequest / SettleRequest are POSTed to the facilitator's /verify and
// /settle endpoints respectively.
type VerifyRequest struct {
	X402Version         int                 `json:"x402Version"`
	PaymentPayload      PaymentPayload      `json:"paymentPayload"`
	PaymentRequirements PaymentRequirements `json:"paymentRequirements"`
}

type SettleRequest struct {
	X402Version         int                 `json:"x402Version"`
	PaymentPayload      PaymentPayload      `json:"paymentPayload"`
	PaymentRequirements PaymentRequirements `json:"paymentRequirements"`
}

type VerifyResponse struct {
	IsValid        bool   `json:"isValid"`
	InvalidReason  string `json:"invalidReason,omitempty"`
	InvalidMessage string `json:"invalidMessage,omitempty"`
	Payer          string `json:"payer,omitempty"`
}

type SettleResponse struct {
	Success      bool   `json:"success"`
	ErrorReason  string `json:"errorReason,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	Payer        string `json:"payer,omitempty"`
	Transaction  string `json:"transaction"`
	Network      string `json:"network"`
	Amount       string `json:"amount,omitempty"`
}
