package server

import (
	"fmt"
	"io"
	"log"
	"sync"

	"local.dev/opamp-poc-supervisor/api/controlpb"
	"local.dev/opamp-poc-supervisor/internal/runtime"
)

type ControlService struct {
	controlpb.UnimplementedControlServiceServer

	registry *runtime.Registry

	mu        sync.Mutex
	cmdQueues map[string]chan *controlpb.Command

	bridge OpAMPBridge
}

func NewControlService(reg *runtime.Registry, bridge OpAMPBridge) *ControlService {
	return &ControlService{
		registry:  reg,
		cmdQueues: make(map[string]chan *controlpb.Command),
		bridge:    bridge,
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
	log.Printf("[edge %s] connected (version=%s platform=%s)", nodeID, reg.GetVersion(), reg.GetPlatform())

	// Register stream; ensure cleanup
	s.registry.Upsert(nodeID, stream)
	defer func() {
		s.registry.Remove(nodeID)
		log.Printf("[edge %s] disconnected", nodeID)
	}()

	// Command pump: supervisor -> edge
	cmdQ := s.queueFor(nodeID)
	errCh := make(chan error, 2)

	go func() {
		for {
			select {
			case <-stream.Context().Done():
				errCh <- stream.Context().Err()
				return
			case cmd := <-cmdQ:
				if cmd == nil {
					errCh <- io.EOF
					return
				}
				if err := stream.Send(&controlpb.Envelope{Body: &controlpb.Envelope_Command{Command: cmd}}); err != nil {
					errCh <- fmt.Errorf("send command: %w", err)
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
				errCh <- recvErr
				return
			}
			if recvErr != nil {
				errCh <- fmt.Errorf("recv: %w", recvErr)
				return
			}

			switch x := in.Body.(type) {
			case *controlpb.Envelope_Event:
				ev := x.Event
				_ = s.bridge.SendStatus(nodeID, ev) // forward to OpAMP stub or log
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
