package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"local.dev/opamp-supervisor/api/controlpb"
	"local.dev/opamp-supervisor/internal/runtime"
)

type DashboardServer struct {
	registry *runtime.PersistentRegistry
	bridge   OpAMPBridge
	mu       sync.RWMutex
	devices  map[string]*DeviceInfo
}

type DeviceInfo struct {
	ID       string    `json:"id"`
	Status   string    `json:"status"`
	Version  string    `json:"version"`
	Platform string    `json:"platform"`
	LastSeen time.Time `json:"lastSeen"`
}

func NewDashboardServer(registry *runtime.PersistentRegistry, bridge OpAMPBridge) *DashboardServer {
	return &DashboardServer{
		registry: registry,
		bridge:   bridge,
		devices:  make(map[string]*DeviceInfo),
	}
}

func (s *DashboardServer) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/api/devices", s.handleGetDevices)
	mux.HandleFunc("/api/push-config", s.handlePushConfig)

	log.Printf("Dashboard server starting on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *DashboardServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	log.Printf("[Dashboard] Request: %s %s", r.Method, r.URL.Path)

	// Serve embedded HTML
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(dashboardHTML))
}

const dashboardHTML = `<!DOCTYPE html>
<html>
<head>
    <title>OpAMP Supervisor Dashboard</title>
    <style>
        body {
            font-family: Arial, sans-serif;
            margin: 20px;
            background-color: #f5f5f5;
        }
        .container {
            max-width: 1200px;
            margin: 0 auto;
            background: white;
            padding: 20px;
            border-radius: 8px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        h1 { color: #333; }
        .device-item {
            padding: 10px;
            margin: 5px 0;
            border: 1px solid #ddd;
            border-radius: 4px;
            background: white;
            cursor: pointer;
            display: flex;
            align-items: center;
        }
        .device-item:hover { background: #f0f0f0; }
        .device-item.selected {
            background: #e3f2fd;
            border-color: #2196F3;
        }
        .status-badge {
            display: inline-block;
            padding: 4px 8px;
            border-radius: 4px;
            font-size: 12px;
            margin-left: 10px;
            background: #4CAF50;
            color: white;
        }
        .config-textarea {
            width: 100%;
            min-height: 300px;
            font-family: 'Courier New', monospace;
            padding: 10px;
            border: 1px solid #ddd;
            border-radius: 4px;
        }
        .button {
            background: #2196F3;
            color: white;
            padding: 10px 20px;
            border: none;
            border-radius: 4px;
            cursor: pointer;
            font-size: 16px;
            margin-top: 10px;
        }
        .button:hover { background: #1976D2; }
        .button:disabled {
            background: #ccc;
            cursor: not-allowed;
        }
        .template-button {
            background: #4CAF50;
            color: white;
            padding: 8px 16px;
            border: none;
            border-radius: 4px;
            cursor: pointer;
            margin-right: 10px;
            font-size: 14px;
        }
        .message {
            padding: 10px;
            margin: 10px 0;
            border-radius: 4px;
        }
        .message.success {
            background: #d4edda;
            color: #155724;
            border: 1px solid #c3e6cb;
        }
        .message.error {
            background: #f8d7da;
            color: #721c24;
            border: 1px solid #f5c6cb;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>🎛️ OpAMP Supervisor Dashboard</h1>
        
        <div style="border: 1px solid #ddd; padding: 15px; margin: 10px 0; border-radius: 4px; border-left: 4px solid #4CAF50; background: #f9f9f9;">
            <h2>Supervisor Agent</h2>
            <p><strong>Status:</strong> <span class="status-badge">Connected</span></p>
            <p><strong>Instance:</strong> supervisor-001</p>
        </div>

        <div style="margin-top: 20px;">
            <h2>Connected Device Agents</h2>
            <p>Select device(s) to configure:</p>
            <div id="devices"></div>
        </div>

        <div style="margin-top: 30px; padding: 20px; border: 1px solid #ddd; border-radius: 4px;">
            <h2>Push Configuration</h2>
            <p><strong>Selected Devices:</strong> <span id="selected-count">0</span></p>
            
            <div style="margin-top: 15px;">
                <h3>Quick Templates:</h3>
                <button class="template-button" onclick="loadTemplate('logs')">Enable Logs Pipeline</button>
                <button class="template-button" onclick="loadTemplate('traces')">Enable Traces Pipeline</button>
                <button class="template-button" onclick="loadTemplate('both')">Enable Both</button>
            </div>

            <textarea id="config" class="config-textarea" placeholder="Enter OTel Collector configuration (YAML format)..."></textarea>
            
            <button class="button" onclick="pushConfig()" id="push-button">Push Configuration to Selected Devices</button>
            
            <div id="message"></div>
        </div>
    </div>

    <script>
        let devices = [];
        let selectedDevices = new Set();

        async function init() {
            await loadDevices();
            setInterval(loadDevices, 5000);
        }

        async function loadDevices() {
            try {
                const response = await fetch('/api/devices');
                const data = await response.json();
                devices = data.devices || [];
                renderDevices();
            } catch (error) {
                console.error('Failed to load devices:', error);
            }
        }

        function renderDevices() {
            const container = document.getElementById('devices');
            if (devices.length === 0) {
                container.innerHTML = '<p style="color: #999;">No devices connected</p>';
                return;
            }

            container.innerHTML = devices.map(device => {
                const isSelected = selectedDevices.has(device.id);
                return ` + "`" + `
                    <div class="device-item \${isSelected ? 'selected' : ''}" onclick="toggleDevice('\${device.id}')">
                        <input type="checkbox" style="margin-right: 10px;" \${isSelected ? 'checked' : ''} onclick="event.stopPropagation(); toggleDevice('\${device.id}')">
                        <div>
                            <strong>\${device.id}</strong>
                            <span class="status-badge">\${device.status}</span>
                            <br>
                            <small>Version: \${device.version} | Platform: \${device.platform}</small>
                        </div>
                    </div>
                ` + "`" + `;
            }).join('');

            updateSelectedCount();
        }

        function toggleDevice(deviceId) {
            if (selectedDevices.has(deviceId)) {
                selectedDevices.delete(deviceId);
            } else {
                selectedDevices.add(deviceId);
            }
            renderDevices();
        }

        function updateSelectedCount() {
            document.getElementById('selected-count').textContent = selectedDevices.size;
            document.getElementById('push-button').disabled = selectedDevices.size === 0;
        }

        function loadTemplate(type) {
            const templates = {
                logs: ` + "`" + `receivers:
  otlp:
    protocols:
      grpc:

processors:
  batch:

exporters:
  logging:
    loglevel: debug

service:
  pipelines:
    logs:
      receivers: [otlp]
      processors: [batch]
      exporters: [logging]` + "`" + `,
                traces: ` + "`" + `receivers:
  otlp:
    protocols:
      grpc:

processors:
  batch:

exporters:
  logging:
    loglevel: debug

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [logging]` + "`" + `,
                both: ` + "`" + `receivers:
  otlp:
    protocols:
      grpc:

processors:
  batch:

exporters:
  logging:
    loglevel: debug

service:
  pipelines:
    logs:
      receivers: [otlp]
      processors: [batch]
      exporters: [logging]
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [logging]` + "`" + `
            };

            document.getElementById('config').value = templates[type] || '';
        }

        async function pushConfig() {
            const config = document.getElementById('config').value;
            if (!config.trim()) {
                showMessage('Please enter a configuration', 'error');
                return;
            }

            if (selectedDevices.size === 0) {
                showMessage('Please select at least one device', 'error');
                return;
            }

            try {
                const response = await fetch('/api/push-config', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        devices: Array.from(selectedDevices),
                        config: config
                    })
                });

                const result = await response.json();
                if (response.ok) {
                    showMessage('✅ Configuration pushed to ' + selectedDevices.size + ' device(s)', 'success');
                } else {
                    showMessage('❌ Failed: ' + (result.error || 'Unknown error'), 'error');
                }
            } catch (error) {
                showMessage('❌ Failed: ' + error.message, 'error');
            }
        }

        function showMessage(text, type) {
            const msgDiv = document.getElementById('message');
            msgDiv.innerHTML = '<div class="message ' + type + '">' + text + '</div>';
            setTimeout(() => { msgDiv.innerHTML = ''; }, 5000);
        }

        init();
    </script>
</body>
</html>
`

