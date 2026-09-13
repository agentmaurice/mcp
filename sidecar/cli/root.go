package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	cfgFile string
	cfg     *config.SidecarConfig
	logger  *zap.Logger
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "mcp-sidecar",
	Short: "MCP Sidecar - Identity and registration management for MCP servers",
	Long: `MCP Sidecar is an autonomous service that handles registration, authentication,
and credential lifecycle for MCP servers connecting to AgentMaurice.

It provides:
  - Automatic registration using bootstrap tokens
  - Secure credential storage
  - Periodic credential validation
  - Automatic credential renewal
  - Health monitoring`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Initialize logger
		logLevel := viper.GetString("log_level")
		logEncoding := viper.GetString("log_encoding")

		var err error
		logger, err = initLogger(logLevel, logEncoding)
		if err != nil {
			return fmt.Errorf("init logger: %w", err)
		}

		return nil
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		if logger != nil {
			_ = logger.Sync()
		}
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Persistent flags available to all subcommands
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "",
		"config file (default: ./sidecar.yaml)")
	rootCmd.PersistentFlags().String("maurice-url", "",
		"AgentMaurice API URL (env: MCP_SIDECAR_MAURICE_URL)")
	rootCmd.PersistentFlags().String("bootstrap-token", "",
		"Bootstrap token for initial registration (env: MCP_SIDECAR_BOOTSTRAP_TOKEN)")
	rootCmd.PersistentFlags().String("credentials-path", "/var/mcp/credentials",
		"Path to store credentials (env: MCP_SIDECAR_CREDENTIALS_PATH)")
	rootCmd.PersistentFlags().String("deployment-id", "",
		"Deployment ID for MQTT topics (env: MCP_SIDECAR_DEPLOYMENT_ID)")
	rootCmd.PersistentFlags().String("mqtt-broker", "",
		"MQTT broker URL (env: MCP_SIDECAR_MQTT_BROKER)")
	rootCmd.PersistentFlags().String("mqtt-client-id", "",
		"MQTT client ID override (env: MCP_SIDECAR_MQTT_CLIENT_ID)")
	rootCmd.PersistentFlags().String("mcp-server-id", "",
		"Reserved for future MQTT topic override (currently forced to mcp_id, env: MCP_SIDECAR_MCP_SERVER_ID)")
	rootCmd.PersistentFlags().String("local-mcp-command", "",
		"Local MCP command (STDIO) (env: MCP_SIDECAR_LOCAL_MCP_COMMAND)")
	rootCmd.PersistentFlags().StringSlice("local-mcp-args", nil,
		"Local MCP command arguments (env: MCP_SIDECAR_LOCAL_MCP_ARGS)")
	rootCmd.PersistentFlags().StringSlice("local-mcp-env", nil,
		"Extra environment entries in KEY=VALUE format (env: MCP_SIDECAR_LOCAL_MCP_ENV)")
	rootCmd.PersistentFlags().String("runtime-spec", "",
		"Runtime bootstrap specification as JSON (env: MCP_SIDECAR_RUNTIME_SPEC)")
	rootCmd.PersistentFlags().String("runtime-workdir", "/tmp/mcp-sidecar-workspace",
		"Workspace directory used by runtime bootstrap (env: MCP_SIDECAR_RUNTIME_WORKDIR)")
	rootCmd.PersistentFlags().Duration("bootstrap-timeout", 10*time.Minute,
		"Timeout for runtime bootstrap (env: MCP_SIDECAR_BOOTSTRAP_TIMEOUT)")
	rootCmd.PersistentFlags().String("mqtt-ca-file", "",
		"Path to CA file used to validate MQTT broker TLS cert (env: MCP_SIDECAR_MQTT_CA_FILE)")
	rootCmd.PersistentFlags().String("mqtt-client-cert-file", "",
		"Path to MQTT client certificate for mTLS (env: MCP_SIDECAR_MQTT_CLIENT_CERT_FILE)")
	rootCmd.PersistentFlags().String("mqtt-client-key-file", "",
		"Path to MQTT client key for mTLS (env: MCP_SIDECAR_MQTT_CLIENT_KEY_FILE)")
	rootCmd.PersistentFlags().String("mqtt-server-name", "",
		"Expected MQTT broker TLS server name (env: MCP_SIDECAR_MQTT_SERVER_NAME)")
	rootCmd.PersistentFlags().Bool("mqtt-insecure-skip-verify", false,
		"Skip MQTT TLS certificate verification (env: MCP_SIDECAR_MQTT_INSECURE_SKIP_VERIFY)")
	rootCmd.PersistentFlags().Duration("mqtt-timeout", 2*time.Minute,
		"Timeout for local MCP and MQTT RPC operations (env: MCP_SIDECAR_MQTT_TIMEOUT)")
	rootCmd.PersistentFlags().Int("mqtt-max-payload-bytes", 1<<20,
		"Max MQTT compressed payload size accepted in bytes (env: MCP_SIDECAR_MQTT_MAX_PAYLOAD_BYTES)")
	rootCmd.PersistentFlags().Int("mqtt-max-decompressed-bytes", 4<<20,
		"Max MQTT decompressed payload size accepted in bytes (env: MCP_SIDECAR_MQTT_MAX_DECOMPRESSED_BYTES)")
	rootCmd.PersistentFlags().Duration("command-timeout", 2*time.Minute,
		"Timeout per incoming cmd message processing (env: MCP_SIDECAR_COMMAND_TIMEOUT)")
	rootCmd.PersistentFlags().Duration("heartbeat-interval", 30*time.Second,
		"Heartbeat publication interval (env: MCP_SIDECAR_HEARTBEAT_INTERVAL)")
	rootCmd.PersistentFlags().Int("mcp-restart-max", 5,
		"Maximum number of MCP process restart attempts after crash (env: MCP_SIDECAR_MCP_RESTART_MAX)")
	rootCmd.PersistentFlags().Duration("mcp-restart-backoff-initial", 1*time.Second,
		"Initial restart backoff after MCP crash (env: MCP_SIDECAR_MCP_RESTART_BACKOFF_INITIAL)")
	rootCmd.PersistentFlags().Duration("mcp-restart-backoff-max", 30*time.Second,
		"Maximum restart backoff after MCP crash (env: MCP_SIDECAR_MCP_RESTART_BACKOFF_MAX)")
	rootCmd.PersistentFlags().String("log-level", "info",
		"Log level: debug, info, warn, error (env: MCP_SIDECAR_LOG_LEVEL)")
	rootCmd.PersistentFlags().String("log-encoding", "console",
		"Log encoding: console, json (env: MCP_SIDECAR_LOG_ENCODING)")

	// Bind flags to viper
	viper.BindPFlag("maurice_url", rootCmd.PersistentFlags().Lookup("maurice-url"))
	viper.BindPFlag("bootstrap_token", rootCmd.PersistentFlags().Lookup("bootstrap-token"))
	viper.BindPFlag("credentials_path", rootCmd.PersistentFlags().Lookup("credentials-path"))
	viper.BindPFlag("deployment_id", rootCmd.PersistentFlags().Lookup("deployment-id"))
	viper.BindPFlag("mqtt_broker", rootCmd.PersistentFlags().Lookup("mqtt-broker"))
	viper.BindPFlag("mqtt_client_id", rootCmd.PersistentFlags().Lookup("mqtt-client-id"))
	viper.BindPFlag("mcp_server_id", rootCmd.PersistentFlags().Lookup("mcp-server-id"))
	viper.BindPFlag("local_mcp_command", rootCmd.PersistentFlags().Lookup("local-mcp-command"))
	viper.BindPFlag("local_mcp_args", rootCmd.PersistentFlags().Lookup("local-mcp-args"))
	viper.BindPFlag("local_mcp_env", rootCmd.PersistentFlags().Lookup("local-mcp-env"))
	viper.BindPFlag("runtime_spec", rootCmd.PersistentFlags().Lookup("runtime-spec"))
	viper.BindPFlag("runtime_workdir", rootCmd.PersistentFlags().Lookup("runtime-workdir"))
	viper.BindPFlag("bootstrap_timeout", rootCmd.PersistentFlags().Lookup("bootstrap-timeout"))
	viper.BindPFlag("mqtt_ca_file", rootCmd.PersistentFlags().Lookup("mqtt-ca-file"))
	viper.BindPFlag("mqtt_client_cert_file", rootCmd.PersistentFlags().Lookup("mqtt-client-cert-file"))
	viper.BindPFlag("mqtt_client_key_file", rootCmd.PersistentFlags().Lookup("mqtt-client-key-file"))
	viper.BindPFlag("mqtt_server_name", rootCmd.PersistentFlags().Lookup("mqtt-server-name"))
	viper.BindPFlag("mqtt_insecure_skip_verify", rootCmd.PersistentFlags().Lookup("mqtt-insecure-skip-verify"))
	viper.BindPFlag("mqtt_timeout", rootCmd.PersistentFlags().Lookup("mqtt-timeout"))
	viper.BindPFlag("mqtt_max_payload_bytes", rootCmd.PersistentFlags().Lookup("mqtt-max-payload-bytes"))
	viper.BindPFlag("mqtt_max_decompressed_bytes", rootCmd.PersistentFlags().Lookup("mqtt-max-decompressed-bytes"))
	viper.BindPFlag("command_timeout", rootCmd.PersistentFlags().Lookup("command-timeout"))
	viper.BindPFlag("heartbeat_interval", rootCmd.PersistentFlags().Lookup("heartbeat-interval"))
	viper.BindPFlag("mcp_restart_max", rootCmd.PersistentFlags().Lookup("mcp-restart-max"))
	viper.BindPFlag("mcp_restart_backoff_initial", rootCmd.PersistentFlags().Lookup("mcp-restart-backoff-initial"))
	viper.BindPFlag("mcp_restart_backoff_max", rootCmd.PersistentFlags().Lookup("mcp-restart-backoff-max"))
	viper.BindPFlag("log_level", rootCmd.PersistentFlags().Lookup("log-level"))
	viper.BindPFlag("log_encoding", rootCmd.PersistentFlags().Lookup("log-encoding"))
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	cfg = config.NewDefaultConfig()

	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		// Search for config in various locations
		if home, err := os.UserHomeDir(); err == nil {
			viper.AddConfigPath(filepath.Join(home, ".mcp-sidecar"))
		}
		viper.AddConfigPath(".")
		viper.AddConfigPath("/etc/mcp-sidecar")
		viper.SetConfigName("sidecar")
		viper.SetConfigType("yaml")
	}

	// Environment variables
	viper.SetEnvPrefix("MCP_SIDECAR")
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))

	// Read config file if available
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			fmt.Fprintf(os.Stderr, "Warning: error reading config file: %v\n", err)
		}
	}

	// Unmarshal into config struct
	if err := viper.Unmarshal(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error parsing config: %v\n", err)
	}
	mergeMetadataFromEnv(cfg)
}

