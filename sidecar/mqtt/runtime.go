package mqtt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/config"
	"github.com/agentmaurice/mcpchatui/mcp/sidecar/identity"
	mqttlib "github.com/eclipse/paho.mqtt.golang"
	"github.com/klauspost/compress/zstd"
	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

const (
	compressionMagicPrefix = "ZST\x01"
	runtimeVersion         = "0.2.0"
	protocolVersion        = 1
)

var (
	zstdEncoderPool = sync.Pool{
		New: func() interface{} {
			encoder, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
			return encoder
		},
	}
	zstdDecoderPool = sync.Pool{
		New: func() interface{} {
			decoder, _ := zstd.NewReader(nil)
			return decoder
		},
	}
)

type Runtime struct {
	cfg         *config.SidecarConfig
	identityMgr *identity.Manager
	logger      *zap.Logger

	client mqttlib.Client
	stdio  mcpclient.MCPClient

	clientID      string
	serverID      string
	tenantID      string
	currentAPIKey string
	startedAt     time.Time

	mu sync.RWMutex
}

type commandEnvelope struct {
	V       int             `json:"v"`
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Timeout int             `json:"timeout_ms,omitempty"`
	TS      int64           `json:"ts,omitempty"`
}

type eventEnvelope struct {
	V      int         `json:"v"`
	ID     string      `json:"id"`
	CmdID  string      `json:"cmd_id,omitempty"`
	Type   string      `json:"type"`
	Status string      `json:"status,omitempty"`
	Result interface{} `json:"result,omitempty"`
	Error  *eventError `json:"error,omitempty"`
	Meta   eventMeta   `json:"meta"`
	TS     int64       `json:"ts"`
}

type eventMeta struct {
	MCPID    string `json:"mcp_id"`
	TenantID string `json:"tenant_id"`
	Version  string `json:"version"`
}

type eventError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewRuntime(cfg *config.SidecarConfig, identityMgr *identity.Manager, logger *zap.Logger) *Runtime {
	return &Runtime{
		cfg:         cfg,
		identityMgr: identityMgr,
		logger:      logger.Named("mqtt_sidecar"),
	}
}

func (r *Runtime) Enabled() bool {
	return strings.TrimSpace(r.cfg.MQTTBroker) != ""
}

func (r *Runtime) Start(ctx context.Context) error {
	if !r.Enabled() {
		return nil
	}

	if err := r.cfg.Validate(); err != nil {
		return err
	}

	apiKey := strings.TrimSpace(r.identityMgr.GetAPIKey())
	mcpID := strings.TrimSpace(r.identityMgr.GetMCPID())
	if apiKey == "" || mcpID == "" {
		return fmt.Errorf("cannot start MQTT runtime without sidecar credentials")
	}

	tenantID := strings.TrimSpace(r.cfg.DeploymentID)
	if tenantID == "" {
		tenantID = strings.TrimSpace(r.identityMgr.GetTenantID())
	}
	if tenantID == "" {
		return fmt.Errorf("tenant/deployment id is required for sidecar mqtt runtime")
	}

	clientID := strings.TrimSpace(r.cfg.MQTTClientID)
	if clientID == "" {
		clientID = fmt.Sprintf("sidecar-%s", mcpID)
	}

	r.mu.Lock()
	r.serverID = mcpID
	r.tenantID = tenantID
	r.clientID = clientID
	r.currentAPIKey = apiKey
	r.startedAt = time.Now()
	r.mu.Unlock()

	if err := r.initLocalBackend(); err != nil {
		return err
	}

	if err := r.connectMQTT(apiKey); err != nil {
		_ = r.closeLocalBackend()
		return err
	}

	r.logger.Info("MQTT sidecar runtime started",
		zap.String("broker", r.cfg.MQTTBroker),
		zap.String("tenant_id", tenantID),
		zap.String("server_id", mcpID),
		zap.String("client_id", clientID))

	go r.heartbeatLoop(ctx)
	go r.watchCredentialRotation(ctx)
	go func() {
		<-ctx.Done()
		_ = r.publishStatus("shutting_down")
		_ = r.Stop()
	}()

	return nil
}

func (r *Runtime) Stop() error {
	r.mu.Lock()
	client := r.client
	r.client = nil
	r.mu.Unlock()
	if client != nil && client.IsConnectionOpen() {
		client.Disconnect(1000)
	}
	return r.closeLocalBackend()
}

func (r *Runtime) onConnect(client mqttlib.Client) {
	r.mu.Lock()
	r.client = client
	r.mu.Unlock()

	r.logger.Info("MQTT sidecar connected")
	r.subscribe(r.cmdTopic(), r.handleCommand)

	if err := r.publishHello(); err != nil {
		r.logger.Error("failed to publish hello", zap.Error(err))
	}
	if err := r.publishStatus("healthy"); err != nil {
		r.logger.Error("failed to publish healthy status", zap.Error(err))
	}
}

