package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"local.dev/opamp-poc-supervisor/api/controlpb"
	"local.dev/opamp-poc-supervisor/internal/runtime"
	"local.dev/opamp-poc-supervisor/internal/server"
)

func main() {
	reg := runtime.NewRegistry()

	var svc *server.ControlService
	enqueue := func(nodeID string, cmd *controlpb.Command) error {
		return svc.EnqueueCommand(nodeID, cmd)
	}

	bridge := server.NewInMemoryBridge(enqueue)
	svc = server.NewControlService(reg, bridge)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := bridge.Start(ctx); err != nil {
		log.Fatalf("bridge start: %v", err)
	}

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
	grpcServer.GracefulStop()
}
