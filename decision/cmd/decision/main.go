package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/decision/internal/mcpserver"
	"github.com/agentmaurice/mcpchatui/mcp/decision/pkg/systemone"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/viper"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	v := viper.New()
	v.SetEnvPrefix("MCP_DECISION")
	v.AutomaticEnv()
	v.SetDefault("transport", "stdio")
	v.SetDefault("http_addr", "127.0.0.1:8080")
	v.SetDefault("typesafe_url", "https://api.typesafe.ai")
	v.SetDefault("hosted_url", "https://llm.agentmaurice.app")
	v.SetDefault("model", "jev-latest")
	v.SetDefault("timeout", "5s")
	v.SetDefault("max_state_bytes", 32768)
	cfg, err := configuration(v)
	if err != nil {
		return err
	}
	c, err := systemone.New(cfg)
	if err != nil {
		return err
	}
	s := mcpserver.New(c, version)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch v.GetString("transport") {
	case "stdio":
		return server.NewStdioServer(s).Listen(ctx, os.Stdin, os.Stdout)
	case "http":
		h := &http.Server{Addr: v.GetString("http_addr"), Handler: mcpserver.Handler(s), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = h.Shutdown(shutdown)
		}()
		if err := h.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errors.New("decision HTTP listener failed")
		}
		return nil
	default:
		return errors.New("MCP_DECISION_TRANSPORT must be stdio or http")
	}
}

func configuration(v *viper.Viper) (systemone.Config, error) {
	key := v.GetString("typesafe_api_key")
	if file := v.GetString("typesafe_api_key_file"); file != "" {
		if key != "" {
			return systemone.Config{}, errors.New("configure either TypeSafe key or key file, not both")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return systemone.Config{}, errors.New("cannot read TypeSafe key file")
		}
		key = strings.TrimSpace(string(data))
	}
	timeout, err := time.ParseDuration(v.GetString("timeout"))
	if err != nil || timeout <= 0 {
		return systemone.Config{}, errors.New("invalid MCP_DECISION_TIMEOUT")
	}
	limit, err := strconv.Atoi(v.GetString("max_state_bytes"))
	if err != nil || limit <= 0 || limit > 32768 {
		return systemone.Config{}, errors.New("invalid MCP_DECISION_MAX_STATE_BYTES")
	}
	cfg := systemone.Config{APIKey: key, Provider: "typesafe", URL: v.GetString("typesafe_url"), Model: v.GetString("model"), Timeout: timeout, MaxStateBytes: limit}
	if key == "" && v.GetString("typesafe_api_key_file") == "" && strings.TrimSpace(v.GetString("hosted_key")) != "" {
		cfg.APIKey = strings.TrimSpace(v.GetString("hosted_key"))
		cfg.Provider = "agentmaurice"
		cfg.URL = v.GetString("hosted_url")
		if cfg.URL == "" {
			cfg.URL = "https://llm.agentmaurice.app"
		}
		cfg.Model = "hosted:" + strings.TrimPrefix(strings.TrimPrefix(cfg.Model, "hosted:"), "agentmaurice:")
		if cfg.Model == "hosted:" {
			cfg.Model = "hosted:jev-latest"
		}
		cfg.InstanceID = strings.TrimSpace(v.GetString("hosted_instance_id"))
	}
	return cfg, nil
}