func (r *Runtime) watchCredentialRotation(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := strings.TrimSpace(r.identityMgr.GetAPIKey())
			if current == "" {
				continue
			}
			r.mu.RLock()
			old := r.currentAPIKey
			r.mu.RUnlock()
			if current == old {
				continue
			}
			r.logger.Info("detected API key rotation, reconnecting MQTT")
			if err := r.reconnectWithAPIKey(current); err != nil {
				r.logger.Error("mqtt re-auth failed", zap.Error(err))
				continue
			}
			r.mu.Lock()
			r.currentAPIKey = current
			r.mu.Unlock()
		}
	}
}

func (r *Runtime) reconnectWithAPIKey(apiKey string) error {
	newClient, err := r.newConnectedClient(apiKey)
	if err != nil {
		return err
	}

	r.mu.Lock()
	old := r.client
	r.client = newClient
	r.mu.Unlock()

	if old != nil && old.IsConnectionOpen() {
		old.Disconnect(1000)
	}
	return nil
}

func (r *Runtime) heartbeatLoop(ctx context.Context) {
	interval := r.cfg.HeartbeatInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			uptime := int64(time.Since(r.startedAt).Seconds())
			_ = r.publishEvent("", "heartbeat", "healthy", map[string]interface{}{
				"status":  "healthy",
				"version": runtimeVersion,
				"uptime":  uptime,
			}, nil)
		}
	}
}

func (r *Runtime) initLocalBackend() error {
	if err := r.closeLocalBackend(); err != nil {
		r.logger.Warn("failed closing existing stdio backend", zap.Error(err))
	}

	client, err := mcpclient.NewStdioMCPClient(
		r.cfg.LocalMCPCommand,
		r.cfg.LocalMCPEnv,
		r.cfg.LocalMCPArgs...,
	)
	if err != nil {
		return fmt.Errorf("failed to start local stdio mcp client: %w", err)
	}
	r.attachLocalMCPStderrLogger(client)

	timeout := r.cfg.CommandTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	initRequest := mcpgo.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcpgo.Implementation{Name: "mcp_sidecar", Version: runtimeVersion}
	initRequest.Params.Capabilities = mcpgo.ClientCapabilities{}
	if _, err := client.Initialize(ctx, initRequest); err != nil {
		_ = client.Close()
		return fmt.Errorf("failed to initialize local stdio mcp client: %w", err)
	}

	r.mu.Lock()
	r.stdio = client
	r.mu.Unlock()
	return nil
}

func (r *Runtime) attachLocalMCPStderrLogger(client *mcpclient.Client) {
	stderr, ok := mcpclient.GetStderr(client)
	if !ok || stderr == nil {
		r.logger.Debug("local MCP stderr stream unavailable")
		return
	}

	go r.streamLocalMCPStderr(stderr)
}

func (r *Runtime) streamLocalMCPStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		r.logger.Info("local MCP stderr", zap.String("line", line))
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		r.logger.Debug("local MCP stderr stream closed with error", zap.Error(err))
		return
	}
	r.logger.Debug("local MCP stderr stream closed")
}

func (r *Runtime) closeLocalBackend() error {
	r.mu.Lock()
	stdioClient := r.stdio
	r.stdio = nil
	r.mu.Unlock()
	if stdioClient == nil {
		return nil
	}
	if err := stdioClient.Close(); err != nil {
		return fmt.Errorf("close local stdio mcp client: %w", err)
	}
	return nil
}

