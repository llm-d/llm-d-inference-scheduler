/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	extProcPb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/common"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/activator/controller"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/activator/datastore"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/activator/handlers"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/activator/requestcontrol"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/activator/runnable"
	tlsutil "github.com/llm-d/llm-d-inference-scheduler/pkg/activator/tls"
)

// ExtProcServerRunner provides methods to manage an external process server.
type ExtProcServerRunner struct {
	GrpcPort                         int
	PoolNamespacedName               types.NamespacedName
	PoolGKNN                         common.GKNN
	Datastore                        datastore.Datastore
	SecureServing                    bool
	HealthChecking                   bool
	CertPath                         string
	RefreshPrometheusMetricsInterval time.Duration
	MetricsStalenessThreshold        time.Duration
	Activator                        *requestcontrol.Activator
}

// Default values for CLI flags in main
const (
	DefaultGrpcPort                         = 9002                          // default for --grpc-port
	DefaultGrpcHealthPort                   = 9003                          // default for --grpc-health-port
	DefaultMetricsPort                      = 9090                          // default for --metrics-port
	DefaultPoolName                         = ""                            // required but no default
	DefaultPoolNamespace                    = "default"                     // default for --pool-namespace
	DefaultRefreshMetricsInterval           = 50 * time.Millisecond         // default for --refresh-metrics-interval
	DefaultRefreshPrometheusMetricsInterval = 5 * time.Second               // default for --refresh-prometheus-metrics-interval
	DefaultSecureServing                    = true                          // default for --secure-serving
	DefaultHealthChecking                   = false                         // default for --health-checking
	DefaultEnablePprof                      = true                          // default for --enable-pprof
	DefaultCertPath                         = ""                            // default for --cert-path
	DefaultPoolGroup                        = "inference.networking.k8s.io" // default for --pool-group
	DefaultMetricsStalenessThreshold        = 2 * time.Second
)

// NewDefaultExtProcServerRunner creates a runner with default values.
// Note: Dependencies like Datastore, Scheduler, SD need to be set separately.
func NewDefaultExtProcServerRunner() *ExtProcServerRunner {
	poolGKNN := common.GKNN{
		NamespacedName: types.NamespacedName{Name: DefaultPoolName, Namespace: DefaultPoolNamespace},
		GroupKind: schema.GroupKind{
			Group: DefaultPoolGroup,
			Kind:  "InferencePool",
		},
	}
	return &ExtProcServerRunner{
		GrpcPort:                         DefaultGrpcPort,
		PoolNamespacedName:               types.NamespacedName{Name: DefaultPoolName, Namespace: DefaultPoolNamespace},
		PoolGKNN:                         poolGKNN,
		SecureServing:                    DefaultSecureServing,
		HealthChecking:                   DefaultHealthChecking,
		RefreshPrometheusMetricsInterval: DefaultRefreshPrometheusMetricsInterval,
		MetricsStalenessThreshold:        DefaultMetricsStalenessThreshold,
		// Dependencies can be assigned later.
	}
}

// SetupWithManager sets up the runner with the given manager.
func (r *ExtProcServerRunner) SetupWithManager(_ context.Context, mgr ctrl.Manager) error {
	// Create the controllers and register them with the manager
	if err := (&controller.InferencePoolReconciler{
		Datastore: r.Datastore,
		Reader:    mgr.GetClient(),
		PoolGKNN:  r.PoolGKNN,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed setting up InferencePoolReconciler: %w", err)
	}

	return nil
}

// AsRunnable returns a Runnable that can be used to start the ext-proc gRPC server.
// The runnable implements LeaderElectionRunnable with leader election disabled.
func (r *ExtProcServerRunner) AsRunnable(logger logr.Logger) manager.Runnable {
	return runnable.NoLeaderElection(manager.RunnableFunc(func(ctx context.Context) error {

		var srv *grpc.Server
		if r.SecureServing {
			var cert tls.Certificate
			var err error
			if r.CertPath != "" {
				cert, err = tls.LoadX509KeyPair(r.CertPath+"/tls.crt", r.CertPath+"/tls.key")
			} else {
				// Create tls based credential.
				cert, err = tlsutil.CreateSelfSignedTLSCertificate(logger)
			}
			if err != nil {
				return fmt.Errorf("failed to create self signed certificate - %w", err)
			}

			creds := credentials.NewTLS(&tls.Config{
				Certificates: []tls.Certificate{cert},
			})
			// Init the server.
			srv = grpc.NewServer(grpc.Creds(creds))
		} else {
			srv = grpc.NewServer()
		}

		extProcServer := handlers.NewStreamingServer(r.Datastore, r.Activator)
		extProcPb.RegisterExternalProcessorServer(srv, extProcServer)

		if r.HealthChecking {
			healthcheck := health.NewServer()
			healthgrpc.RegisterHealthServer(srv,
				healthcheck,
			)
			svcName := extProcPb.ExternalProcessor_ServiceDesc.ServiceName
			logger.Info("Setting ExternalProcessor service status to SERVING", "serviceName", svcName)
			healthcheck.SetServingStatus(svcName, healthgrpc.HealthCheckResponse_SERVING)
		}

		// Forward to the gRPC runnable.
		return runnable.GRPCServer("ext-proc", srv, r.GrpcPort).Start(ctx)
	}))
}
