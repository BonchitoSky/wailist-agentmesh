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
