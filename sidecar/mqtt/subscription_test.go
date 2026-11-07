package mqtt

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/config"
	"github.com/eclipse/paho.mqtt.golang/packets"
	"go.uber.org/zap"
)

// refusingBroker is a small TCP MQTT broker whose authorization backend is not
// ready: it refuses the SUBSCRIBE of the first `refuse` attempts with the
// failure code of the SUBACK, keeping the connection up and acknowledging
// everything else, the way the One's broker answers while its webhook fails.
type refusingBroker struct {
	ln          net.Listener
	refuse      int32
	subscribes  atomic.Int32
	accepted    atomic.Int32
	events      chan eventEnvelope
	dropFirst   bool
	connections atomic.Int32
	wg          sync.WaitGroup
}

func newRefusingBroker(t *testing.T, refuse int, dropFirst bool) *refusingBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &refusingBroker{ln: ln, refuse: int32(refuse), events: make(chan eventEnvelope, 64), dropFirst: dropFirst}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go b.serve(conn, int(b.connections.Add(1)))
		}
	}()
	return b
}

func (b *refusingBroker) serve(conn net.Conn, index int) {
	defer conn.Close()
	packet, err := packets.ReadPacket(conn)
	if err != nil {
		return
	}
	if _, ok := packet.(*packets.ConnectPacket); !ok {
		return
	}
	_ = (&packets.ConnackPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Connack}, ReturnCode: packets.Accepted}).Write(conn)
	for {
		packet, err = packets.ReadPacket(conn)
		if err != nil {
			return
		}
		switch p := packet.(type) {
		case *packets.SubscribePacket:
			attempt := b.subscribes.Add(1)
			if index == 1 && b.dropFirst && attempt == 2 {
				return // the connection drops during the retries
			}
			code := byte(1)
			if attempt <= b.refuse {
				code = 0x80
			} else {
				b.accepted.Add(1)
			}
			_ = (&packets.SubackPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Suback}, MessageID: p.MessageID, ReturnCodes: []byte{code}}).Write(conn)
			if code == 1 {
				command := &packets.PublishPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Publish, Qos: 1}, TopicName: "am/deployment-fixture/mcp-fixture/cmd", MessageID: 7, Payload: commandPayload("cmd-after-subscribe")}
				_ = command.Write(conn)
			}
		case *packets.PublishPacket:
			if p.FixedHeader.Qos > 0 {
				_ = (&packets.PubackPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Puback}, MessageID: p.MessageID}).Write(conn)
			}
			if event, ok := decodeRuntimeEvent(p.Payload); ok {
				select {
				case b.events <- event:
				default:
				}
			}
		case *packets.PingreqPacket:
			_ = (&packets.PingrespPacket{FixedHeader: packets.FixedHeader{MessageType: packets.Pingresp}}).Write(conn)
		}
	}
}

func startRuntimeOn(t *testing.T, b *refusingBroker) *Runtime {
	t.Helper()
	cfg := config.NewDefaultConfig()
	cfg.MQTTBroker = "tcp://" + b.ln.Addr().String()
	cfg.CommandTimeout = 500 * time.Millisecond
	cfg.MQTTTimeout = 5 * time.Second
	r := NewRuntime(cfg, nil, zap.NewNop())
	r.serverID = "mcp-fixture"
	r.tenantID = "deployment-fixture"
	r.clientID = "sidecar-fixture"
	r.subscribeRetryInitial = 20 * time.Millisecond
	r.subscribeRetryMax = 40 * time.Millisecond
	if err := r.connectMQTT("fixture-api-key"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Stop() })
	return r
}

func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// AGENT-1554: a refused subscription leaves the connection up. The sidecar must
// notice the failure code, say it is degraded, and subscribe again until the
// broker accepts, then receive commands.
func TestRuntimeSubscribesAgainWhenTheBrokerRefusesTheSubscription(t *testing.T) {
	b := newRefusingBroker(t, 3, false)
	r := startRuntimeOn(t, b)

	waitFor(t, "the subscription to be accepted after three refusals", 5*time.Second, func() bool { return b.accepted.Load() == 1 })
	if got := b.subscribes.Load(); got != 4 {
		t.Fatalf("expected 4 SUBSCRIBE packets (3 refused, 1 accepted), got %d", got)
	}
	waitFor(t, "the runtime to record the subscription", time.Second, r.commandSubscribed)

	// The command published after the accepted SUBSCRIBE reaches the handler,
	// which answers with an rpc_error since the fixture has no local backend.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-b.events:
			if event.Type == "rpc_error" && event.CmdID == "cmd-after-subscribe" {
				return
			}
		case <-deadline:
			t.Fatal("the command published after the accepted subscription was not handled")
		}
	}
}

func TestRuntimeIsDegradedWhileTheSubscriptionIsRefused(t *testing.T) {
	b := newRefusingBroker(t, 1<<20, false) // never accepts
	r := startRuntimeOn(t, b)
	waitFor(t, "several refused attempts", 3*time.Second, func() bool { return b.subscribes.Load() >= 3 })
	if r.commandSubscribed() {
		t.Fatal("the runtime must not report a subscription the broker refused")
	}
}

// A reconnection starts its own subscription loop and ends the one of the
// dropped connection: afterwards the broker sees a single accepted SUBSCRIBE.
func TestRuntimeStopsTheRetryLoopOfADroppedConnection(t *testing.T) {
	b := newRefusingBroker(t, 1, true) // refuse once, drop during the retry, accept on the new connection
	r := startRuntimeOn(t, b)

	waitFor(t, "the subscription on the new connection", 8*time.Second, func() bool { return b.accepted.Load() >= 1 })
	waitFor(t, "the runtime to record the subscription", time.Second, r.commandSubscribed)
	before := b.subscribes.Load()
	time.Sleep(300 * time.Millisecond) // several retry periods: a stale loop would subscribe again
	if after := b.subscribes.Load(); after != before {
		t.Fatalf("a stale retry loop subscribed again: %d SUBSCRIBE packets before, %d after", before, after)
	}
	if got := b.accepted.Load(); got != 1 {
		t.Fatalf("expected exactly one accepted subscription, got %d", got)
	}
}
