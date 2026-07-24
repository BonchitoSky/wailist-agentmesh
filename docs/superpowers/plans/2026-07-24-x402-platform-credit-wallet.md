# x402 Platform Credit Wallet Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace per-agent-wallet funding of the x402 relay's inbound leg with a single platform-owned "Wallet 1" spend wallet, gated and reconciled against each user's credit balance for the real on-chain amount actually settled — not a flat fee.

**Architecture:** Credits already exist 1:1 with USD (`credit_balance_usd_micros`). Today `executeTool402V2Relay` pays the relay's inbound leg from the triggering agent's own Algorand wallet, and `runner.go` debits a flat $0.50 platform fee regardless of the real settled amount. This plan swaps the payer to a new platform-owned Wallet 1 (funded by the operator, holds real USDC), threads the real settled amount back out of the payment call, and changes the preflight/debit in `runner.go` to gate and charge that real amount against the triggering user's credits. Wallet 2 (`PlatformWalletAddress`, already built) is untouched — it still settles the inbound leg and pays the downstream target. The legacy flat-quote dialect (`ExecuteTool402`, direct-pay via the agent's own wallet) is not touched by this plan; it keeps its existing flat-fee billing.

**Tech Stack:** Go, go-algorand-sdk/v2, existing `internal/wallet`, `internal/engine`, `internal/engine/nodes`, `internal/models` packages.

## Global Constraints

