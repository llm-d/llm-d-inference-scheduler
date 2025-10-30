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

package runner

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/go-logr/logr"
	uberzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	healthPb "google.golang.org/grpc/health/grpc_health_v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/common"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/version"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/activator/datastore"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/activator/requestcontrol"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/activator/runnable"
	runserver "github.com/llm-d/llm-d-inference-scheduler/pkg/activator/server"
)

var (
	grpcPort       = flag.Int("grpc-port", runserver.DefaultGrpcPort, "The gRPC port used for communicating with Envoy proxy")
	grpcHealthPort = flag.Int("grpc-health-port", runserver.DefaultGrpcHealthPort, "The port used for gRPC liveness and readiness probes")
	metricsPort    = flag.Int("metrics-port", runserver.DefaultMetricsPort, "The metrics port")
	poolName       = flag.String("pool-name", runserver.DefaultPoolName, "Name of the InferencePool this Endpoint Picker is associated with.")
	poolGroup      = flag.String("pool-group", runserver.DefaultPoolGroup, "group of the InferencePool this Endpoint Picker is associated with.")
	poolNamespace  = flag.String("pool-namespace", "", "Namespace of the InferencePool this Endpoint Picker is associated with.")
	logVerbosity   = flag.Int("v", logging.DEFAULT, "number for the log level verbosity")
	secureServing  = flag.Bool("secure-serving", runserver.DefaultSecureServing, "Enables secure serving. Defaults to true.")
	healthChecking = flag.Bool("health-checking", runserver.DefaultHealthChecking, "Enables health checking")
	certPath       = flag.String("cert-path", runserver.DefaultCertPath, "The path to the certificate for secure serving. The certificate and private key files "+
		"are assumed to be named tls.crt and tls.key, respectively. If not set, and secureServing is enabled, "+
		"then a self-signed certificate is used.")
	haEnableLeaderElection = flag.Bool("ha-enable-leader-election", false, "Enables leader election for high availability. When enabled, readiness probes will only pass on the leader.")

	setupLog = ctrl.Log.WithName("setup")
)

