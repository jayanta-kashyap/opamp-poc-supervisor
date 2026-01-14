# Supervisor (OpAMP-gRPC Bridge)

## Overview
The Supervisor acts as a **bridge** between the cloud-based OpAMP Server and edge-based Device Agents. It translates OpAMP protocol (WebSocket) into bidirectional gRPC streams, enabling secure and efficient communication with edge devices.

## Role in the POC Architecture

```
        Cloud Layer (Minikube)
┌─────────────────────────────────────────┐
│          OpAMP Server                   │
│      (WebSocket - OpAMP Protocol)       │
└─────────────────────────────────────────┘
                    ↕
           OpAMP over WebSocket
                    ↕
┌─────────────────────────────────────────┐
│    Supervisor (This Component)          │
│  ┌──────────────┐  ┌─────────────────┐ │
│  │ OpAMP Client │  │  gRPC Server    │ │
│  │  (Port 4320) │  │  (Port 50051)   │ │
│  └──────────────┘  └─────────────────┘ │
│  ┌──────────────────────────────────┐  │
│  │   Dashboard (Port 8080)          │  │
│  └──────────────────────────────────┘  │
└─────────────────────────────────────────┘
                    ↕
        Bidirectional gRPC Streams
                    ↕
┌─────────────────────────────────────────┐
│        Device Agents (Edge)             │
│  device-1, device-2, ... device-N       │
└─────────────────────────────────────────┘
```

## Components

### 1. OpAMP Bridge (OpAMP Client)
**Purpose:** Connects to OpAMP Server and manages the OpAMP protocol lifecycle.

**Functionality:**
- **Connection Management:** Establishes and maintains WebSocket connection to OpAMP Server
- **Agent Registration:** Registers itself as a supervisor with device list
- **Device List Sync:** Periodically updates OpAMP Server with current device connections (every 10 seconds)
- **Remote Config Reception:** Receives configuration updates from OpAMP Server
- **Config Routing:** Routes received configs to appropriate devices via gRPC

**How it works:**
```go
// Creates OpAMP client connection
client := opampclient.NewWebSocket(logger)

// Registers with identifying attributes
ServiceName: "supervisor"
InstanceID: "supervisor-001"

// Reports connected devices in non-identifying attributes
device.0: "device-1"
device.1: "device-2"
device.count: 2

// Handles incoming remote configs
onMessage() -> extracts device-specific config -> enqueues to device
```

**Key Methods:**
- `Start()`: Initializes OpAMP connection to server
- `SendStatus()`: Sends device status updates upstream to OpAMP Server
- `PushCommand()`: Queues commands to be sent to devices
- `onMessage()`: Processes incoming OpAMP messages and routes configs
- `updateAgentDescription()`: Keeps OpAMP Server updated with device list

### 2. gRPC Control Server (Port 50051)
**Purpose:** Provides bidirectional streaming RPC for device agents to connect and communicate.

**Functionality:**
- **Device Registration:** Accepts streaming connections from device agents
- **Bidirectional Communication:** Maintains persistent streams for each device
- **Command Distribution:** Pushes commands/configs to devices in real-time
- **Event Collection:** Receives status updates and events from devices

**Protocol Definition (control.proto):**
```protobuf
service ControlService {
  rpc Stream(stream Event) returns (stream Command);
}

message Command {
  string type = 1;           // e.g., "UpdateConfig"
  string payload = 2;        // YAML config
  string correlation_id = 3; // For tracking
}

message Event {
  string type = 1;           // e.g., "Status", "ConfigApplied"
  string payload = 2;        // JSON event data
  string correlation_id = 3;
}
```

**How it works:**
1. Device connects and opens bidirectional stream
2. Device sends initial "Connected" event
3. Supervisor registers device in runtime registry
4. Supervisor can push commands at any time
5. Device sends events (status, config applied, errors)
6. Stream remains open for lifecycle of connection

### 3. Runtime Registry
**Purpose:** Maintains in-memory state of all connected devices.

**Functionality:**
- **Device Tracking:** Maps device IDs to their gRPC streams
- **Queue Management:** Maintains pending command queues per device
- **Connection State:** Tracks which devices are currently connected
- **Thread-Safe:** Uses mutex for concurrent access

**Key Operations:**
```go
RegisterNode(nodeID, stream)   // When device connects
UnregisterNode(nodeID)         // When device disconnects
EnqueueCommand(nodeID, cmd)    // Queue command for device
GetDeviceList()                // Returns all connected device IDs
```

### 4. Dashboard (Port 8080)
**Purpose:** Provides a simple web interface to view supervisor status.

