package mqtt

import (
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/config"
	"github.com/eclipse/paho.mqtt.golang/packets"
	"go.uber.org/zap"
)

// TestRuntimeReconnectsAfterUnacknowledgedRPC exercises Runtime.connectMQTT
// and handleCommand against a small TCP MQTT broker. The first rpc_error is
// deliberately left without PUBACK before the transport is closed; the next
// correlated command must arrive after Paho automatic reconnect.
func TestRuntimeReconnectsAfterUnacknowledgedRPC(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	recovered := make(chan struct{}, 1)
	var mu sync.Mutex
	connections := 0
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			mu.Lock()
			connections++
			index := connections
			mu.Unlock()
			go serveRuntimeBroker(conn, index, recovered)
		}
	}()

	cfg := config.NewDefaultConfig()
	cfg.MQTTBroker = "tcp://" + ln.Addr().String()
	cfg.CommandTimeout = 500 * time.Millisecond
	cfg.MQTTTimeout = 10 * time.Second
	r := NewRuntime(cfg, nil, zap.NewNop())
	r.serverID = "mcp-fixture"
	r.tenantID = "deployment-fixture"
	r.clientID = "sidecar-fixture"
	if err := r.connectMQTT("fixture-api-key"); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()

	select {
	case <-recovered:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not reconnect and deliver the correlated command")
	}
}

func TestRuntimePublishTimesOutWithoutPUBACK(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ready := make(chan struct{})
	go servePublishTimeoutBroker(ln, ready)

	cfg := config.NewDefaultConfig()
	cfg.MQTTBroker = "tcp://" + ln.Addr().String()
	cfg.MQTTTimeout = 200 * time.Millisecond
	r := NewRuntime(cfg, nil, zap.NewNop())
	r.serverID = "mcp-timeout"
	r.tenantID = "deployment-timeout"
	r.clientID = "sidecar-timeout"
	if err := r.connectMQTT("fixture-api-key"); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	select {
	case <-ready:
	case <-time.After(1 * time.Second):
		t.Fatal("broker did not observe the initial hello/status events")
	}

	done := make(chan error, 1)
	go func() { done <- r.publish("am/deployment-timeout/mcp-timeout/evt", []byte("unacknowledged")) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("publish unexpectedly succeeded without PUBACK")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("publish did not honor configured MQTT timeout")
	}
}

func serveRuntimeBroker(conn net.Conn, connectionIndex int, recovered chan<- struct{}) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	packet, err := packets.ReadPacket(conn)
	if err != nil {
		return
	}
	if _, ok := packet.(*packets.ConnectPacket); !ok {
		return
	}
	_ = (&packets.ConnackPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Connack}, SessionPresent: connectionIndex > 1, ReturnCode: packets.Accepted}).Write(conn)
	commandSent := false
	for {
		packet, err = packets.ReadPacket(conn)
		if err != nil {
			return
		}
		switch p := packet.(type) {
		case *packets.SubscribePacket:
			_ = (&packets.SubackPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Suback}, MessageID: p.MessageID, ReturnCodes: []byte{1}}).Write(conn)
			if !commandSent {
				name := "first"
				if connectionIndex > 1 {
					name = "recovered"
				}
				command := &packets.PublishPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Publish, Qos: 1}, TopicName: "am/deployment-fixture/mcp-fixture/cmd", MessageID: uint16(connectionIndex), Payload: commandPayload("cmd-" + name)}
				if err := command.Write(conn); err != nil {
					return
				}
				commandSent = true
			}
		case *packets.PublishPacket:
			if p.TopicName != "am/deployment-fixture/mcp-fixture/evt" {
				continue
			}
			event, ok := decodeRuntimeEvent(p.Payload)
			if !ok || event.Type != "rpc_error" {
				if p.FixedHeader.Qos > 0 {
					_ = (&packets.PubackPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Puback}, MessageID: p.MessageID}).Write(conn)
				}
				continue
			}
			if connectionIndex == 1 {
				return
			}
			if event.CmdID == "cmd-recovered" {
				_ = (&packets.PubackPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Puback}, MessageID: p.MessageID}).Write(conn)
				select {
				case recovered <- struct{}{}:
				default:
				}
				return
			}
		case *packets.PubackPacket:
		case *packets.PingreqPacket:
			_ = (&packets.PingrespPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Pingresp}}).Write(conn)
		}
	}
}

func commandPayload(id string) []byte {
	command := commandEnvelope{V: protocolVersion, ID: id, Type: "rpc", Method: "tools/call", Params: json.RawMessage(`{"name":"health"}`), TS: time.Now().Unix()}
	body, _ := json.Marshal(command)
	compressed, _ := compressPayload(body)
	return compressed
}

func decodeRuntimeEvent(payload []byte) (eventEnvelope, bool) {
	var event eventEnvelope
	body, err := decompressPayloadLimited(payload, 1<<20)
	if err != nil || json.Unmarshal(body, &event) != nil {
		return event, false
	}
	return event, true
}

func servePublishTimeoutBroker(ln net.Listener, ready chan<- struct{}) {
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	packet, err := packets.ReadPacket(conn)
	if err != nil {
		return
	}
	if _, ok := packet.(*packets.ConnectPacket); !ok {
		return
	}
	_ = (&packets.ConnackPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Connack}, ReturnCode: packets.Accepted}).Write(conn)
	ackedEvents := 0
	readySent := false
	for {
		packet, err = packets.ReadPacket(conn)
		if err != nil {
			return
		}
		switch p := packet.(type) {
		case *packets.SubscribePacket:
			_ = (&packets.SubackPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Suback}, MessageID: p.MessageID, ReturnCodes: []byte{1}}).Write(conn)
		case *packets.PublishPacket:
			if p.FixedHeader.Qos == 0 {
				continue
			}
			if ackedEvents < 2 {
				ackedEvents++
				_ = (&packets.PubackPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Puback}, MessageID: p.MessageID}).Write(conn)
				if ackedEvents == 2 && !readySent {
					close(ready)
					readySent = true
				}
			}
		case *packets.PingreqPacket:
			_ = (&packets.PingrespPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Pingresp}}).Write(conn)
		}
	}
}