func (r *Runtime) attemptMCPRestart() error {
	attempts := r.cfg.MCPRestartMax
	if attempts < 0 {
		attempts = 0
	}
	backoff := r.cfg.MCPRestartBackoffInitial
	if backoff <= 0 {
		backoff = time.Second
	}
	maxBackoff := r.cfg.MCPRestartBackoffMax
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}

	var lastErr error
	for i := 0; i <= attempts; i++ {
		if i > 0 {
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
		if err := r.initLocalBackend(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("restart attempts exhausted")
	}
	return lastErr
}

func (r *Runtime) connectMQTT(apiKey string) error {
	client, err := r.newConnectedClient(apiKey)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.client = client
	r.currentAPIKey = apiKey
	r.mu.Unlock()
	return nil
}

func (r *Runtime) newConnectedClient(apiKey string) (mqttlib.Client, error) {
	r.mu.RLock()
	clientID := r.clientID
	mcpID := r.serverID
	r.mu.RUnlock()

	tlsConfig, err := r.cfg.TLSConfig()
	if err != nil {
		return nil, err
	}

	opts := mqttlib.NewClientOptions()
	opts.AddBroker(r.cfg.MQTTBroker)
	opts.SetClientID(clientID)
	opts.SetUsername(fmt.Sprintf("sidecar_%s", mcpID))
	opts.SetPassword(apiKey)
	opts.SetTLSConfig(tlsConfig)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(2 * time.Second)
	opts.SetKeepAlive(60 * time.Second)
	opts.SetPingTimeout(10 * time.Second)
	opts.SetCleanSession(false)
	opts.SetMaxReconnectInterval(30 * time.Second)
	opts.SetOnConnectHandler(r.onConnect)
	opts.SetConnectionLostHandler(func(_ mqttlib.Client, err error) {
		r.logger.Warn("MQTT connection lost", zap.Error(err))
	})
	opts.SetReconnectingHandler(func(_ mqttlib.Client, _ *mqttlib.ClientOptions) {
		r.logger.Info("MQTT reconnecting")
	})

	client := mqttlib.NewClient(opts)
	token := client.Connect()
	if ok := token.WaitTimeout(20 * time.Second); !ok {
		return nil, fmt.Errorf("timeout while connecting to MQTT broker")
	}
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("failed to connect to MQTT broker: %w", err)
	}
	return client, nil
}

func (r *Runtime) subscribe(topic string, handler mqttlib.MessageHandler) {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client == nil {
		return
	}

	token := client.Subscribe(topic, 1, handler)
	token.Wait()
	if err := token.Error(); err != nil {
		r.logger.Error("failed to subscribe", zap.String("topic", topic), zap.Error(err))
		return
	}
	r.logger.Info("subscribed to MQTT topic", zap.String("topic", topic))
}

func (r *Runtime) handleCommand(_ mqttlib.Client, msg mqttlib.Message) {
	if len(msg.Payload()) > r.cfg.MQTTMaxPayloadBytes {
		r.publishErrorEvent("", "payload_too_large", "payload exceeds mqtt_max_payload_bytes")
		return
	}

	cmd, err := r.decodeCommand(msg.Payload())
	if err != nil {
		r.publishErrorEvent("", "invalid_command", err.Error())
		return
	}

	if err := r.validateCommand(cmd); err != nil {
		r.publishErrorEvent(cmd.ID, "invalid_command", err.Error())
		return
	}

	timeout := r.cfg.CommandTimeout
	if cmd.Timeout > 0 {
		t := time.Duration(cmd.Timeout) * time.Millisecond
		if t > 0 {
			timeout = t
		}
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result, err := r.dispatchCommand(ctx, cmd)
	if err != nil {
		r.publishErrorEvent(cmd.ID, "rpc_error", err.Error())
		return
	}
	_ = r.publishEvent(cmd.ID, "rpc_result", "", result, nil)
}

func (r *Runtime) decodeCommand(payload []byte) (*commandEnvelope, error) {
	decompressed, err := decompressPayloadLimited(payload, r.cfg.MQTTMaxDecompressedBytes)
	if err != nil {
		return nil, err
	}

	var cmd commandEnvelope
	if err := json.Unmarshal(decompressed, &cmd); err != nil {
		return nil, fmt.Errorf("invalid JSON payload: %w", err)
	}
	return &cmd, nil
}

func (r *Runtime) validateCommand(cmd *commandEnvelope) error {
	if cmd.V != protocolVersion {
		return fmt.Errorf("unsupported protocol version: %d", cmd.V)
	}
	if strings.TrimSpace(cmd.ID) == "" {
		return fmt.Errorf("missing command id")
	}
	if strings.TrimSpace(cmd.Type) != "rpc" {
		return fmt.Errorf("unsupported command type: %s", cmd.Type)
	}
	switch strings.TrimSpace(cmd.Method) {
	case string(mcpgo.MethodToolsList), string(mcpgo.MethodToolsCall), string(mcpgo.MethodResourcesTemplatesList), string(mcpgo.MethodResourcesRead):
		return nil
	default:
		return fmt.Errorf("unsupported command method: %s", cmd.Method)
	}
}

func (r *Runtime) dispatchCommand(ctx context.Context, cmd *commandEnvelope) (interface{}, error) {
	method := strings.TrimSpace(cmd.Method)

	call := func() (interface{}, error) {
		r.mu.RLock()
		stdioClient := r.stdio
		r.mu.RUnlock()
		if stdioClient == nil {
			return nil, errors.New("local stdio mcp client is not initialized")
		}

		switch method {
		case string(mcpgo.MethodToolsList):
			return stdioClient.ListTools(ctx, mcpgo.ListToolsRequest{})
		case string(mcpgo.MethodToolsCall):
			var params mcpgo.CallToolParams
			if err := decodeRawParams(cmd.Params, &params); err != nil {
				return nil, fmt.Errorf("invalid tools/call params: %w", err)
			}
			return stdioClient.CallTool(ctx, mcpgo.CallToolRequest{Params: params})
		case string(mcpgo.MethodResourcesTemplatesList):
			return stdioClient.ListResourceTemplates(ctx, mcpgo.ListResourceTemplatesRequest{})
		case string(mcpgo.MethodResourcesRead):
			var params mcpgo.ReadResourceParams
			if err := decodeRawParams(cmd.Params, &params); err != nil {
				return nil, fmt.Errorf("invalid resources/read params: %w", err)
			}
			return stdioClient.ReadResource(ctx, mcpgo.ReadResourceRequest{Params: params})
		default:
			return nil, fmt.Errorf("unsupported command method: %s", method)
		}
	}

	result, err := call()
	if err == nil {
		return result, nil
	}
	if !isCrashLikeError(err) {
		return nil, err
	}

	_ = r.publishStatus("crashed")
	if restartErr := r.attemptMCPRestart(); restartErr != nil {
		return nil, fmt.Errorf("mcp crashed and restart failed: %w", restartErr)
	}
	return call()
}

func decodeRawParams(raw json.RawMessage, out interface{}) error {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func isCrashLikeError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "broken pipe") || strings.Contains(text, "eof") || strings.Contains(text, "connection reset")
}

