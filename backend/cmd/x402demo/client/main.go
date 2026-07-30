// Command client plays the real payer ("the AI agent") in the x402-demo
// loop: it pays the orchestrator, which settles that payment, pays the
// merchant on the caller's behalf, and returns the combined result. Prints
// the full trace including both real settlement tx ids and explorer links.
//
// Run from the backend/ directory: go run ./cmd/x402demo/client
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/agentmesh/backend/cmd/x402demo/demolib"
	"github.com/agentmesh/backend/internal/wallet"
)

func main() {
	envPath := flag.String("env", "cmd/x402demo/.env", "path to the x402-demo .env file")
	flag.Parse()

	if err := demolib.LoadDotEnv(*envPath); err != nil {
		log.Fatalf("load %s: %v", *envPath, err)
	}
	cfg, err := demolib.LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.ClientAddress == "" || cfg.ClientEncMnemonic == "" {
		log.Fatal("CLIENT_ADDRESS and CLIENT_ENC_MNEMONIC are required — see cmd/x402demo/README.md")
	}

	walletSvc := wallet.NewService(cfg.EncryptionKey, cfg.AlgodURL, cfg.AlgodToken, string(cfg.Network))

	orchestratorURL := cfg.PublicOrchestratorURL + "/orchestrate-resume-screen"
	req := demolib.ScreenRequest{
		TaskDescription: "Senior React Frontend Developer with 5+ years of experience",
		Files: []demolib.ResumeFile{
			{
				Filename: "jane_doe_resume.txt",
				Text:     "Jane Doe. Senior Frontend Engineer with 6 years building React, TypeScript, and Next.js applications. Led migration to Redux Toolkit and Tailwind CSS.",
			},
			{
				Filename: "sam_lee_resume.txt",
				Text:     "Sam Lee. Backend engineer specializing in Go, PostgreSQL, and gRPC microservices. Some exposure to Kubernetes and Terraform.",
			},
		},
	}

	fmt.Printf("== x402-demo client ==\n")
	fmt.Printf("network:      %s\n", cfg.Network)
	fmt.Printf("client:       %s\n", cfg.ClientAddress)
	fmt.Printf("orchestrator: %s\n", orchestratorURL)
	fmt.Println()
	fmt.Println("paying orchestrator (this settles a real on-chain USDC payment)...")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := demolib.PayAndFetch(ctx, walletSvc, cfg.ClientEncMnemonic, cfg, http.MethodPost, orchestratorURL, req)
	if err != nil {
		log.Fatalf("demo run failed: %v", err)
	}
	if result.StatusCode >= 300 {
		log.Fatalf("orchestrator returned status %d: %s", result.StatusCode, string(result.Body))
	}

	var pretty map[string]any
	if err := json.Unmarshal(result.Body, &pretty); err != nil {
		fmt.Println(string(result.Body))
		return
	}
	prettyJSON, _ := json.MarshalIndent(pretty, "", "  ")

	fmt.Println()
	fmt.Println("== response ==")
	fmt.Println(string(prettyJSON))
	fmt.Println()
	fmt.Println("== settlements ==")
	fmt.Printf("client -> orchestrator (Wallet 2): tx=%s  %s\n", result.Settlement.Transaction, cfg.ExplorerURL(result.Settlement.Transaction))
	if settlement, ok := pretty["settlement"].(map[string]any); ok {
		if outbound, ok := settlement["outbound"].(map[string]any); ok {
			fmt.Printf("orchestrator (Wallet 2) -> merchant (Wallet 3): tx=%v  %v\n", outbound["txId"], outbound["explorerURL"])
		}
	}
}