// initLogger initializes the zap logger with the specified level and encoding.
func initLogger(level string, encoding string) (*zap.Logger, error) {
	// Parse log level
	var zapLevel zapcore.Level
	switch strings.ToLower(level) {
	case "debug":
		zapLevel = zapcore.DebugLevel
	case "info":
		zapLevel = zapcore.InfoLevel
	case "warn", "warning":
		zapLevel = zapcore.WarnLevel
	case "error":
		zapLevel = zapcore.ErrorLevel
	default:
		zapLevel = zapcore.InfoLevel
	}

	// Create config based on encoding
	var zapConfig zap.Config
	if encoding == "json" {
		zapConfig = zap.NewProductionConfig()
	} else {
		zapConfig = zap.NewDevelopmentConfig()
		zapConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	zapConfig.Level = zap.NewAtomicLevelAt(zapLevel)
	zapConfig.Encoding = encoding

	return zapConfig.Build()
}

// getConfig returns the current configuration.
func getConfig() *config.SidecarConfig {
	return cfg
}

// getLogger returns the current logger.
func getLogger() *zap.Logger {
	return logger
}

func mergeMetadataFromEnv(cfg *config.SidecarConfig) {
	raw := strings.TrimSpace(os.Getenv("MCP_SIDECAR_METADATA"))
	if raw == "" {
		return
	}

	if cfg.Metadata == nil {
		cfg.Metadata = make(map[string]string)
	}

	parsed := map[string]string{}
	if strings.HasPrefix(raw, "{") {
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: invalid MCP_SIDECAR_METADATA JSON: %v\n", err)
			return
		}
	} else {
		entries := strings.Split(raw, ",")
		for _, entry := range entries {
			parts := strings.SplitN(strings.TrimSpace(entry), "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			if key == "" {
				continue
			}
			parsed[key] = strings.TrimSpace(parts[1])
		}
	}

	for k, v := range parsed {
		cfg.Metadata[k] = v
	}
}