- USDC ASA id: mainnet `31566704`, testnet `10458941` — reuse the constants already computed in `cmd/server/main.go`, never re-hardcode a different value.
- USDC has 6 decimals, the same scale as `credit_balance_usd_micros` and `X402PlatformFeeUSDMicros` — asset base-unit amounts convert 1:1 to USD micros, no scaling math anywhere in this plan.
- `ENCRYPTION_KEY` (32-byte hex) is the single key that encrypts every wallet mnemonic in this system (agent wallets, Wallet 2, and now Wallet 1). Wallet 1's mnemonic must be encrypted with the exact same key the deployed backend uses, or `DecryptMnemonic`/signing calls fail at runtime.
- New env vars follow the existing fail-fast pattern used for `PLATFORM_WALLET_ADDRESS`/`PLATFORM_WALLET_ENC_MNEMONIC` in `cmd/server/main.go`: `PLATFORM_SPEND_WALLET_ADDRESS`, `PLATFORM_SPEND_WALLET_ENC_MNEMONIC` — missing either is `log.Fatal` at boot, never silently defaulted.
- Reuse `nodes.BalanceChecker` (already defined in `internal/engine/nodes/billing.go:39`, `func(ctx context.Context, amountUSDMicros int64) error`) for the new preflight. Do not introduce a second checker type — this is the same pattern already used for `NodeTypeAgent`.
- The legacy flat-quote dialect (`ExecuteTool402`, agent's-own-wallet direct pay) keeps byte-identical behavior, wallet, and fee accounting — it is only wrapped in the new return type, nothing about what it does changes.
- Raw mnemonics never appear in a commit, a log line, or this plan's own file — the CLI tool in Task 1 prints one to stdout for the operator to capture once, nothing more.
- `go build ./... && go vet ./... && go test ./...` must stay green after every task.

---

### Task 1: Wallet 1 provisioning CLI

**Files:**
- Create: `cmd/walletgen/main.go`
- Create: `cmd/walletgen/README.md`

**Interfaces:**
- Consumes: `wallet.NewService(encKey, algodURL, algodToken, network string) *Service`, `(*Service).GenerateWallet() (address, encMnemonic string, err error)`, `(*Service).OptInAsset(ctx, encMnemonic string, assetID uint64) (string, error)`, `wallet.Encrypt(plaintext, key string) (string, error)`, SDK's `mnemonic.ToPrivateKey(string) (ed25519.PrivateKey, error)` and `crypto.AccountFromPrivateKey(ed25519.PrivateKey) (Account, error)`.
- Produces: a standalone binary, no other task depends on its Go symbols.

- [ ] **Step 1: Write the CLI**

```go
// Command walletgen provisions an Algorand account for use as one of
// AgentMesh's platform wallets and prints its address plus an
// ENCRYPTION_KEY-encrypted mnemonic ready to paste into env vars. Run this
// locally, never in CI or committed anywhere — its stdout is a secret.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	sdkcrypto "github.com/algorand/go-algorand-sdk/v2/crypto"
	"github.com/algorand/go-algorand-sdk/v2/mnemonic"

	"github.com/agentmesh/backend/internal/wallet"
)

func main() {
	encKey := flag.String("enc-key", "", "32-byte hex ENCRYPTION_KEY (required, must match the deployed backend's ENCRYPTION_KEY)")
	algodURL := flag.String("algod-url", "https://mainnet-api.algonode.cloud", "algod REST endpoint used for the opt-in transaction")
	algodToken := flag.String("algod-token", "", "algod API token, if the endpoint requires one")
	network := flag.String("network", "mainnet", "algorand network: mainnet or testnet")
	assetID := flag.Uint64("asset-id", 31566704, "USDC ASA id to opt into (mainnet 31566704, testnet 10458941)")
	importMnemonic := flag.String("import-mnemonic", "", "25-word Algorand mnemonic to import instead of generating a fresh account (e.g. one already generated in Pera or Defly)")
	skipOptIn := flag.Bool("skip-opt-in", false, "skip the USDC opt-in transaction (do this if the account isn't funded with ALGO yet; opt in separately once it is)")
	flag.Parse()

	if *encKey == "" {
		log.Fatal("-enc-key is required")
	}

	svc := wallet.NewService(*encKey, *algodURL, *algodToken, *network)

	var address, encMnemonic string
	var err error
	if *importMnemonic != "" {
		address, encMnemonic, err = importWallet(*importMnemonic, *encKey)
	} else {
		address, encMnemonic, err = svc.GenerateWallet()
	}
	if err != nil {
		log.Fatalf("wallet setup failed: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Address: %s\n", address)
	fmt.Fprintf(os.Stderr, "Fund this address with ALGO (fees + min balance) and USDC (spend balance) before use.\n")

	if !*skipOptIn {
		txID, err := svc.OptInAsset(context.Background(), encMnemonic, *assetID)
		if err != nil {
			log.Fatalf("USDC opt-in failed (fund the address with a small amount of ALGO first, then re-run with the same -import-mnemonic and -skip-opt-in=false): %v", err)
		}
		fmt.Fprintf(os.Stderr, "Opted into asset %d (txid %s)\n", *assetID, txID)
	}

	fmt.Println(encMnemonic)
}

func importWallet(mn, encKey string) (address, encMnemonic string, err error) {
	sk, err := mnemonic.ToPrivateKey(mn)
	if err != nil {
		return "", "", fmt.Errorf("invalid mnemonic: %w", err)
	}
	acc, err := sdkcrypto.AccountFromPrivateKey(sk)
	if err != nil {
		return "", "", err
	}
	enc, err := wallet.Encrypt(mn, encKey)
	if err != nil {
		return "", "", err
	}
	return acc.Address.String(), enc, nil
}
```

- [ ] **Step 2: Write the usage README**

```markdown
# walletgen

Provisions an Algorand account for AgentMesh's platform wallets (Wallet 1
spend wallet, or Wallet 2 settlement wallet if re-provisioning). Prints the
address to stderr and the ENCRYPTION_KEY-encrypted mnemonic to stdout.

Generate fresh:

    go run ./cmd/walletgen -enc-key "$ENCRYPTION_KEY" -network mainnet > wallet1.enc

Import a mnemonic you already generated in Pera or Defly:

    go run ./cmd/walletgen -enc-key "$ENCRYPTION_KEY" -network mainnet -import-mnemonic "word1 word2 ... word25" > wallet1.enc

The account needs a small ALGO balance before it can opt into USDC (opt-in
is itself a transaction with a fee and raises the account's minimum
balance). If opt-in fails because the account is unfunded, send it ~0.5
ALGO, then re-run with the same -import-mnemonic (or -skip-opt-in=false).

The printed mnemonic is a secret. Copy the encrypted value into
PLATFORM_SPEND_WALLET_ENC_MNEMONIC (or PLATFORM_WALLET_ENC_MNEMONIC),
clear your terminal scrollback, and never commit the output file.
```

- [ ] **Step 3: Verify it builds**

Run: `go build ./cmd/walletgen/...`
Expected: exits 0, no output.

Run: `go vet ./cmd/walletgen/...`
Expected: exits 0, no output.

- [ ] **Step 4: Commit**

```bash
git add cmd/walletgen/main.go cmd/walletgen/README.md
git commit -m "add walletgen CLI for provisioning platform Algorand wallets"
```

---

### Task 2: Real-settled-amount return type and preflight in tool402.go

**Files:**
- Modify: `internal/engine/nodes/tool402.go`
- Modify: `internal/engine/nodes/billing.go:20-23` (stale comment)
- Modify: `internal/models/types.go:213-216` (new DebitKind)
- Test: `internal/engine/nodes/tool402_test.go`

**Interfaces:**
- Consumes: `nodes.BalanceChecker` (`internal/engine/nodes/billing.go:39`, already defined — `func(ctx context.Context, amountUSDMicros int64) error`).
- Produces: `nodes.Tool402PaymentResult{Response any, SettledUSDMicros int64, DebitKind string}`; new `ExecuteTool402V2` and `executeTool402V2Relay` signatures (below) that Task 3's `runner.go` change consumes; `models.DebitKindX402RelayCost` constant.

- [ ] **Step 1: Add the new DebitKind constant**

In `internal/models/types.go`, replace:

```go
const (
	DebitKindByokFlatFee     = "byok_flat_fee"
	DebitKindX402PlatformFee = "x402_platform_fee"
)
```

with:

```go
const (
	DebitKindByokFlatFee     = "byok_flat_fee"
	DebitKindX402PlatformFee = "x402_platform_fee"
	DebitKindX402RelayCost   = "x402_relay_cost"
)
```

- [ ] **Step 2: Fix the stale billing.go comment**

In `internal/engine/nodes/billing.go`, replace:

```go
// Trigger/End, and stay free. Tool402 is metered separately (a $0.50
// flat fee gated on whether a payment actually happened at runtime, not
// on static config), so it always returns false here.
```

with:

```go
// Trigger/End, and stay free. Tool402 is metered separately — relay-path
// payments charge the real settled amount, legacy direct-pay charges a
// flat fee, both gated on whether a payment actually happened at runtime,
// not on static config — so it always returns false here.
```

- [ ] **Step 3: Add Tool402PaymentResult and rewrite executeTool402V2Relay**

In `internal/engine/nodes/tool402.go`, replace the `USDCGroupSigner` interface block and everything after it (from `// USDCGroupSigner signs...` to end of file) with:

```go
// USDCGroupSigner signs a gasless USDC atomic-payment group for the relay's
// X-Payment header. Satisfied by *wallet.Service (SignUSDCPaymentGroup).
type USDCGroupSigner interface {
	SignUSDCPaymentGroup(ctx context.Context, encMnemonic, payTo string, assetID, amountMicros uint64, feePayerAddr string) ([]string, int, error)
}

// Tool402PaymentResult is what ExecuteTool402V2 returns, so the caller can
// debit the amount actually charged instead of assuming a fixed fee.
// SettledUSDMicros is 0 and DebitKind is "" when no payment was sent (e.g.
// the endpoint didn't require one, or no wallet was configured).
type Tool402PaymentResult struct {
	Response         any
	SettledUSDMicros int64
	DebitKind        string
}

// ExecuteTool402V2 is the entry point runner.go calls for tool402 nodes. It
// inspects the target's 402 quote shape: a real x402 v2 challenge (accepts[])
// is routed through the AgentMesh relay so both payment legs are real,
// GoPlausible-settled, and attributable to us as an orchestrator entry, paid
// from the platform's own Wallet 1 spend wallet and gated/charged against
// the triggering user's credits for the real settled amount. The legacy
// flat-quote dialect (no accepts[]) bypasses the relay entirely and keeps
// today's direct-pay-from-the-agent's-own-wallet behavior, gated/charged at
// the fixed platform fee — it was never GoPlausible-compliant and isn't
// becoming so.
func ExecuteTool402V2(ctx context.Context, node models.WorkflowNode, rc RunContexter, aw models.AgentWallet, signer WalletSigner, usdcSigner USDCGroupSigner, platformSpendEncMnemonic string, relayBaseURL string, checkBalance BalanceChecker) (Tool402PaymentResult, error) {
	if err := urlValidator(node.Endpoint); err != nil {
		return Tool402PaymentResult{}, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, node.Endpoint, nil)
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return Tool402PaymentResult{}, err
	}

	if resp.StatusCode != http.StatusPaymentRequired {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, httpResponseLimit))
		var result any
		if json.Unmarshal(b, &result) == nil {
			return Tool402PaymentResult{Response: result}, nil
		}
		return Tool402PaymentResult{Response: string(b)}, nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, httpResponseLimit))
	resp.Body.Close()
	var v2Challenge struct {
		Accepts []map[string]any `json:"accepts"`
	}
	if json.Unmarshal(body, &v2Challenge) == nil && len(v2Challenge.Accepts) > 0 {
		return executeTool402V2Relay(ctx, node, usdcSigner, platformSpendEncMnemonic, relayBaseURL, checkBalance)
	}

	// Legacy flat-quote dialect: unchanged direct-pay path, flat-fee billing.
	if err := checkBalance(ctx, models.X402PlatformFeeUSDMicros); err != nil {
		return Tool402PaymentResult{}, err
	}
	result, err := ExecuteTool402(ctx, node, rc, aw, signer)
	if err != nil {
		return Tool402PaymentResult{}, err
	}
	out := Tool402PaymentResult{Response: result}
	if m, ok := result.(map[string]any); ok {
		if _, hasTx := m["txId"]; hasTx {
			out.SettledUSDMicros = models.X402PlatformFeeUSDMicros
			out.DebitKind = models.DebitKindX402PlatformFee
		}
	}
	return out, nil
}

func executeTool402V2Relay(ctx context.Context, node models.WorkflowNode, usdcSigner USDCGroupSigner, platformSpendEncMnemonic string, relayBaseURL string, checkBalance BalanceChecker) (Tool402PaymentResult, error) {
	if platformSpendEncMnemonic == "" || usdcSigner == nil {
		return Tool402PaymentResult{Response: map[string]any{"error": "payment required but no platform spend wallet configured"}}, nil
	}

	relayURL := relayBaseURL + "/x402/relay?target=" + url.QueryEscape(node.Endpoint)

	quoteReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, relayURL, nil)
	quoteResp, err := toolHTTPClient.Do(quoteReq)
	if err != nil {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay quote failed: %w", err)
	}
	quoteBody, _ := io.ReadAll(io.LimitReader(quoteResp.Body, httpResponseLimit))
	quoteResp.Body.Close()

	var relayChallenge struct {
		Accepts []map[string]any `json:"accepts"`
	}
	if json.Unmarshal(quoteBody, &relayChallenge) != nil || len(relayChallenge.Accepts) == 0 {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay: invalid challenge response")
	}
	accept := relayChallenge.Accepts[0]
	payTo, _ := accept["payTo"].(string)
	assetStr, _ := accept["asset"].(string)
	amountStr, _ := accept["maxAmountRequired"].(string)
	var feePayer string
	if extra, ok := accept["extra"].(map[string]any); ok {
		feePayer, _ = extra["feePayer"].(string)
	}
	assetID, _ := strconv.ParseUint(assetStr, 10, 64)
	amount, _ := strconv.ParseUint(amountStr, 10, 64)

	// USDC's 6 decimals match credit_balance_usd_micros' scale exactly —
	// the relay's asset base-unit amount converts to USD micros 1:1.
	if err := checkBalance(ctx, int64(amount)); err != nil {
		return Tool402PaymentResult{}, err
	}

	group, idx, err := usdcSigner.SignUSDCPaymentGroup(ctx, platformSpendEncMnemonic, payTo, assetID, amount, feePayer)
	if err != nil {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay payment signing failed: %w", err)
	}
	xPayment, _ := json.Marshal(map[string]any{
		"x402Version": 2, "scheme": "exact",
		"payload": map[string]any{"paymentGroup": group, "paymentIndex": idx},
	})

	payReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, relayURL, nil)
	payReq.Header.Set("X-Payment", string(xPayment))
	payResp, err := toolHTTPClient.Do(payReq)
	if err != nil {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay payment request failed: %w", err)
	}
	defer payResp.Body.Close()
	finalBody, _ := io.ReadAll(io.LimitReader(payResp.Body, httpResponseLimit))

	out := Tool402PaymentResult{SettledUSDMicros: int64(amount), DebitKind: models.DebitKindX402RelayCost}
	var result any
	if json.Unmarshal(finalBody, &result) == nil {
		out.Response = result
	} else {
		out.Response = string(finalBody)
	}
	return out, nil
}
```

- [ ] **Step 4: Update the three existing ExecuteTool402V2 call sites in tests**

In `internal/engine/nodes/tool402_test.go`, `TestX402V2TargetRoutesThroughRelay` (around line 194), replace:

```go
	result, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, usdcSigner, relay.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !targetHit {
		t.Fatal("want relay to have queried target's real price first")
	}
	if !relayHit {
		t.Fatal("want relay to have been called")
	}
	m, ok := result.(map[string]any)
	if !ok || m["data"] != "relayed paid response" {
		t.Fatalf("want relayed response, got %v", result)
	}
}
```

with:

```go
	checkBalance := func(context.Context, int64) error { return nil }
	paymentResult, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, usdcSigner, "platform-enc-mnemonic", relay.URL, checkBalance)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !targetHit {
		t.Fatal("want relay to have queried target's real price first")
	}
	if !relayHit {
		t.Fatal("want relay to have been called")
	}
	if paymentResult.SettledUSDMicros != 100000 {
		t.Fatalf("want settled amount 100000 (matches maxAmountRequired), got %d", paymentResult.SettledUSDMicros)
	}
	if paymentResult.DebitKind != models.DebitKindX402RelayCost {
		t.Fatalf("want debit kind %q, got %q", models.DebitKindX402RelayCost, paymentResult.DebitKind)
	}
	m, ok := paymentResult.Response.(map[string]any)
	if !ok || m["data"] != "relayed paid response" {
		t.Fatalf("want relayed response, got %v", paymentResult.Response)
	}
}
```

In `TestX402LegacyTargetBypassesRelay` (around line 235), replace:

```go
	result, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, usdcSigner, relay.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if relayHit {
		t.Fatal("legacy target must bypass the relay entirely")
	}
	m := result.(map[string]any)
	if m["txId"] != "TX-SIGNED-123" {
		t.Fatalf("want legacy direct-pay path unchanged, got %v", m)
	}
}
```

with:

```go
	checkBalance := func(context.Context, int64) error { return nil }
	paymentResult, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, usdcSigner, "platform-enc-mnemonic", relay.URL, checkBalance)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if relayHit {
		t.Fatal("legacy target must bypass the relay entirely")
	}
	if paymentResult.SettledUSDMicros != models.X402PlatformFeeUSDMicros {
		t.Fatalf("want flat fee %d, got %d", models.X402PlatformFeeUSDMicros, paymentResult.SettledUSDMicros)
	}
	if paymentResult.DebitKind != models.DebitKindX402PlatformFee {
		t.Fatalf("want debit kind %q, got %q", models.DebitKindX402PlatformFee, paymentResult.DebitKind)
	}
	m := paymentResult.Response.(map[string]any)
	if m["txId"] != "TX-SIGNED-123" {
		t.Fatalf("want legacy direct-pay path unchanged, got %v", m)
	}
}
```

In `TestX402V2TargetWithAmpersandInQueryString` (around line 279), replace:

```go
	result, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, usdcSigner, relay.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the relay received the full endpoint URL, not truncated at &
	if capturedTargetParam != endpointWithQuery {
		t.Fatalf("want target param %q, got %q (was truncated at &)", endpointWithQuery, capturedTargetParam)
	}

	m, ok := result.(map[string]any)
```

with:

```go
	checkBalance := func(context.Context, int64) error { return nil }
	paymentResult, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, usdcSigner, "platform-enc-mnemonic", relay.URL, checkBalance)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the relay received the full endpoint URL, not truncated at &
	if capturedTargetParam != endpointWithQuery {
		t.Fatalf("want target param %q, got %q (was truncated at &)", endpointWithQuery, capturedTargetParam)
	}

	m, ok := paymentResult.Response.(map[string]any)
```

(Check whether `models` is already imported in this test file — it is, per the existing `models.WorkflowNode`/`models.AgentWallet` usage above; no new import needed.)

- [ ] **Step 5: Add a test proving the preflight gates on the real relay amount, not the flat fee**

Append to `internal/engine/nodes/tool402_test.go`:

```go
// TestX402V2RelayPreflightUsesRealAmount verifies the balance check gates on
// the relay's real maxAmountRequired, not the flat platform fee — a
// checkBalance that only tolerates the flat fee must reject a costlier call.
func TestX402V2RelayPreflightUsesRealAmount(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer target.Close()

	var paid bool
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			paid = true
			w.Write([]byte(`{"data":"relayed paid response"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"PLATFORMADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer relay.Close()

	node := models.WorkflowNode{ID: "x1", Type: models.NodeTypeTool402, Endpoint: target.URL}
	rc := engine.NewRunContext("r1", nil)
	aw := models.AgentWallet{}
	usdcSigner := &mockUSDCGroupSigner{group: []string{"g0", "g1"}, idx: 0}

	var checkedAmount int64
	checkBalance := func(_ context.Context, amount int64) error {
		checkedAmount = amount
		if amount > 500_000 {
			return fmt.Errorf("insufficient credits")
		}
		return nil
	}

	// maxAmountRequired (100000) is under the flat-fee-sized ceiling this
	// checkBalance allows, so this call should succeed and pay.
	_, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, nil, usdcSigner, "platform-enc-mnemonic", relay.URL, checkBalance)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if checkedAmount != 100000 {
		t.Fatalf("want checkBalance called with real amount 100000, got %d", checkedAmount)
	}
	if !paid {
		t.Fatal("want relay to have been paid")
	}

	// Now make checkBalance reject anything over 50000 — below the real
	// 100000 cost — and verify the payment never happens.
	paid = false
	strictCheck := func(_ context.Context, amount int64) error {
		if amount > 50_000 {
			return fmt.Errorf("insufficient credits")
		}
		return nil
	}
	_, err = nodes.ExecuteTool402V2(context.Background(), node, rc, aw, nil, usdcSigner, "platform-enc-mnemonic", relay.URL, strictCheck)
	if err == nil {
		t.Fatal("want insufficient-credits error when real amount exceeds balance")
	}
	if paid {
		t.Fatal("want no payment sent when preflight rejects the real amount")
	}
}
```

- [ ] **Step 6: Run the package tests**

Run: `go test ./internal/engine/nodes/... -run TestX402 -v`
Expected: all `TestX402*` tests PASS, including the three updated and the new `TestX402V2RelayPreflightUsesRealAmount`.

- [ ] **Step 7: Run full build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all green (this will currently fail to compile in `internal/engine/runner.go` and its tests, since `ExecuteTool402V2`'s signature changed — that's expected and fixed in Task 3; if Task 3 hasn't started yet, confirm the failure is confined to `internal/engine` package compilation, not `internal/engine/nodes`).

- [ ] **Step 8: Commit**

```bash
git add internal/engine/nodes/tool402.go internal/engine/nodes/tool402_test.go internal/engine/nodes/billing.go internal/models/types.go
git commit -m "tool402: return real settled amount instead of assuming a flat fee"
```

---

### Task 3: Wire Wallet 1 through main.go and runner.go

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `internal/engine/runner.go`
- Test: `internal/api/handlers/runs_test.go`, `internal/api/handlers/stop_test.go`, `internal/engine/runner_stop_test.go`
- Test: `internal/engine/runner_test.go` (new test)

**Interfaces:**
- Consumes: `nodes.Tool402PaymentResult`, `nodes.ExecuteTool402V2(ctx, node, rc, aw, signer, usdcSigner, platformSpendEncMnemonic, relayBaseURL, checkBalance)` (Task 2).
- Produces: `engine.NewRunner(store, broker, walletSvc, relayBaseURL, platformSpendEncMnemonic string) *Runner` — the new 5th parameter.

- [ ] **Step 1: Add env vars and thread them into NewRunner in main.go**

In `cmd/server/main.go`, replace:

```go
	platformWalletAddr := os.Getenv("PLATFORM_WALLET_ADDRESS")
	platformWalletEncMnemonic := os.Getenv("PLATFORM_WALLET_ENC_MNEMONIC")
	if platformWalletAddr == "" || platformWalletEncMnemonic == "" {
		log.Fatal("PLATFORM_WALLET_ADDRESS and PLATFORM_WALLET_ENC_MNEMONIC must both be set — the platform wallet's payTo address must stay fixed for the whole competition, so it is provisioned once out-of-band, never auto-generated at startup")
	}
