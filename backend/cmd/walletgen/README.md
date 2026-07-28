# walletgen

Provisions an Algorand account for AgentMesh's platform wallets (Wallet 1
spend wallet, or Wallet 2 settlement wallet if re-provisioning). Prints the
address to stderr and the ENCRYPTION_KEY-encrypted mnemonic to stdout.

## Generate fresh

    go run ./cmd/walletgen -enc-key "$ENCRYPTION_KEY" -network mainnet -skip-opt-in > wallet1.enc

This generates a new account and prints its address and encrypted mnemonic.
It skips USDC opt-in because the account needs ALGO funding first.

Once you have funded the address with ~0.5 ALGO, complete the opt-in:

    go run ./cmd/walletgen -enc-key "$ENCRYPTION_KEY" -network mainnet -opt-in-only "$(cat wallet1.enc)"

The `-opt-in-only` flag re-uses the encrypted mnemonic you already printed,
performs opt-in against the now-funded account, and exits.

## Import a mnemonic

Import a mnemonic you already generated in Pera or Defly:

    go run ./cmd/walletgen -enc-key "$ENCRYPTION_KEY" -network mainnet -import-mnemonic "word1 word2 ... word25" > wallet1.enc

This prints the address and encrypted mnemonic. The account needs a small
ALGO balance before opting into USDC. If opt-in fails because the account
is unfunded, fund it with ~0.5 ALGO, then re-run with the same
-opt-in-only flag as shown above.

## Final step

The printed mnemonic is a secret. Copy the encrypted value into
PLATFORM_SPEND_WALLET_ENC_MNEMONIC (or PLATFORM_WALLET_ENC_MNEMONIC),
clear your terminal scrollback, and never commit the output file.
