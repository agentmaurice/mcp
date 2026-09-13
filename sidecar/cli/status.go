package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/identity"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check the status of stored credentials",
	Long: `Checks if credentials exist and validates them with the server.

Returns:
  - Whether credentials are stored locally
  - Whether credentials are valid
  - Whether renewal is required
  - MCP server ID`,
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	log := getLogger()
	config := getConfig()

	fmt.Println("MCP Sidecar Status")
	fmt.Println("==================")
	fmt.Printf("Credentials Path: %s\n", config.CredentialsPath)

	// Initialize identity manager
	identityMgr := identity.NewManager(config, log)

	// Check for existing credentials
	if !identityMgr.HasCredentials() {
		fmt.Println("\nStatus: NO CREDENTIALS")
		fmt.Println("No credentials found. Run 'mcp-sidecar start' with a bootstrap token to register.")
		return nil
	}

	fmt.Printf("\nMCP Server ID: %s\n", identityMgr.GetMCPID())
	fmt.Println("Credentials: FOUND")

	// Validate with server if URL is configured
	if config.MauriceURL == "" {
		fmt.Println("\nCannot validate: maurice-url not configured")
		return nil
	}

	fmt.Printf("\nValidating with %s...\n", config.MauriceURL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := identityMgr.Validate(ctx)
	if err != nil {
		log.Error("Validation failed", zap.Error(err))
		fmt.Printf("Validation Error: %v\n", err)
		return nil
	}

	if resp.Valid {
		fmt.Println("Credentials: VALID")
		if resp.RenewRequired {
			fmt.Println("Renewal: REQUIRED (credentials expiring soon)")
		} else {
			fmt.Println("Renewal: Not required")
		}
	} else {
		fmt.Println("Credentials: INVALID")
		fmt.Println("The stored credentials are no longer valid.")
		fmt.Println("You may need to re-register with a new bootstrap token.")
	}

	return nil
}