**Functionality:**
- **Device List:** Shows all connected devices with status
- **Command Queue:** Displays pending commands for each device
- **Connection Stats:** Shows gRPC connection health
- **Manual Testing:** Allows sending test commands to devices

**Endpoints:**
- `GET /` - Dashboard HTML
- `GET /api/devices` - JSON list of devices
- `POST /api/command` - Send command to device

## Data Flow Examples

### Device Connection Flow
1. Device agent starts up
2. Device opens gRPC stream to supervisor:50051
3. Device sends "Connected" event with its ID
4. Supervisor registers device in registry
5. Supervisor updates OpAMP Server with new device list
6. OpAMP Server UI shows device as "online"

### Configuration Push Flow (E2E)
1. User enters config in OpAMP UI for "device-1"
2. OpAMP Server sends RemoteConfig via OpAMP protocol
3. Supervisor receives config in `onMessage()`
4. Supervisor extracts device ID from config key
5. Supervisor creates Command message:
   ```
   Type: "UpdateConfig"
   Payload: "<YAML config>"
   ```
6. Supervisor enqueues command to device-1's queue
7. Supervisor pushes command via device-1's gRPC stream
8. Device receives command
9. Device applies config to OTel Collector
10. Device sends "ConfigApplied" event back
11. Supervisor forwards status to OpAMP Server

### Periodic Sync Flow
Every 10 seconds:
1. Supervisor queries registry for device list
2. Supervisor updates OpAMP agent description:
   ```
   NonIdentifyingAttributes:
     - device.0: "device-1"
     - device.1: "device-2"
     - device.count: 2
   ```
3. OpAMP Server updates its device registry
4. UI reflects any changes (new/disconnected devices)

## Key Features in POC

1. **Protocol Translation:** Seamlessly bridges OpAMP ↔ gRPC
2. **Device Multiplexing:** Single OpAMP connection manages many gRPC streams
3. **Resilient Communication:** Handles disconnections and reconnections
4. **Command Queuing:** Ensures commands aren't lost during brief disconnects
5. **Real-time Updates:** Bidirectional streams enable instant config delivery
6. **Cloud-Edge Bridge:** Allows cloud-based management of edge devices

## Technology Stack
- **Language:** Go
- **OpAMP:** github.com/open-telemetry/opamp-go (client)
- **gRPC:** google.golang.org/grpc
- **Protocol Buffers:** Defined in `api/control.proto`

## Deployment
- **Container Image:** `supervisor:latest`
- **Kubernetes:** Deployed in `opamp-system` namespace
- **Environment Variables:**
  - `OPAMP_SERVER_URL`: WebSocket URL of OpAMP Server
  - `OPAMP_INSECURE_TLS`: Set to "true" for testing
- **Ports:**
  - 50051: gRPC server for device connections
  - 8080: HTTP dashboard

## Building
```bash
# Generate gRPC code from proto
protoc --go_out=. --go-grpc_out=. api/control.proto

# Build binary
go build -o supervisor ./cmd/supervisor

# Build Docker image
docker build -t supervisor:latest .
```

## Directory Structure
```
opamp-poc-supervisor/
├── api/
│   ├── control.proto          # gRPC service definition
│   └── controlpb/             # Generated protobuf code
├── cmd/
│   └── supervisor/
│       └── main.go            # Entry point
├── internal/
│   ├── runtime/
│   │   └── registry.go        # Device registry
│   └── server/
│       ├── control.go         # gRPC server implementation
│       ├── dashboard.go       # Web dashboard
│       └── opamp_bridge.go    # OpAMP client bridge
├── web/
│   └── dashboard.html         # Dashboard UI
└── k8s/
    └── supervisor.yaml        # Kubernetes deployment
```

## How This Enables E2E POC

The Supervisor is the **critical bridge** that makes the POC work:

1. **Protocol Adaptation:** OpAMP (standard) → gRPC (efficient for edge)
2. **Connection Aggregation:** One OpAMP connection manages N devices
3. **Real-time Delivery:** Instant config push without polling
4. **Device Lifecycle:** Automatic registration and tracking
5. **Bidirectional Comms:** Commands down, status up
6. **Cloud-Edge Decoupling:** OpAMP Server doesn't need to know about gRPC

**Without the supervisor:**
- OpAMP Server would need to connect directly to each device
- No protocol translation possible
- Can't leverage gRPC's efficiency for edge
- Harder to manage device-specific authentication
- No aggregation of device connections

The supervisor enables **scalable, efficient, and standards-based** management of distributed OTel collectors.
