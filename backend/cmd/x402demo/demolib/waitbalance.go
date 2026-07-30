package demolib

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// WaitForAssetBalance polls algod for address's balance of assetID until it
// reaches at least minAmount or timeout elapses.
//
// Exists because settling a payment INTO an account and then immediately
// spending FROM that same account (the orchestrator's inbound-then-outbound
// chain) can race a facilitator's own balance simulation: /settle
// broadcasts and confirms the inbound transaction, but the very next
// /verify call for the outbound leg simulates against whatever round algod
// last saw, which can still be one round behind the inbound settle —
// observed live on testnet on 2026-07-31 (a 50000-micro-USDC inbound
// settle confirmed on-chain, then an immediate outbound attempt was
// rejected with "underflow ... sender amount 0", before resolving a few
// seconds later). Waiting for the receiving side to actually observe the
// new balance closes that gap.
func WaitForAssetBalance(ctx context.Context, algodURL, algodToken, address string, assetID, minAmount uint64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 10 * time.Second}

	for {
		bal, err := assetBalance(ctx, client, algodURL, algodToken, address, assetID)
		if err == nil && bal >= minAmount {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("timed out waiting for asset %d balance on %s: %w", assetID, address, err)
			}
			return fmt.Errorf("timed out waiting for asset %d balance on %s to reach %d (last seen %d)", assetID, address, minAmount, bal)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func assetBalance(ctx context.Context, client *http.Client, algodURL, algodToken, address string, assetID uint64) (uint64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, algodURL+"/v2/accounts/"+address, nil)
	if err != nil {
		return 0, err
	}
	if algodToken != "" {
		req.Header.Set("X-Algo-API-Token", algodToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("algod account lookup: status %d", resp.StatusCode)
	}
	var info struct {
		Assets []struct {
			AssetID uint64 `json:"asset-id"`
			Amount  uint64 `json:"amount"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return 0, err
	}
	for _, a := range info.Assets {
		if a.AssetID == assetID {
			return a.Amount, nil
		}
	}
	return 0, nil
}
