package cli

import (
	"fmt"
	"os"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/identity"
	"github.com/spf13/cobra"
)

var revokeCmd = &cobra.Command{
	Use:   "revoke",
	Short: "Clear local credentials",
	Long: `Removes locally stored credentials.

Note: This only removes the local credentials file.
To fully revoke the MCP server, an admin should call the /mcp/revoke API.

After running this command, you will need a new bootstrap token to re-register.`,
	RunE: runRevoke,
}

func init() {
	rootCmd.AddCommand(revokeCmd)

	revokeCmd.Flags().Bool("confirm", false,
		"Confirm credential deletion without prompting")
}

func runRevoke(cmd *cobra.Command, args []string) error {
	log := getLogger()
	config := getConfig()

	confirm, _ := cmd.Flags().GetBool("confirm")

	// Initialize identity manager
	identityMgr := identity.NewManager(config, log)

	// Check for existing credentials
	if !identityMgr.HasCredentials() {
		fmt.Println("No credentials found to revoke.")
		return nil
	}

	mcpID := identityMgr.GetMCPID()
	fmt.Printf("MCP Server ID: %s\n", mcpID)
	fmt.Printf("Credentials Path: %s\n", config.CredentialsPath)

	if !confirm {
		fmt.Print("\nAre you sure you want to delete local credentials? [y/N]: ")
		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if err := identityMgr.ClearCredentials(); err != nil {
		return fmt.Errorf("failed to clear credentials: %w", err)
	}

	fmt.Println("\nLocal credentials deleted.")
	fmt.Println("Note: To fully revoke the MCP server, an admin should call the /mcp/revoke API.")

	os.Exit(0)
	return nil
}