```

with:

```go
	platformWalletAddr := os.Getenv("PLATFORM_WALLET_ADDRESS")
	platformWalletEncMnemonic := os.Getenv("PLATFORM_WALLET_ENC_MNEMONIC")
	if platformWalletAddr == "" || platformWalletEncMnemonic == "" {
		log.Fatal("PLATFORM_WALLET_ADDRESS and PLATFORM_WALLET_ENC_MNEMONIC must both be set — the platform wallet's payTo address must stay fixed for the whole competition, so it is provisioned once out-of-band, never auto-generated at startup")
	}

	platformSpendWalletAddr := os.Getenv("PLATFORM_SPEND_WALLET_ADDRESS")
	platformSpendWalletEncMnemonic := os.Getenv("PLATFORM_SPEND_WALLET_ENC_MNEMONIC")
	if platformSpendWalletAddr == "" || platformSpendWalletEncMnemonic == "" {
		log.Fatal("PLATFORM_SPEND_WALLET_ADDRESS and PLATFORM_SPEND_WALLET_ENC_MNEMONIC must both be set — Wallet 1 pays every relayed x402 call on behalf of users' credit balances, so it is provisioned once out-of-band via cmd/walletgen, never auto-generated at startup")
	}
```

Then replace:

```go
	runner := engine.NewRunner(store, broker, walletSvc, envOr("BASE_URL", "http://localhost:8080"))