// Run starts the activator runner with the provided context, initializes logging,
// controllers, and servers, and blocks until the manager exits or the context is cancelled.
func Run(ctx context.Context) error {
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	initLogging(&opts)

	setupLog.Info("GIE build", "commit-sha", version.CommitSHA, "build-ref", version.BuildRef)

	// Validate flags
	if err := validateFlags(); err != nil {
		setupLog.Error(err, "Failed to validate flags")
		return err
	}

	// Print all flag values
	flags := make(map[string]any)
	flag.VisitAll(func(f *flag.Flag) {
		flags[f.Name] = f.Value
	})
	setupLog.Info("Flags processed", "flags", flags)

	// --- Get Kubernetes Config ---
	cfg, err := ctrl.GetConfig()
	if err != nil {
		setupLog.Error(err, "Failed to get Kubernetes rest config")
		return err
	}

	// --- Setup Datastore ---
	datastore := datastore.NewDatastore(ctx)

	// --- Setup Activator ---
	activator, err := requestcontrol.NewActivatorWithConfig(cfg, datastore)
	if err != nil {
		setupLog.Error(err, "Failed to setup Activator")
		return err
	}

	// --- Setup Deactivator ---
	deactivator, err := requestcontrol.DeactivatorWithConfig(cfg, &datastore)
	if err != nil {
		setupLog.Error(err, "Failed to setup Deactivator")
		return err
	}

	// Start Deactivator
	go deactivator.MonitorInferencePoolIdleness(ctx)

	// --- Setup Metrics Server ---
	metricsServerOptions := metricsserver.Options{
		BindAddress:    fmt.Sprintf(":%d", *metricsPort),
		FilterProvider: filters.WithAuthenticationAndAuthorization,
	}

	// Determine pool namespace: if --pool-namespace is non-empty, use it; else NAMESPACE env var; else default
	resolvePoolNamespace := func() string {
		if *poolNamespace != "" {
			return *poolNamespace
		}
		if nsEnv := os.Getenv("NAMESPACE"); nsEnv != "" {
			return nsEnv
		}
		return runserver.DefaultPoolNamespace
	}
	resolvedPoolNamespace := resolvePoolNamespace()
	poolNamespacedName := types.NamespacedName{
		Name:      *poolName,
		Namespace: resolvedPoolNamespace,
	}
	poolGroupKind := schema.GroupKind{
		Group: *poolGroup,
		Kind:  "InferencePool",
	}
	poolGKNN := common.GKNN{
		NamespacedName: poolNamespacedName,
		GroupKind:      poolGroupKind,
	}

	isLeader := &atomic.Bool{}
	isLeader.Store(false)

	mgr, err := runserver.NewDefaultManager(poolGKNN, cfg, metricsServerOptions, *haEnableLeaderElection)
	if err != nil {
		setupLog.Error(err, "Failed to create controller manager")
		return err
	}

	if *haEnableLeaderElection {
		setupLog.Info("Leader election enabled")
		go func() {
			<-mgr.Elected()
			isLeader.Store(true)
			setupLog.Info("This instance is now the leader!")
		}()
	} else {
		// If leader election is disabled, all instances are "leaders" for readiness purposes.
		isLeader.Store(true)
	}

	// --- Setup ExtProc Server Runner ---
	serverRunner := &runserver.ExtProcServerRunner{
		GrpcPort:           *grpcPort,
		PoolNamespacedName: poolNamespacedName,
		PoolGKNN:           poolGKNN,
		Datastore:          datastore,
		SecureServing:      *secureServing,
		HealthChecking:     *healthChecking,
		CertPath:           *certPath,
		Activator:          activator,
	}
	if err := serverRunner.SetupWithManager(ctx, mgr); err != nil {
		setupLog.Error(err, "Failed to setup Activator controllers")
		return err
	}

	// --- Add Runnables to Manager ---
	// Register health server.
	if err := registerHealthServer(mgr, ctrl.Log.WithName("health"), datastore, *grpcHealthPort, isLeader, *haEnableLeaderElection); err != nil {
		return err
	}

	// Register ext-proc server.
	if err := registerExtProcServer(mgr, serverRunner, ctrl.Log.WithName("ext-proc")); err != nil {
		return err
	}

	// --- Start Manager ---
	// This blocks until a signal is received.
	setupLog.Info("Controller manager starting")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Error starting controller manager")
		return err
	}
	setupLog.Info("Controller manager terminated")
	return nil
}

func initLogging(opts *zap.Options) {
	// Unless -zap-log-level is explicitly set, use -v
	useV := true
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "zap-log-level" {
			useV = false
		}
	})
	if useV {
		// See https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/log/zap#Options.Level
		lvl := -1 * (*logVerbosity)
		opts.Level = uberzap.NewAtomicLevelAt(zapcore.Level(int8(lvl)))
	}

	logger := zap.New(zap.UseFlagOptions(opts), zap.RawZapOpts(uberzap.AddCaller()))
	ctrl.SetLogger(logger)
}

// registerExtProcServer adds the ExtProcServerRunner as a Runnable to the manager.
func registerExtProcServer(mgr manager.Manager, runner *runserver.ExtProcServerRunner, logger logr.Logger) error {
	if err := mgr.Add(runner.AsRunnable(logger)); err != nil {
		setupLog.Error(err, "Failed to register ext-proc gRPC server runnable")
		return err
	}
	setupLog.Info("ExtProc server runner added to manager.")
	return nil
}

// registerHealthServer adds the Health gRPC server as a Runnable to the given manager.
func registerHealthServer(mgr manager.Manager, logger logr.Logger, ds datastore.Datastore, port int, isLeader *atomic.Bool, leaderElectionEnabled bool) error {
	srv := grpc.NewServer()
	healthPb.RegisterHealthServer(srv, &healthServer{
		logger:                logger,
		datastore:             ds,
		isLeader:              isLeader,
		leaderElectionEnabled: leaderElectionEnabled,
	})
	if err := mgr.Add(
		runnable.NoLeaderElection(runnable.GRPCServer("health", srv, port))); err != nil {
		setupLog.Error(err, "Failed to register health server")
		return err
	}
	return nil
}

func validateFlags() error {
	if *poolName == "" {
		return fmt.Errorf("required %q flag not set", "poolName")
	}

	return nil
}
