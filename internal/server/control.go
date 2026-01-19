package server

import (
	"fmt"
	"io"
	"log"
	"sync"

	"local.dev/opamp-supervisor/api/controlpb"
	"local.dev/opamp-supervisor/internal/runtime"
)

type ControlService struct {
	controlpb.UnimplementedControlServiceServer

	registry *runtime.PersistentRegistry

	mu           sync.Mutex
	cmdQueues    map[string]chan *controlpb.Command
	configQueues map[string]chan *controlpb.ConfigPush

	bridge OpAMPBridge
}

func NewControlService(reg *runtime.PersistentRegistry, bridge OpAMPBridge) *ControlService {
	return &ControlService{
		registry:     reg,
		cmdQueues:    make(map[string]chan *controlpb.Command),
		configQueues: make(map[string]chan *controlpb.ConfigPush),
		bridge:       bridge,
	}
}

func (s *ControlService) queueFor(nodeID string) chan *controlpb.Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.cmdQueues[nodeID]
	if !ok {
		q = make(chan *controlpb.Command, 64)
		s.cmdQueues[nodeID] = q
	}
	return q
}

func (s *ControlService) configQueueFor(nodeID string) chan *controlpb.ConfigPush {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.configQueues[nodeID]
	if !ok {
		q = make(chan *controlpb.ConfigPush, 64)
		s.configQueues[nodeID] = q
	}
	return q
}

func (s *ControlService) Control(stream controlpb.ControlService_ControlServer) error {
	var nodeID string

	// Expect initial Register from the edge
	first, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("initial recv: %w", err)
	}
	reg := first.GetRegister()
	if reg == nil || reg.GetNodeId() == "" {
		return fmt.Errorf("first message must be register with non-empty node_id")
	}
	nodeID = reg.GetNodeId()
	agentType := reg.GetAgentType()
	log.Printf("[edge %s] connected (version=%s platform=%s agentType=%s)", nodeID, reg.GetVersion(), reg.GetPlatform(), agentType)

	// Register stream and agent type with persistent tracking
	s.registry.OnDeviceConnect(nodeID, agentType, stream)
	defer func() {
		s.registry.OnDeviceDisconnect(nodeID)
		log.Printf("[edge %s] disconnected", nodeID)
	}()

	// Command pump: supervisor -> edge
	cmdQ := s.queueFor(nodeID)
	configQ := s.configQueueFor(nodeID)
	errCh := make(chan error, 2)

	go func() {
		for {
			select {
			case <-stream.Context().Done():
				log.Printf("[edge %s] Command pump: context done: %v", nodeID, stream.Context().Err())
				errCh <- stream.Context().Err()
				return
			case cmd := <-cmdQ:
				if cmd == nil {
					log.Printf("[edge %s] Command pump: received nil command", nodeID)
					errCh <- io.EOF
					return
				}
				if err := stream.Send(&controlpb.Envelope{Body: &controlpb.Envelope_Command{Command: cmd}}); err != nil {
					log.Printf("[edge %s] Command pump: send error: %v", nodeID, err)
					errCh <- fmt.Errorf("send command: %w", err)
					return
				}
			case cfg := <-configQ:
				if cfg == nil {
					log.Printf("[edge %s] Command pump: received nil config", nodeID)
					errCh <- io.EOF
					return
				}
				log.Printf("[edge %s] Sending ConfigPush: device=%s, hash=%s, size=%d",
					nodeID, cfg.DeviceId, cfg.ConfigHash, len(cfg.ConfigData))
				if err := stream.Send(&controlpb.Envelope{Body: &controlpb.Envelope_ConfigPush{ConfigPush: cfg}}); err != nil {
					log.Printf("[edge %s] Command pump: send ConfigPush error: %v", nodeID, err)
					errCh <- fmt.Errorf("send config_push: %w", err)
					return
				}
			}
		}
	}()

	// Receive loop: edge -> supervisor
	go func() {
		for {
			in, recvErr := stream.Recv()
			if recvErr == io.EOF {
				log.Printf("[edge %s] Receive loop: got EOF", nodeID)
				errCh <- recvErr
				return
			}
			if recvErr != nil {
				log.Printf("[edge %s] Receive loop: error: %v", nodeID, recvErr)
				errCh <- fmt.Errorf("recv: %w", recvErr)
				return
			}

			switch x := in.Body.(type) {
			case *controlpb.Envelope_Event:
				ev := x.Event
				// Update heartbeat on any event
				s.registry.UpdateHeartbeat(nodeID)
				_ = s.bridge.SendStatus(nodeID, ev) // forward to OpAMP stub or log
			case *controlpb.Envelope_ConfigAck:
				ack := x.ConfigAck
				// Update heartbeat on ConfigAck
				s.registry.UpdateHeartbeat(nodeID)
				log.Printf("[edge %s] ConfigAck received: device=%s, hash=%s, success=%v, error=%s",
					nodeID, ack.DeviceId, ack.ConfigHash, ack.Success, ack.ErrorMessage)
				// Forward ACK to OpAMP server via bridge
				_ = s.bridge.OnConfigAck(nodeID, ack)
			case *controlpb.Envelope_Register:
				// already registered; ignore duplicates
			case *controlpb.Envelope_Command:
				log.Printf("[edge %s] unexpected Command from edge: %s", nodeID, x.Command.GetType())
			default:
				log.Printf("[edge %s] unknown envelope type", nodeID)
			}
		}
	}()

	// Wait for termination
	return <-errCh
}

// EnqueueCommand is used by the bridge to deliver commands to a specific node.
func (s *ControlService) EnqueueCommand(nodeID string, cmd *controlpb.Command) error {
	q := s.queueFor(nodeID)
	select {
	case q <- cmd:
		return nil
	default:
		return fmt.Errorf("command queue full for node %s", nodeID)
	}
}

// EnqueueConfigPush is used by the bridge to deliver config pushes to a specific node.
func (s *ControlService) EnqueueConfigPush(nodeID string, cfg *controlpb.ConfigPush) error {
	q := s.configQueueFor(nodeID)
	select {
	case q <- cfg:
		log.Printf("[control] Enqueued ConfigPush for node %s, device %s", nodeID, cfg.DeviceId)
		return nil
	default:
		return fmt.Errorf("config queue full for node %s", nodeID)
	}
}
