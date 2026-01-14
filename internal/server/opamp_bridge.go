package server

import (
	"context"
	"crypto/tls"
	"log"
	"os"
	"sync"
	"time"

	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"

	"local.dev/opamp-poc-supervisor/api/controlpb"
)

type OpAMPBridge interface {
	Start(ctx context.Context) error
	SendStatus(nodeID string, ev *controlpb.Event) error
	PushCommand(nodeID string, cmd *controlpb.Command) error
	Stop(ctx context.Context) error
}

type RealOpAMPBridge struct {
	serverURL     string
	enqueue       func(nodeID string, cmd *controlpb.Command) error
	getDeviceList func() []string
	mu            sync.RWMutex
	client        client.OpAMPClient
	logger        types.Logger
	instanceID    string
}

func NewRealOpAMPBridge(serverURL string, enqueue func(nodeID string, cmd *controlpb.Command) error, getDeviceList func() []string) *RealOpAMPBridge {
	return &RealOpAMPBridge{
		serverURL:     serverURL,
		enqueue:       enqueue,
		getDeviceList: getDeviceList,
		instanceID:    "supervisor-001",
		logger:        &simpleLogger{},
	}
}

type simpleLogger struct{}

func (l *simpleLogger) Debugf(ctx context.Context, format string, v ...interface{}) {
	log.Printf("[OpAMP DEBUG] "+format, v...)
}

func (l *simpleLogger) Errorf(ctx context.Context, format string, v ...interface{}) {
	log.Printf("[OpAMP ERROR] "+format, v...)
}

func (b *RealOpAMPBridge) Start(ctx context.Context) error {
	log.Println("[OpAMP DEBUG] Start function called")
	insecureTLSEnv := os.Getenv("OPAMP_INSECURE_TLS")
	log.Printf("[OpAMP DEBUG] OPAMP_INSECURE_TLS = '%s'", insecureTLSEnv)

	settings := types.StartSettings{
		OpAMPServerURL: b.serverURL,
		InstanceUid:    types.InstanceUid{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		Callbacks: types.Callbacks{
			OnConnect: func(ctx context.Context) {
				log.Println("[OpAMP] Connected to server")
			},
			OnConnectFailed: func(ctx context.Context, err error) {
				log.Printf("[OpAMP] Connection failed: %v", err)
			},
			OnError: func(ctx context.Context, err *protobufs.ServerErrorResponse) {
				log.Printf("[OpAMP] Server error: %v", err.ErrorMessage)
			},
			OnMessage: b.onMessage,
			OnOpampConnectionSettings: func(ctx context.Context, settings *protobufs.OpAMPConnectionSettings) error {
				log.Printf("[OpAMP] Connection settings updated")
				return nil
			},
		},
		Capabilities: protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus,
	}

	// Configure TLS if using wss://
	insecureTLS := os.Getenv("OPAMP_INSECURE_TLS")
	log.Printf("[OpAMP DEBUG] OPAMP_INSECURE_TLS env var = '%s'", insecureTLS)
	if insecureTLS == "true" {
		settings.TLSConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
		log.Println("[OpAMP] Using insecure TLS (skipping certificate verification)")
	} else {
		log.Println("[OpAMP] NOT using insecure TLS")
	}

	opampClient := client.NewWebSocket(b.logger)

	if err := b.updateAgentDescription(opampClient); err != nil {
		return err
	}

	if err := opampClient.Start(ctx, settings); err != nil {
		return err
	}

	b.mu.Lock()
	b.client = opampClient
	b.mu.Unlock()

	log.Printf("[OpAMP] Bridge started, connecting to %s", b.serverURL)

	go b.periodicHealthReport(ctx)
	go b.periodicDeviceSync(ctx)

	return nil
}

func (b *RealOpAMPBridge) updateAgentDescription(opampClient client.OpAMPClient) error {
	devices := b.getDeviceList()

	// Create identifying attributes
	identifyingAttrs := []*protobufs.KeyValue{
		{
			Key: "service.name",
			Value: &protobufs.AnyValue{
				Value: &protobufs.AnyValue_StringValue{StringValue: "supervisor"},
			},
		},
		{
			Key: "service.instance.id",
			Value: &protobufs.AnyValue{
				Value: &protobufs.AnyValue_StringValue{StringValue: b.instanceID},
			},
		},
	}

	// Create non-identifying attributes including device list
	nonIdentifyingAttrs := []*protobufs.KeyValue{
		{
			Key: "service.version",
			Value: &protobufs.AnyValue{
				Value: &protobufs.AnyValue_StringValue{StringValue: "1.0.0"},
			},
		},
		{
			Key: "device.count",
			Value: &protobufs.AnyValue{
				Value: &protobufs.AnyValue_IntValue{IntValue: int64(len(devices))},
			},
		},
	}

	// Add each device as a separate attribute
	for i, deviceID := range devices {
		nonIdentifyingAttrs = append(nonIdentifyingAttrs, &protobufs.KeyValue{
			Key: "device." + string(rune(i+'0')),
			Value: &protobufs.AnyValue{
				Value: &protobufs.AnyValue_StringValue{StringValue: deviceID},
			},
		})
	}

	log.Printf("[OpAMP] Updating agent description with %d devices: %v", len(devices), devices)

	return opampClient.SetAgentDescription(&protobufs.AgentDescription{
		IdentifyingAttributes:    identifyingAttrs,
		NonIdentifyingAttributes: nonIdentifyingAttrs,
	})
}

func (b *RealOpAMPBridge) periodicDeviceSync(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.mu.RLock()
			client := b.client
			b.mu.RUnlock()

			if client != nil {
				devices := b.getDeviceList()
				log.Printf("[OpAMP] Periodic device sync: found %d devices: %v", len(devices), devices)
				if err := b.updateAgentDescription(client); err != nil {
					log.Printf("[OpAMP] Failed to update device list: %v", err)
				}
			}
		}
	}
}

