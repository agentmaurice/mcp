package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/bootstrap"
	"github.com/agentmaurice/mcpchatui/mcp/sidecar/healthcheck"
	"github.com/agentmaurice/mcpchatui/mcp/sidecar/identity"
	sidecarmqtt "github.com/agentmaurice/mcpchatui/mcp/sidecar/mqtt"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the sidecar service",
	Long: `Starts the MCP sidecar service which will:
1. Check for existing credentials
2. Register with AgentMaurice if no credentials exist
3. Periodically validate credentials
4. Auto-renew when required

The service will run until interrupted (SIGINT/SIGTERM).`,
	RunE: runStart,
}

func init() {
	rootCmd.AddCommand(startCmd)

	// Start-specific flags
	startCmd.Flags().String("public-key", "",
		"Public key for mTLS (optional, env: MCP_SIDECAR_PUBLIC_KEY)")
	startCmd.Flags().String("mcp-type", "custom",
		"MCP server type (env: MCP_SIDECAR_MCP_TYPE)")
	startCmd.Flags().Duration("check-interval", 5*time.Minute,
		"Credential validation interval (env: MCP_SIDECAR_CHECK_INTERVAL)")
	startCmd.Flags().StringToString("metadata", nil,
		"Additional metadata (key=value pairs)")

	// Bind start flags to viper
	viper.BindPFlag("public_key", startCmd.Flags().Lookup("public-key"))
	viper.BindPFlag("mcp_type", startCmd.Flags().Lookup("mcp-type"))
	viper.BindPFlag("check_interval", startCmd.Flags().Lookup("check-interval"))
}

func runStart(cmd *cobra.Command, args []string) error {
	log := getLogger()
	config := getConfig()

	// Parse metadata from flag
	if metadata, _ := cmd.Flags().GetStringToString("metadata"); len(metadata) > 0 {
		if config.Metadata == nil {
			config.Metadata = map[string]string{}
		}
		for k, v := range metadata {
			config.Metadata[k] = v
		}
	}

	// Validate configuration
	if err := config.Validate(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runtimeSpec, err := bootstrap.ParseRuntimeSpec(config.RuntimeSpec)
	if err != nil {
		return err
	}
	if runtimeSpec != nil {
		runner := bootstrap.NewRunner(config, log)
		bootstrapResult, err := runner.Run(ctx, runtimeSpec)
		if err != nil {
			return fmt.Errorf("runtime bootstrap failed: %w", err)
		}
		log.Info("Runtime bootstrap ready",
			zap.String("runtime_kind", bootstrapResult.RuntimeKind),
			zap.String("workspace", bootstrapResult.WorkspaceDir),
			zap.String("resolved_commit_sha", bootstrapResult.ResolvedCommitSHA),
			zap.Duration("duration", bootstrapResult.Duration),
		)
	}

	log.Info("Starting MCP Sidecar",
		zap.String("maurice_url", config.MauriceURL),
		zap.String("mcp_type", config.MCPType),
		zap.String("credentials_path", config.CredentialsPath),
	)

	// Initialize identity manager
	identityMgr := identity.NewManager(config, log)

	// Check for existing credentials or register
	if !identityMgr.HasCredentials() {
		if config.BootstrapToken == "" {
			return fmt.Errorf("no credentials found and no bootstrap token provided; use --bootstrap-token")
		}

		log.Info("No existing credentials, performing registration...")
		if err := identityMgr.Register(ctx); err != nil {
			return fmt.Errorf("registration failed: %w", err)
		}
		log.Info("Registration successful",
			zap.String("mcp_id", identityMgr.GetMCPID()),
		)
	} else {
		log.Info("Loaded existing credentials",
			zap.String("mcp_id", identityMgr.GetMCPID()),
		)
	}

	// Initialize and start health checker
	checker := healthcheck.NewChecker(identityMgr, config.CheckInterval, log)

	// Set up unhealthy callback
	checker.SetOnUnhealthy(func() {
		log.Warn("Credentials became invalid")
		// In a real deployment, you might want to:
		// - Attempt re-registration
		// - Notify an external monitoring system
		// - Gracefully shutdown the MCP server
	})

	// Start health checker in background
	go checker.Start(ctx)

	// Optional: start MQTT runtime bridge when mqtt_broker is configured.
	var mqttRuntime *sidecarmqtt.Runtime
	if config.MQTTBroker != "" {
		mqttRuntime = sidecarmqtt.NewRuntime(config, identityMgr, log)
		if err := mqttRuntime.Start(ctx); err != nil {
			return fmt.Errorf("failed to start mqtt runtime: %w", err)
		}
		fields := []zap.Field{
			zap.String("deployment_id", config.DeploymentID),
			zap.String("mqtt_broker", config.MQTTBroker),
			zap.String("local_mcp_command", config.LocalMCPCommand),
			zap.Strings("local_mcp_args", config.LocalMCPArgs),
		}
		log.Info("MQTT runtime started", fields...)
	}

	// Wait for shutdown signal
	log.Info("MCP Sidecar running. Press Ctrl+C to stop.")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigChan:
		log.Info("Received signal, shutting down...",
			zap.String("signal", sig.String()),
		)
	case <-ctx.Done():
		log.Info("Context cancelled, shutting down...")
	}

	// Cancel context to stop health checker
	cancel()

	if mqttRuntime != nil {
		_ = mqttRuntime.Stop()
	}

	// Give some time for graceful shutdown
	time.Sleep(500 * time.Millisecond)

	log.Info("MCP Sidecar stopped")
	return nil
}