func (r *Runtime) publishHello() error {
	return r.publishEvent("", "hello", "healthy", map[string]interface{}{
		"protocol": protocolVersion,
	}, nil)
}

func (r *Runtime) publishStatus(status string) error {
	return r.publishEvent("", "status", status, nil, nil)
}

func (r *Runtime) publishErrorEvent(cmdID, code, message string) {
	_ = r.publishEvent(cmdID, "rpc_error", "", nil, &eventError{Code: code, Message: message})
}

func (r *Runtime) publishEvent(cmdID, eventType, status string, result interface{}, evtErr *eventError) error {
	r.mu.RLock()
	meta := eventMeta{MCPID: r.serverID, TenantID: r.tenantID, Version: runtimeVersion}
	r.mu.RUnlock()

	payload := eventEnvelope{
		V:      protocolVersion,
		ID:     fmt.Sprintf("evt-%d", time.Now().UnixNano()),
		CmdID:  cmdID,
		Type:   eventType,
		Status: status,
		Result: result,
		Error:  evtErr,
		Meta:   meta,
		TS:     time.Now().Unix(),
	}
	return r.publishJSON(r.evtTopic(), payload)
}

func (r *Runtime) publish(topic string, payload []byte) error {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client == nil {
		return fmt.Errorf("mqtt client is not initialized")
	}

	compressed, err := compressPayload(payload)
	if err != nil {
		return err
	}

	token := client.Publish(topic, 1, false, compressed)
	token.Wait()
	if err := token.Error(); err != nil {
		return err
	}
	return nil
}

func (r *Runtime) publishJSON(topic string, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	return r.publish(topic, body)
}

func (r *Runtime) topicBase() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return fmt.Sprintf("am/%s/%s", r.tenantID, r.serverID)
}

func (r *Runtime) cmdTopic() string {
	return fmt.Sprintf("%s/cmd", r.topicBase())
}

func (r *Runtime) evtTopic() string {
	return fmt.Sprintf("%s/evt", r.topicBase())
}

func compressPayload(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}

	encoder := zstdEncoderPool.Get().(*zstd.Encoder)
	defer zstdEncoderPool.Put(encoder)

	compressed := encoder.EncodeAll(data, make([]byte, 0, len(data)))
	out := make([]byte, 0, len(compressionMagicPrefix)+len(compressed))
	out = append(out, []byte(compressionMagicPrefix)...)
	out = append(out, compressed...)
	return out, nil
}

func decompressPayloadLimited(data []byte, maxSize int) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}
	if len(data) < len(compressionMagicPrefix) ||
		!bytes.Equal(data[:len(compressionMagicPrefix)], []byte(compressionMagicPrefix)) {
		if maxSize > 0 && len(data) > maxSize {
			return nil, fmt.Errorf("payload exceeds decompressed size limit")
		}
		return data, nil
	}

	compressed := data[len(compressionMagicPrefix):]
	decoder := zstdDecoderPool.Get().(*zstd.Decoder)
	defer zstdDecoderPool.Put(decoder)
	decompressed, err := decoder.DecodeAll(compressed, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress zstd data: %w", err)
	}
	if maxSize > 0 && len(decompressed) > maxSize {
		return nil, fmt.Errorf("payload exceeds decompressed size limit")
	}
	return decompressed, nil
}