```

with:

```go
	runner := engine.NewRunner(store, broker, walletSvc, envOr("BASE_URL", "http://localhost:8080"), platformSpendWalletEncMnemonic)
```

`platformSpendWalletAddr` isn't read anywhere else in `main.go` today — it exists so the fail-fast check confirms both halves of the pair are set together, matching the existing `PLATFORM_WALLET_ADDRESS` pattern where the address is informational/for-reference (used by the operator to fund the account) while the encrypted mnemonic is what the process actually needs.

- [ ] **Step 2: Add the field and constructor param in runner.go**

In `internal/engine/runner.go`, replace:

```go
type Runner struct {
	store        *db.Store
	broker       *sse.Broker
	walletSvc    nodes.WalletSigner
	registry     *runRegistry
	relayBaseURL string
}

func NewRunner(store *db.Store, broker *sse.Broker, walletSvc nodes.WalletSigner, relayBaseURL string) *Runner {
	return &Runner{
		store:        store,
		broker:       broker,
		walletSvc:    walletSvc,
		registry:     newRunRegistry(),
		relayBaseURL: relayBaseURL,
	}
}
```

with:

```go
type Runner struct {
	store                    *db.Store
	broker                   *sse.Broker
	walletSvc                nodes.WalletSigner
	registry                 *runRegistry
	relayBaseURL             string
	platformSpendEncMnemonic string
}

