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

	"local.dev/opamp-supervisor/api/controlpb"
)

type OpAMPBridge interface {
	Start(ctx context.Context) error
	SendStatus(nodeID string, ev *controlpb.Event) error
	PushCommand(nodeID string, cmd *controlpb.Command) error
	OnConfigAck(nodeID string, ack *controlpb.ConfigAck) error
	Stop(ctx context.Context) error
}

type RealOpAMPBridge struct {
	serverURL      string
	enqueue        func(nodeID string, cmd *controlpb.Command) error
	enqueueConfig  func(nodeID string, cfg *controlpb.ConfigPush) error
	getDeviceList  func() []string
	getAgentType   func(nodeID string) string
	mu             sync.RWMutex
	client         client.OpAMPClient
	logger         types.Logger
	instanceID     string
	ackStatus      map[string]bool
	ackHash        map[string]string
	deviceConfigs  map[string]string
}

func NewRealOpAMPBridge(serverURL string, enqueue func(nodeID string, cmd *controlpb.Command) error, enqueueConfig func(nodeID string, cfg *controlpb.ConfigPush) error, getDeviceList func() []string, getAgentType func(nodeID string) string) *RealOpAMPBridge {
	return &RealOpAMPBridge{
		serverURL:      serverURL,
		enqueue:        enqueue,
		enqueueConfig:  enqueueConfig,
		getDeviceList:  getDeviceList,
		getAgentType:   getAgentType,
		instanceID:     "supervisor-001",
		logger:         &simpleLogger{},
		ackStatus:      make(map[string]bool),
		ackHash:        make(map[string]string),
		deviceConfigs:  make(map[string]string),
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
	go b.periodicConfigReport(ctx)

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

	// Add each device attributes: id, status, hash, type
	for i, deviceID := range devices {
		nonIdentifyingAttrs = append(nonIdentifyingAttrs, &protobufs.KeyValue{
			Key:   "device.id." + string(rune(i+'0')),
			Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: deviceID}},
		})

		// Add agent type
		agentType := b.getAgentType(deviceID)
		if agentType == "" {
			agentType = "unknown"
		}
		nonIdentifyingAttrs = append(nonIdentifyingAttrs, &protobufs.KeyValue{
			Key:   "device.type." + deviceID,
			Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: agentType}},
		})

		// Status and hash keyed by device ID for server parsing
		b.mu.RLock()
		status, hasStatus := b.ackStatus[deviceID]
		hash := b.ackHash[deviceID]
		config := b.deviceConfigs[deviceID]
		b.mu.RUnlock()
		if hasStatus {
			nonIdentifyingAttrs = append(nonIdentifyingAttrs, &protobufs.KeyValue{
				Key:   "device.status." + deviceID,
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: map[bool]string{true: "applied", false: "failed"}[status]}},
			})
		}
		if hash != "" {
			nonIdentifyingAttrs = append(nonIdentifyingAttrs, &protobufs.KeyValue{
				Key:   "device.hash." + deviceID,
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: hash}},
			})
		}
		// Send actual device config
		if config != "" {
			nonIdentifyingAttrs = append(nonIdentifyingAttrs, &protobufs.KeyValue{
				Key:   "device.config." + deviceID,
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: config}},
			})
		}
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

func (b *RealOpAMPBridge) periodicConfigReport(ctx context.Context) {
	// Disabled for now - would need device agents to implement GetConfig command
	// This is a placeholder for future enhancement
	return
}

func (b *RealOpAMPBridge) onMessage(ctx context.Context, msg *types.MessageData) {
	if msg.RemoteConfig != nil && msg.RemoteConfig.Config != nil && msg.RemoteConfig.Config.ConfigMap != nil {
		log.Printf("[OpAMP] Received remote config: %d entries", len(msg.RemoteConfig.Config.ConfigMap))

		for key, cfg := range msg.RemoteConfig.Config.ConfigMap {
			targetDevice := key
			configPush := &controlpb.ConfigPush{
				DeviceId:   targetDevice,
				ConfigData: cfg.Body,
				ConfigHash: string(msg.RemoteConfig.ConfigHash),
				AgentType:  "otelcol",
			}

			// Store the config so we can report it back
			b.mu.Lock()
			b.deviceConfigs[targetDevice] = string(cfg.Body)
			b.mu.Unlock()

			if err := b.enqueueConfig(targetDevice, configPush); err != nil {
				log.Printf("[OpAMP] Failed to enqueue config to %s: %v", targetDevice, err)
			} else {
				log.Printf("[OpAMP] Queued config for %s (hash=%s, size=%d)", targetDevice, configPush.ConfigHash, len(cfg.Body))
			}
		}
	}

	if msg.AgentIdentification != nil {
		log.Printf("[OpAMP] Agent identification received")
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

func (b *RealOpAMPBridge) OnConfigAck(nodeID string, ack *controlpb.ConfigAck) error {
	// Record status/hash and refresh description so server picks up status
	b.mu.Lock()
	b.ackStatus[ack.GetDeviceId()] = ack.GetSuccess()
	b.ackHash[ack.GetDeviceId()] = ack.GetConfigHash()
	
	// Store the effective config reported by the device
	if len(ack.GetEffectiveConfig()) > 0 {
		b.deviceConfigs[ack.GetDeviceId()] = string(ack.GetEffectiveConfig())
		log.Printf("[OpAMP] Stored effective config from device %s (%d bytes)", 
			ack.GetDeviceId(), len(ack.GetEffectiveConfig()))
	}
	
	client := b.client
	b.mu.Unlock()

	if client != nil {
		if err := b.updateAgentDescription(client); err != nil {
			log.Printf("[OpAMP] Failed to refresh agent description on ack: %v", err)
		}
	}
	return nil
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

func (b *InMemoryBridge) OnConfigAck(nodeID string, ack *controlpb.ConfigAck) error {
	log.Printf("[OpAMP stub] ConfigAck from %s: device=%s, success=%v, hash=%s, effective_config_size=%d",
		nodeID, ack.GetDeviceId(), ack.GetSuccess(), ack.GetConfigHash(), len(ack.GetEffectiveConfig()))
	return nil
}

func (b *InMemoryBridge) Stop(ctx context.Context) error {
	log.Printf("[InMemoryBridge] Stopping bridge")
	return nil
}
