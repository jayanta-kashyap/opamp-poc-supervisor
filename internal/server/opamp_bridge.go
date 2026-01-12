package server

import (
    "context"
    "log"
    "time"

    "local.dev/opamp-poc-supervisor/api/controlpb"
)

type OpAMPBridge interface {
    Start(ctx context.Context) error
    SendStatus(nodeID string, ev *controlpb.Event) error
    PushCommand(nodeID string, cmd *controlpb.Command) error
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