func NewRunner(store *db.Store, broker *sse.Broker, walletSvc nodes.WalletSigner, relayBaseURL string, platformSpendEncMnemonic string) *Runner {
	return &Runner{
		store:                    store,
		broker:                   broker,
		walletSvc:                walletSvc,
		registry:                 newRunRegistry(),
		relayBaseURL:             relayBaseURL,
		platformSpendEncMnemonic: platformSpendEncMnemonic,
	}
}
```

- [ ] **Step 3: Rewrite the NodeTypeTool402 case**

In `internal/engine/runner.go`, replace:

```go
	case models.NodeTypeTool402:
		if err := r.preflightCheck(ctx, wf, models.X402PlatformFeeUSDMicros); err != nil {
			return nil, err
		}
		// Find the agent that has this tool attached and use its wallet.
		var aw models.AgentWallet
		for agentID, cfg := range attachMap {
			for _, t := range cfg.Tools {
				if t.ID == node.ID {
					aw = walletByAgent[agentID]
				}
			}
		}
		// r.walletSvc's dynamic type (*wallet.Service) also satisfies
		// USDCGroupSigner (Task 3); the assertion is nil-safe if a test double
		// only implements WalletSigner, and ExecuteTool402V2 falls back to a
		// graceful "no wallet configured" result rather than paying via relay.
		usdcSigner, _ := r.walletSvc.(nodes.USDCGroupSigner)
		result, err := nodes.ExecuteTool402V2(ctx, node, rc, aw, r.walletSvc, usdcSigner, r.relayBaseURL)
		if err != nil {
			return nil, err
		}
		if m, ok := result.(map[string]any); ok {
			if _, hasTx := m["txId"]; hasTx {
				r.debitOrLog(ctx, wf, run, node.ID, models.X402PlatformFeeUSDMicros, models.DebitKindX402PlatformFee)
			}
		}
		return result, nil
