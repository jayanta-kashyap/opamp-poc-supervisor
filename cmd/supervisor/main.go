package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"local.dev/opamp-supervisor/api/controlpb"
	"local.dev/opamp-supervisor/internal/runtime"
	"local.dev/opamp-supervisor/internal/server"
)

func main() {
	// Use persistent registry with state file in /var/lib/supervisor
	stateFile := os.Getenv("STATE_FILE")
	if stateFile == "" {
		stateFile = "/var/lib/supervisor/devices.json"
	}

	// Ensure state directory exists
	stateDir := "/var/lib/supervisor"
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		log.Printf("[WARN] Failed to create state directory: %v, using in-memory registry", err)
		stateFile = "" // Fall back to in-memory
	}

	var reg *runtime.PersistentRegistry
	if stateFile != "" {
		reg = runtime.NewPersistentRegistry(stateFile)
		log.Printf("Using persistent registry with state file: %s", stateFile)
	} else {
		// Fallback: wrap regular registry as persistent (won't actually persist)
		baseReg := runtime.NewRegistry()
		reg = &runtime.PersistentRegistry{
			Registry: baseReg,
		}
		log.Printf("Using in-memory registry (no persistence)")
	}

	var svc *server.ControlService
	enqueue := func(nodeID string, cmd *controlpb.Command) error {
		return svc.EnqueueCommand(nodeID, cmd)
	}
	enqueueConfigPush := func(nodeID string, cfg *controlpb.ConfigPush) error {
		return svc.EnqueueConfigPush(nodeID, cfg)
	}

	// Get OpAMP server URL from environment or use default
	opampURL := os.Getenv("OPAMP_SERVER_URL")
	if opampURL == "" {
		opampURL = "ws://opamp-server.opamp-control.svc.cluster.local:4320/v1/opamp"
		log.Printf("OPAMP_SERVER_URL not set, using default: %s", opampURL)
	}

	// Use real OpAMP bridge with persistent registry's device list
	bridge := server.NewRealOpAMPBridge(opampURL, enqueue, enqueueConfigPush, reg.ListConnectedDevices, reg.GetAgentType)
	svc = server.NewControlService(reg, bridge)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Println("[MAIN] About to start OpAMP bridge")
	if err := bridge.Start(ctx); err != nil {
		log.Fatalf("bridge start: %v", err)
	}
	log.Println("[MAIN] Bridge started successfully")

	// Start dashboard web UI
	dashboard := server.NewDashboardServer(reg, bridge)
	go func() {
		log.Println("Dashboard UI starting on :8080")
		if err := dashboard.Start(":8080"); err != nil {
			log.Fatalf("dashboard serve: %v", err)
		}
	}()

	grpcServer := grpc.NewServer()
	reflection.Register(grpcServer)
	controlpb.RegisterControlServiceServer(grpcServer, svc)

	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	go func() {
		log.Println("Supervisor gRPC listening on :50051")
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("grpc serve: %v", err)
		}
	}()

	// Graceful shutdown
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	<-sigs

	log.Println("Shutting down supervisor...")

	// Stop OpAMP bridge
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := bridge.Stop(shutdownCtx); err != nil {
		log.Printf("Error stopping bridge: %v", err)
	}

	grpcServer.GracefulStop()
}