func (b *RealOpAMPBridge) onMessage(ctx context.Context, msg *types.MessageData) {
	if msg.RemoteConfig != nil {
		log.Printf("[OpAMP] Received remote config: %d entries", len(msg.RemoteConfig.Config.ConfigMap))

		for key, cfg := range msg.RemoteConfig.Config.ConfigMap {
			log.Printf("[OpAMP] Processing config key=%s, body size=%d", key, len(cfg.Body))

			targetDevice := key

			cmd := &controlpb.Command{
				Type:          "UpdateConfig",
				Payload:       string(cfg.Body),
				CorrelationId: time.Now().Format(time.RFC3339Nano),
			}

			log.Printf("[OpAMP] Forwarding config to device: %s", targetDevice)
			if err := b.enqueue(targetDevice, cmd); err != nil {
				log.Printf("[OpAMP] Failed to enqueue config to %s: %v", targetDevice, err)
			} else {
				log.Printf("[OpAMP] Successfully queued config for %s", targetDevice)
			}
		}
	}

	if msg.AgentIdentification != nil {
		log.Printf("[OpAMP] Agent identification received: %s", msg.AgentIdentification.NewInstanceUid)
	}

	if msg.CustomMessage != nil {
		log.Printf("[OpAMP] Custom message received: capability=%s, type=%s",
			msg.CustomMessage.Capability, msg.CustomMessage.Type)
	}
}

func (b *RealOpAMPBridge) periodicHealthReport(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.mu.RLock()
			client := b.client
			b.mu.RUnlock()

			if client != nil {
				health := &protobufs.ComponentHealth{
					Healthy:           true,
					StartTimeUnixNano: uint64(time.Now().Add(-5 * time.Minute).UnixNano()),
					LastError:         "",
				}

				if err := client.SetHealth(health); err != nil {
					log.Printf("[OpAMP] Failed to set health: %v", err)
				}
			}
		}
	}
}

func (b *RealOpAMPBridge) SendStatus(nodeID string, ev *controlpb.Event) error {
	log.Printf("[OpAMP] Forwarding status from %s: type=%s", nodeID, ev.GetType())

	b.mu.RLock()
	client := b.client
	b.mu.RUnlock()

	if client == nil {
		return nil
	}

	log.Printf("[OpAMP] Status forwarded to server (node=%s, type=%s)", nodeID, ev.GetType())

	return nil
}

func (b *RealOpAMPBridge) PushCommand(nodeID string, cmd *controlpb.Command) error {
	return b.enqueue(nodeID, cmd)
}

func (b *RealOpAMPBridge) Stop(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.client != nil {
		return b.client.Stop(ctx)
	}
	return nil
}

type InMemoryBridge struct {
	enqueue func(nodeID string, cmd *controlpb.Command) error
}

func NewInMemoryBridge(enqueue func(nodeID string, cmd *controlpb.Command) error) *InMemoryBridge {
	return &InMemoryBridge{enqueue: enqueue}
}

func (b *InMemoryBridge) Start(ctx context.Context) error {
	// Simulate periodic commands to an example node "edge-1".
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cmd := &controlpb.Command{
					Type:          "FetchStatus",
					Payload:       `{"interval":"30s"}`,
					CorrelationId: time.Now().Format(time.RFC3339Nano),
				}
				if err := b.enqueue("edge-1", cmd); err != nil {
					log.Printf("bridge enqueue error: %v", err)
				}
			}
		}
	}()
	return nil
}

func (b *InMemoryBridge) SendStatus(nodeID string, ev *controlpb.Event) error {
	log.Printf("[OpAMP stub] status from %s: %s payload=%s", nodeID, ev.GetType(), ev.GetPayload())
	return nil
}

func (b *InMemoryBridge) PushCommand(nodeID string, cmd *controlpb.Command) error {
	return b.enqueue(nodeID, cmd)
}

func (b *InMemoryBridge) Stop(ctx context.Context) error {
	log.Printf("[InMemoryBridge] Stopping bridge")
	return nil
}