```

with:

```go
	case models.NodeTypeTool402:
		// Find the agent that has this tool attached and use its wallet (only
		// the legacy direct-pay dialect still needs this; the relay dialect
		// pays from the platform's own Wallet 1 spend wallet instead).
		var aw models.AgentWallet
		for agentID, cfg := range attachMap {
			for _, t := range cfg.Tools {
				if t.ID == node.ID {
					aw = walletByAgent[agentID]
				}
			}
		}
		// r.walletSvc's dynamic type (*wallet.Service) also satisfies
		// USDCGroupSigner (Task 3); the assertion is nil-safe if a test double
		// only implements WalletSigner, and ExecuteTool402V2 falls back to a
		// graceful "no wallet configured" result rather than paying via relay.
		usdcSigner, _ := r.walletSvc.(nodes.USDCGroupSigner)
		checkBalance := func(cctx context.Context, amount int64) error {
			return r.preflightCheck(cctx, wf, amount)
		}
		paymentResult, err := nodes.ExecuteTool402V2(ctx, node, rc, aw, r.walletSvc, usdcSigner, r.platformSpendEncMnemonic, r.relayBaseURL, checkBalance)
		if err != nil {
			return nil, err
		}
		if paymentResult.SettledUSDMicros > 0 {
			r.debitOrLog(ctx, wf, run, node.ID, paymentResult.SettledUSDMicros, paymentResult.DebitKind)
		}
		return paymentResult.Response, nil
