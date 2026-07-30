package demolib

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// LoadDotEnv reads KEY=VALUE lines from path into the process environment,
// skipping blank lines and lines starting with '#'. Existing env vars are
// NOT overridden, matching the usual "real env wins over .env file"
// convention. Missing file is not an error (env vars may be set some other
// way, e.g. in CI).
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		os.Setenv(key, value)
	}
	return scanner.Err()
}

// Config is the single place every x402-demo process reads its network
// switch, wallets, and pricing from. Flip Network (via the NETWORK env var)
// and everything else — CAIP-2 network id, USDC asset id, explorer URL
// prefix — follows automatically. Nothing else needs to change for the
// testnet -> mainnet cutover.
type Config struct {
	Network AlgorandNetwork

	CAIP2Network string
	USDCAssetID  uint64

	FacilitatorURL string
	FeePayer       string

	AlgodURL   string
	AlgodToken string

	EncryptionKey string // for wallet.Service — decrypts the *_ENC_MNEMONIC env vars below

	MerchantPort            int
	OrchestratorPort        int
	PublicMerchantURL       string
	PublicOrchestratorURL   string
	MerchantPriceUSDCMicros uint64
	// OrchestratorPriceUSDCMicros is what the orchestrator charges the end
	// client. Defaults to MerchantPriceUSDCMicros (a break-even pass-through
	// demo) — see LoadConfig.
	OrchestratorPriceUSDCMicros uint64

	ClientAddress          string
	ClientEncMnemonic      string
	OrchWallet2Address     string
	OrchWallet2EncMnemonic string
	MerchantWallet3Address string
}

type AlgorandNetwork string

const (
	NetworkTestnet AlgorandNetwork = "testnet"
	NetworkMainnet AlgorandNetwork = "mainnet"
)

// Shared x402-global-challenge fee payer. Confirmed live via facilitator.
// goplausible.xyz's own /supported response on 2026-07-30 (algorand:* ->
// this exact address) and independently via Prism's real 402 challenge —
// same constant AgentMesh's production backend already hardcodes.
const defaultFeePayer = "ZMFK2OI7ZBD2U27ISERZC4S6LKM6WMFJPZQ4MYNJDZ2VNBNMBA67RA22AA"

// CAIP-2 network identifiers, fixed protocol-level constants (not a
// deployer choice) — confirmed live via the facilitator's /supported list.
const (
	caip2Testnet = "algorand:SGO1GKSzyE7IEPItTxCByw9x8FmnrCDexi9/cOUJOiI="
	caip2Mainnet = "algorand:wGHE2Pwdvd7S12BL5FaOP20EGYesN73ktiC1qzkkit8="
)

const (
	usdcAssetTestnet uint64 = 10458941
	usdcAssetMainnet uint64 = 31566704
)

func LoadConfig() (Config, error) {
	network := AlgorandNetwork(envOr("NETWORK", "testnet"))
	if network != NetworkTestnet && network != NetworkMainnet {
		return Config{}, fmt.Errorf(`NETWORK must be "testnet" or "mainnet", got %q`, network)
	}

	caip2 := caip2Testnet
	usdcAsset := usdcAssetTestnet
	defaultAlgodURL := "https://testnet-api.algonode.cloud"
	if network == NetworkMainnet {
		caip2 = caip2Mainnet
		usdcAsset = usdcAssetMainnet
		defaultAlgodURL = "https://mainnet-api.algonode.cloud"
	}

	merchantPort, err := envIntOr("MERCHANT_PORT", 4021)
	if err != nil {
		return Config{}, err
	}
	orchPort, err := envIntOr("ORCHESTRATOR_PORT", 4020)
	if err != nil {
		return Config{}, err
	}
	merchantPrice, err := envUint64Or("MERCHANT_PRICE_USDC_MICROS", 50_000)
	if err != nil {
		return Config{}, err
	}
	orchPrice, err := envUint64Or("ORCHESTRATOR_PRICE_USDC_MICROS", merchantPrice)
	if err != nil {
		return Config{}, err
	}

	encKey := os.Getenv("ENCRYPTION_KEY")
	if encKey == "" {
		return Config{}, fmt.Errorf("ENCRYPTION_KEY is required (generate one and paste it into x402-demo/.env — see README)")
	}

	cfg := Config{
		Network:      network,
		CAIP2Network: caip2,
		USDCAssetID:  usdcAsset,

		FacilitatorURL: envOr("FACILITATOR_URL", "https://facilitator.goplausible.xyz"),
		FeePayer:       envOr("FEE_PAYER", defaultFeePayer),

		AlgodURL:   envOr("ALGOD_URL", defaultAlgodURL),
		AlgodToken: os.Getenv("ALGOD_TOKEN"),

		EncryptionKey: encKey,

		MerchantPort:                merchantPort,
		OrchestratorPort:            orchPort,
		PublicMerchantURL:           envOr("PUBLIC_MERCHANT_URL", fmt.Sprintf("http://localhost:%d", merchantPort)),
		PublicOrchestratorURL:       envOr("PUBLIC_ORCHESTRATOR_URL", fmt.Sprintf("http://localhost:%d", orchPort)),
		MerchantPriceUSDCMicros:     merchantPrice,
		OrchestratorPriceUSDCMicros: orchPrice,

		ClientAddress:          os.Getenv("CLIENT_ADDRESS"),
		ClientEncMnemonic:      os.Getenv("CLIENT_ENC_MNEMONIC"),
		OrchWallet2Address:     os.Getenv("ORCH_WALLET2_ADDRESS"),
		OrchWallet2EncMnemonic: os.Getenv("ORCH_WALLET2_ENC_MNEMONIC"),
		MerchantWallet3Address: os.Getenv("MERCHANT_WALLET3_ADDRESS"),
	}
	return cfg, nil
}

// ExplorerURL builds a Lora explorer link for a settled transaction,
// pointed at the right network.
func (c Config) ExplorerURL(txID string) string {
	return fmt.Sprintf("https://lora.algokit.io/%s/transaction/%s", c.Network, txID)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envUint64Or(key string, fallback uint64) (uint64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}
