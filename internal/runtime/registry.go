package runtime

import (
	"context"
	"sync"

	"local.dev/opamp-poc-supervisor/api/controlpb"
)

type Stream interface {
	Send(*controlpb.Envelope) error
	Context() context.Context
}

type Registry struct {
	mu      sync.RWMutex
	streams map[string]Stream
}

func NewRegistry() *Registry {
	return &Registry{streams: make(map[string]Stream)}
}

func (r *Registry) Upsert(nodeID string, s Stream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streams[nodeID] = s
}

func (r *Registry) Get(nodeID string) (Stream, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.streams[nodeID]
	return s, ok
}

func (r *Registry) Remove(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.streams, nodeID)
}

func (r *Registry) ListNodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	nodes := make([]string, 0, len(r.streams))
	for nodeID := range r.streams {
		nodes = append(nodes, nodeID)
	}
	return nodes
}
