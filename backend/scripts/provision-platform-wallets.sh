#!/usr/bin/env bash
# Interactively provisions the two mainnet platform wallets AgentMesh's
# backend needs at boot (PLATFORM_WALLET = Wallet 2 / payTo,
# PLATFORM_SPEND_WALLET = Wallet 1 / spend) and optionally pushes the
# resulting env vars straight to Railway.
#
# Generates fresh Algo25 wallets via ./cmd/walletgen rather than importing
# an existing Pera "Universal Wallet" (24-word BIP-39) account: this
# codebase's ENCRYPTION_KEY/encoding scheme only speaks Algo25, and there is
# no supported way to export a Universal Wallet account's key in that
# format. Fund the freshly generated addresses from your existing wallets
# with a normal transfer instead.
#
# Nothing sensitive touches disk: the raw mnemonic is shown on your
# terminal exactly once per wallet (stderr, live) and never written to a
# file. Only the ENCRYPTION_KEY-encrypted mnemonic (ciphertext) is held in
# memory for the rest of the run, to build the final env var summary and,
# if you opt in, push to Railway.
#
# Run from anywhere; it cds to backend/ itself.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

echo "=== AgentMesh platform wallet provisioning ==="
echo

read -r -s -p "ENCRYPTION_KEY (must match the one already set on your deploy — hidden input): " ENC_KEY
echo
if [ -z "$ENC_KEY" ]; then
  echo "ENCRYPTION_KEY is required." >&2
  exit 1
fi

read -r -p "Network [mainnet]: " NETWORK
NETWORK=${NETWORK:-mainnet}

if [ "$NETWORK" = "mainnet" ]; then
  DEFAULT_ALGOD="https://mainnet-api.algonode.cloud"
  ASSET_ID=31566704
else
  DEFAULT_ALGOD="https://testnet-api.algonode.cloud"
  ASSET_ID=10458941
fi
read -r -p "Algod URL [$DEFAULT_ALGOD]: " ALGOD_URL
ALGOD_URL=${ALGOD_URL:-$DEFAULT_ALGOD}

declare -A ADDR
declare -A ENC

provision_one() {
  local var_prefix="$1" label="$2"
  echo
  echo "--------------------------------------------------------------"
  echo "Provisioning $var_prefix ($label)"
  echo "--------------------------------------------------------------"

  local full_output enc_mnemonic info address
  full_output=$(go run ./cmd/walletgen \
    -enc-key="$ENC_KEY" -network="$NETWORK" -algod-url="$ALGOD_URL" \
    -skip-opt-in -show-mnemonic 2>&1)

  enc_mnemonic=$(printf '%s\n' "$full_output" | tail -n1)
  info=$(printf '%s\n' "$full_output" | head -n -1)
  address=$(printf '%s\n' "$info" | grep -oP 'Address: \K\S+' || true)

  if [ -z "$address" ] || [ -z "$enc_mnemonic" ]; then
    echo "walletgen did not produce an address/encrypted mnemonic — full output:" >&2
    printf '%s\n' "$full_output" >&2
    exit 1
  fi

  printf '%s\n' "$info"
  echo
  echo ">>> Import the 25 words above into Pera now: Add Wallet > Import Account > Algo25 (legacy)."
  read -r -p "Press Enter once you've written it down / imported it... "

  echo
  echo ">>> Send at least 2 ALGO to $address (covers fees, min balance, USDC opt-in)."
  read -r -p "Press Enter once that ALGO transfer has confirmed... "

  echo "Opting $var_prefix into USDC (asset $ASSET_ID)..."
  go run ./cmd/walletgen \
    -enc-key="$ENC_KEY" -network="$NETWORK" -algod-url="$ALGOD_URL" \
    -asset-id="$ASSET_ID" -opt-in-only="$enc_mnemonic"

  echo
  echo ">>> Now send USDC to $address from your existing wallet (can also do this later)."
  read -r -p "Press Enter once sent, or just Enter to skip for now... "

  ADDR[$var_prefix]=$address
  ENC[$var_prefix]=$enc_mnemonic
}

provision_one PLATFORM_WALLET "Wallet 2 — payTo, receives inbound settlements"
provision_one PLATFORM_SPEND_WALLET "Wallet 1 — spend, pays outbound on behalf of user credits"

echo
echo "================================================================"
echo "Done. Env vars for your deploy (ciphertext only — safe to store):"
echo "================================================================"
echo "ALGORAND_NETWORK=$NETWORK"
echo "ALGOD_URL=$ALGOD_URL"
echo "PLATFORM_WALLET_ADDRESS=${ADDR[PLATFORM_WALLET]}"
echo "PLATFORM_WALLET_ENC_MNEMONIC=${ENC[PLATFORM_WALLET]}"
echo "PLATFORM_SPEND_WALLET_ADDRESS=${ADDR[PLATFORM_SPEND_WALLET]}"
echo "PLATFORM_SPEND_WALLET_ENC_MNEMONIC=${ENC[PLATFORM_SPEND_WALLET]}"
echo
echo "The raw 25-word mnemonics were only ever shown above, once each — not"
echo "repeated here. Clear your terminal scrollback once you've imported"
echo "them into a wallet app and/or written them down offline."
echo

read -r -p "Push these 6 vars to Railway now via 'railway variable set' (--skip-deploys, no redeploy triggered)? [y/N] " PUSH
if [ "$PUSH" = "y" ] || [ "$PUSH" = "Y" ]; then
  read -r -p "Railway service name [wailist-agentmesh]: " SVC
  SVC=${SVC:-wailist-agentmesh}
  read -r -p "Railway environment [production]: " ENVIRONMENT
  ENVIRONMENT=${ENVIRONMENT:-production}

  railway variable set "ALGORAND_NETWORK=$NETWORK" --service "$SVC" --environment "$ENVIRONMENT" --skip-deploys
  railway variable set "ALGOD_URL=$ALGOD_URL" --service "$SVC" --environment "$ENVIRONMENT" --skip-deploys
  railway variable set "PLATFORM_WALLET_ADDRESS=${ADDR[PLATFORM_WALLET]}" --service "$SVC" --environment "$ENVIRONMENT" --skip-deploys
  railway variable set "PLATFORM_WALLET_ENC_MNEMONIC=${ENC[PLATFORM_WALLET]}" --service "$SVC" --environment "$ENVIRONMENT" --skip-deploys
  railway variable set "PLATFORM_SPEND_WALLET_ADDRESS=${ADDR[PLATFORM_SPEND_WALLET]}" --service "$SVC" --environment "$ENVIRONMENT" --skip-deploys
  railway variable set "PLATFORM_SPEND_WALLET_ENC_MNEMONIC=${ENC[PLATFORM_SPEND_WALLET]}" --service "$SVC" --environment "$ENVIRONMENT" --skip-deploys

  echo
  echo "Vars set (deploy NOT triggered — deploy manually once you've verified the current"
  echo "deployment's health, since it was showing a failed status before this ran)."
else
  echo "Skipped Railway push. Set the 6 vars above manually when ready."
fi
