package runtime

import (
	"context"
	"sync"

	"local.dev/opamp-supervisor/api/controlpb"
)

type Stream interface {
	Send(*controlpb.Envelope) error
	Context() context.Context
}

type DeviceInfo struct {
	Stream    Stream
	AgentType string
}

type Registry struct {
	mu      sync.RWMutex
	devices map[string]*DeviceInfo
}

func NewRegistry() *Registry {
	return &Registry{devices: make(map[string]*DeviceInfo)}
}

func (r *Registry) Upsert(nodeID string, s Stream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.devices[nodeID]; ok {
		info.Stream = s
	} else {
		r.devices[nodeID] = &DeviceInfo{Stream: s}
	}
}

func (r *Registry) SetAgentType(nodeID string, agentType string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.devices[nodeID]; ok {
		info.AgentType = agentType
	} else {
		r.devices[nodeID] = &DeviceInfo{AgentType: agentType}
	}
}

func (r *Registry) GetAgentType(nodeID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if info, ok := r.devices[nodeID]; ok {
		return info.AgentType
	}
	return ""
}

func (r *Registry) Get(nodeID string) (Stream, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if info, ok := r.devices[nodeID]; ok && info.Stream != nil {
		return info.Stream, true
	}
	return nil, false
}

func (r *Registry) Remove(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.devices, nodeID)
}

func (r *Registry) ListNodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	nodes := make([]string, 0, len(r.devices))
	for nodeID := range r.devices {
		nodes = append(nodes, nodeID)
	}
	return nodes
}
