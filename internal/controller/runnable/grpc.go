/**
 * This file is adapted from Gateway API Inference Extension
 * Original source: https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/main/internal/runnable/grpc.go
 * Licensed under the Apache License, Version 2.0
 */

// Package runnable contains tooling to manage and convert manager.Runnable
// objects for controllers.
package runnable

import (
	"context"
	"fmt"
	"net"

	"google.golang.org/grpc"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// GRPCServer promotes the provided grpc.Server to a manager.Runnable.
func GRPCServer(name string, srv *grpc.Server, port int) manager.Runnable {
	return manager.RunnableFunc(func(ctx context.Context) error {
		log := ctrl.Log.WithValues("name", name)
		log.Info("gRPC server starting")

		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			log.Error(err, "gRPC server failed to listen", "port", port)
			return err
		}

		log.Info("gRPC server listening", "port", port)

		doneCh := make(chan struct{})
		defer close(doneCh)
		go func() {
			select {
			case <-ctx.Done():
				log.Info("gRPC server shutting down")
				srv.GracefulStop()
			case <-doneCh:
			}
		}()

		if err := srv.Serve(listener); err != nil && err != grpc.ErrServerStopped {
			log.Error(err, "gRPC server failed")
			return err
		}

		log.Info("gRPC server terminated")

		return nil
	})
}