func (s *DashboardServer) handleGetDevices(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Get connected devices from registry
	nodes := s.registry.ListNodes()
	devices := make([]*DeviceInfo, 0, len(nodes))

	for _, nodeID := range nodes {
		if info, ok := s.devices[nodeID]; ok {
			devices = append(devices, info)
		} else {
			// Create basic info if not tracked
			devices = append(devices, &DeviceInfo{
				ID:       nodeID,
				Status:   "connected",
				Version:  "unknown",
				Platform: "unknown",
				LastSeen: time.Now(),
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"devices": devices,
	})
}

func (s *DashboardServer) handlePushConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Devices []string `json:"devices"`
		Config  string   `json:"config"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[Dashboard] Pushing config to %d devices", len(req.Devices))

	// Push config to each selected device via supervisor
	for _, deviceID := range req.Devices {
		cmd := &controlpb.Command{
			Type:    "UpdateConfig",
			Payload: req.Config,
		}

		if err := s.bridge.PushCommand(deviceID, cmd); err != nil {
			log.Printf("[Dashboard] Failed to push to %s: %v", deviceID, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		log.Printf("[Dashboard] Config pushed to %s", deviceID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Configuration pushed successfully",
	})
}

func (s *DashboardServer) UpdateDeviceInfo(nodeID string, identity *controlpb.EdgeIdentity) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.devices[nodeID] = &DeviceInfo{
		ID:       nodeID,
		Status:   "connected",
		Version:  identity.Version,
		Platform: identity.Platform,
		LastSeen: time.Now(),
	}
}
