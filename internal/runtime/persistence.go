package runtime

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// DeviceState represents persistent device metadata
type DeviceState struct {
	NodeID        string    `json:"node_id"`
	AgentType     string    `json:"agent_type"`
	Connected     bool      `json:"connected"`
	LastSeen      time.Time `json:"last_seen"`
	FirstSeen     time.Time `json:"first_seen"`
	Reconnections int       `json:"reconnections"`
}

// PersistentRegistry extends Registry with persistent state
type PersistentRegistry struct {
	*Registry
	mu         sync.RWMutex
	stateFile  string
	deviceList map[string]*DeviceState // nodeID -> state
}

func NewPersistentRegistry(stateFile string) *PersistentRegistry {
	pr := &PersistentRegistry{
		Registry:   NewRegistry(),
		stateFile:  stateFile,
		deviceList: make(map[string]*DeviceState),
	}
	pr.loadState()
	go pr.periodicStateSave()
	go pr.staleConnectionCleanup()
	return pr
}

// loadState restores device state from disk
func (pr *PersistentRegistry) loadState() {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	data, err := os.ReadFile(pr.stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[Registry] No existing state file at %s, starting fresh", pr.stateFile)
			return
		}
		log.Printf("[Registry] Failed to read state file: %v", err)
		return
	}

	var devices []*DeviceState
	if err := json.Unmarshal(data, &devices); err != nil {
		log.Printf("[Registry] Failed to parse state file: %v", err)
		return
	}

	for _, dev := range devices {
		// Mark all loaded devices as disconnected (they need to reconnect)
		dev.Connected = false
		pr.deviceList[dev.NodeID] = dev
	}

	log.Printf("[Registry] Loaded %d device states from disk", len(devices))
}

// saveState persists current device state to disk
func (pr *PersistentRegistry) saveState() error {
	pr.mu.RLock()
	devices := make([]*DeviceState, 0, len(pr.deviceList))
	for _, dev := range pr.deviceList {
		devices = append(devices, dev)
	}
	pr.mu.RUnlock()

	data, err := json.MarshalIndent(devices, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write: write to temp file, then rename
	tempFile := pr.stateFile + ".tmp"
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return err
	}

	return os.Rename(tempFile, pr.stateFile)
}

// periodicStateSave saves state every 30 seconds
func (pr *PersistentRegistry) periodicStateSave() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if err := pr.saveState(); err != nil {
			log.Printf("[Registry] Failed to save state: %v", err)
		}
	}
}

// staleConnectionCleanup marks devices as disconnected after 2 minutes of no heartbeat
func (pr *PersistentRegistry) staleConnectionCleanup() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		pr.mu.Lock()
		now := time.Now()
		staleTimeout := 2 * time.Minute

		for nodeID, dev := range pr.deviceList {
			if dev.Connected && now.Sub(dev.LastSeen) > staleTimeout {
				log.Printf("[Registry] Device %s marked as stale (last seen: %s ago)",
					nodeID, now.Sub(dev.LastSeen))
				dev.Connected = false
			}
		}
		pr.mu.Unlock()
	}
}

// OnDeviceConnect records device connection
func (pr *PersistentRegistry) OnDeviceConnect(nodeID, agentType string, stream Stream) {
	pr.Registry.Upsert(nodeID, stream)
	if agentType != "" {
		pr.Registry.SetAgentType(nodeID, agentType)
	}

	pr.mu.Lock()
	defer pr.mu.Unlock()

	now := time.Now()
	if dev, exists := pr.deviceList[nodeID]; exists {
		// Existing device reconnecting
		dev.Connected = true
		dev.LastSeen = now
		dev.Reconnections++
		if agentType != "" {
			dev.AgentType = agentType
		}
		log.Printf("[Registry] Device %s reconnected (total reconnections: %d)", nodeID, dev.Reconnections)
	} else {
		// New device
		pr.deviceList[nodeID] = &DeviceState{
			NodeID:        nodeID,
			AgentType:     agentType,
			Connected:     true,
			FirstSeen:     now,
			LastSeen:      now,
			Reconnections: 0,
		}
		log.Printf("[Registry] New device %s registered (agent_type: %s)", nodeID, agentType)
	}

	// Save state immediately on connect
	go pr.saveState()
}

// OnDeviceDisconnect records device disconnection
func (pr *PersistentRegistry) OnDeviceDisconnect(nodeID string) {
	pr.Registry.Remove(nodeID)

	pr.mu.Lock()
	defer pr.mu.Unlock()

	if dev, exists := pr.deviceList[nodeID]; exists {
		dev.Connected = false
		dev.LastSeen = time.Now()
		log.Printf("[Registry] Device %s disconnected", nodeID)
	}

	// Save state immediately on disconnect
	go pr.saveState()
}

// UpdateHeartbeat updates last seen timestamp for a device
func (pr *PersistentRegistry) UpdateHeartbeat(nodeID string) {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	if dev, exists := pr.deviceList[nodeID]; exists {
		dev.LastSeen = time.Now()
		if !dev.Connected {
			dev.Connected = true
			log.Printf("[Registry] Device %s marked as connected (heartbeat received)", nodeID)
		}
	}
}

// GetDeviceState returns device state information
func (pr *PersistentRegistry) GetDeviceState(nodeID string) (*DeviceState, bool) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	dev, exists := pr.deviceList[nodeID]
	if !exists {
		return nil, false
	}

	// Return a copy to prevent external modifications
	stateCopy := *dev
	return &stateCopy, true
}

// ListConnectedDevices returns only currently connected devices
func (pr *PersistentRegistry) ListConnectedDevices() []string {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	devices := make([]string, 0)
	for nodeID, dev := range pr.deviceList {
		if dev.Connected {
			devices = append(devices, nodeID)
		}
	}
	return devices
}

// ListAllDevices returns all known devices (connected or not)
func (pr *PersistentRegistry) ListAllDevices() []string {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	devices := make([]string, 0, len(pr.deviceList))
	for nodeID := range pr.deviceList {
		devices = append(devices, nodeID)
	}
	return devices
}

// GetDeviceStates returns all device states
func (pr *PersistentRegistry) GetDeviceStates() []*DeviceState {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	states := make([]*DeviceState, 0, len(pr.deviceList))
	for _, dev := range pr.deviceList {
		stateCopy := *dev
		states = append(states, &stateCopy)
	}
	return states
}

// GetAgentType overrides base Registry to check persistent state first
func (pr *PersistentRegistry) GetAgentType(nodeID string) string {
	// First check base registry (for currently connected devices)
	if agentType := pr.Registry.GetAgentType(nodeID); agentType != "" {
		return agentType
	}

	// Fall back to persistent state
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	if dev, exists := pr.deviceList[nodeID]; exists && dev.AgentType != "" {
		return dev.AgentType
	}

	return ""
}