```

Note the removed upfront `r.preflightCheck(ctx, wf, models.X402PlatformFeeUSDMicros)` call — the gate now happens inside `ExecuteTool402V2` via `checkBalance`, at the real amount for the relay dialect and the flat fee for the legacy dialect, so there is no longer a separate flat-fee gate ahead of it.

- [ ] **Step 4: Update the five existing NewRunner call sites**

In `internal/api/handlers/runs_test.go` (line 22), `internal/api/handlers/stop_test.go` (lines 21, 46, 69), replace each:

```go
	d.Engine = engine.NewRunner(d.Store, d.Broker, d.Wallet, "http://localhost:8080")
```

with:

```go
	d.Engine = engine.NewRunner(d.Store, d.Broker, d.Wallet, "http://localhost:8080", "")
```

In `internal/engine/runner_stop_test.go` (line 33), replace:

```go
	return engine.NewRunner(store, broker, &noopSigner{}, "http://localhost:8080"), store
```

with:

```go
	return engine.NewRunner(store, broker, &noopSigner{}, "http://localhost:8080", ""), store
```

(Empty string is correct here — none of these tests exercise a tool402 node, so the platform spend wallet is never used.)

- [ ] **Step 5: Run the affected package tests**

Run: `go test ./internal/engine/... ./internal/api/handlers/... -v`
Expected: all PASS, including the pre-existing `TestStop*`/`TestRuns*` suites and Task 2's `TestX402*` suites (now compiling against the real `Runner`).

- [ ] **Step 6: Run full build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all packages green.

- [ ] **Step 7: Commit**

```bash
git add cmd/server/main.go internal/engine/runner.go internal/api/handlers/runs_test.go internal/api/handlers/stop_test.go internal/engine/runner_stop_test.go
git commit -m "runner: pay relayed x402 calls from platform Wallet 1, gate and debit the real settled amount"
```

---

### Task 4: Operator runbook note

**Files:**
- Create: `docs/superpowers/plans/2026-07-24-wallet1-operator-runbook.md`

This is documentation only, executed by the human operator, not a subagent — same status as Task 8 of the prior orchestrator-relay plan.

- [ ] **Step 1: Write the runbook**

```markdown
# Wallet 1 / Wallet 2 operator runbook

