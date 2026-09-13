package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/identity"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Manually register with AgentMaurice",
	Long: `Performs manual registration with AgentMaurice using a bootstrap token.

This command will:
1. Use the provided bootstrap token to register
2. Store the received credentials locally
3. Exit (does not start the health checker)

Use 'mcp-sidecar start' for normal operation after registration.`,
	RunE: runRegister,
}

func init() {
	rootCmd.AddCommand(registerCmd)

	// Register-specific flags (same as start for consistency)
	registerCmd.Flags().String("public-key", "",
		"Public key for mTLS (optional)")
	registerCmd.Flags().String("mcp-type", "custom",
		"MCP server type")
	registerCmd.Flags().StringToString("metadata", nil,
		"Additional metadata (key=value pairs)")
	registerCmd.Flags().Bool("force", false,
		"Force re-registration even if credentials exist")

	viper.BindPFlag("public_key", registerCmd.Flags().Lookup("public-key"))
	viper.BindPFlag("mcp_type", registerCmd.Flags().Lookup("mcp-type"))
}

func runRegister(cmd *cobra.Command, args []string) error {
	log := getLogger()
	config := getConfig()

	// Parse metadata from flag
	if metadata, _ := cmd.Flags().GetStringToString("metadata"); len(metadata) > 0 {
		config.Metadata = metadata
	}

	force, _ := cmd.Flags().GetBool("force")

	// Validate configuration
	if config.MauriceURL == "" {
		return fmt.Errorf("--maurice-url is required")
	}
	if config.BootstrapToken == "" {
		return fmt.Errorf("--bootstrap-token is required for registration")
	}

	// Initialize identity manager
	identityMgr := identity.NewManager(config, log)

	// Check for existing credentials
	if identityMgr.HasCredentials() && !force {
		fmt.Println("Credentials already exist.")
		fmt.Printf("MCP Server ID: %s\n", identityMgr.GetMCPID())
		fmt.Println("\nUse --force to re-register (this will invalidate existing credentials)")
		return nil
	}

	if force && identityMgr.HasCredentials() {
		log.Warn("Force flag set, clearing existing credentials")
		if err := identityMgr.ClearCredentials(); err != nil {
			log.Warn("Failed to clear credentials", zap.Error(err))
		}
	}

	fmt.Println("Registering with AgentMaurice...")
	fmt.Printf("  URL: %s\n", config.MauriceURL)
	fmt.Printf("  MCP Type: %s\n", config.MCPType)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := identityMgr.Register(ctx); err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}

	fmt.Println("\nRegistration successful!")
	fmt.Printf("MCP Server ID: %s\n", identityMgr.GetMCPID())
	fmt.Printf("Credentials saved to: %s\n", config.CredentialsPath)
	fmt.Println("\nYou can now run 'mcp-sidecar start' to begin credential management.")

	return nil
}