## One-time setup

1. Provision Wallet 1 (platform spend wallet):
   `go run ./cmd/walletgen -enc-key "$ENCRYPTION_KEY" -network mainnet > wallet1.enc`
   (or `-import-mnemonic "..."` if you generated it yourself in Pera/Defly first).
2. Fund the printed address with ALGO (fees + min balance) and USDC (the
   actual spend balance — this is the money that pays relayed x402 calls).
3. Set `PLATFORM_SPEND_WALLET_ADDRESS` (the printed address) and
   `PLATFORM_SPEND_WALLET_ENC_MNEMONIC` (the printed encrypted mnemonic,
   stdout) in the deploy environment. Delete `wallet1.enc` once copied in;
   clear terminal scrollback.
4. Wallet 2 (`PLATFORM_WALLET_ADDRESS`) is unchanged — it already exists
   and keeps settling the inbound leg and paying downstream targets.

## Ongoing: manual sweep

Wallet 2 accumulates the spread between what Wallet 1 pays in and what
downstream targets actually charge, plus anything from other margin
sources. This is not swept automatically. Periodically (weekly, or when
Wallet 2's balance looks large relative to daily volume):

1. Check Wallet 2's balance.
2. Leave a working minimum in Wallet 2 (enough to cover a few days of
   expected settlement volume).
3. Send the rest back to Wallet 1 (to keep funding relayed calls) or to a
   separate treasury wallet, manually, via a plain Algorand transfer.

## NOWPayments payout caveat

If Wallet 1 is also set as the NOWPayments payout-receiving wallet: confirm
NOWPayments actually supports payouts in USDC on the Algorand network
specifically before relying on it — this was not verified as part of this
plan. If it isn't supported, credits and Wallet 1's on-chain balance are
already decoupled (credits are a database tally), so Wallet 1 can be
funded through any means — it just needs to hold enough balance in
aggregate, not a literal per-purchase transfer.
```

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/plans/2026-07-24-wallet1-operator-runbook.md
git commit -m "docs: add Wallet 1 / Wallet 2 operator runbook"
```

---

## Out of scope (explicitly not built here)

- Automatic sweeping of Wallet 2's balance — the user asked for manual control over this.
- Any additional platform margin/fee stacked on top of the real relay-path settled amount — the user described 1:1 credit debit for real cost; a separate fee constant can be added later as its own change if wanted.
- Changing the legacy flat-quote dialect's wallet or fee model — explicitly preserved.
- NOWPayments payout-currency verification — flagged in Task 4's runbook, not a code change.
