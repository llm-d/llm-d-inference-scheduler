package batchrunner

import (
	"context"
	"flag"
	"fmt"
	"net/http"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/batch"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/batch/redis"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/version"
	"github.com/prometheus/client_golang/prometheus"
	uberzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics"
	runserver "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/server"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

type BatchRunner struct {
	customCollectors []prometheus.Collector
}

var (
	setupLog            = ctrl.Log.WithName("setup")
	logVerbosity        = flag.Int("v", logging.DEFAULT, "number for the log level verbosity")
	concurrency         = flag.Int("concurrency", 8, "number of concurrent workers")
	endpoint            = flag.String("endpoint", "http://localhost:30080/v1/completions", "inference endpoint")
	inferenceObjective  = flag.String("inference-objective", "", "inference objective to use in requests")
	metricsPort         = flag.Int("metrics-port", runserver.DefaultMetricsPort, "The metrics port")
	metricsEndpointAuth = flag.Bool("metrics-endpoint-auth", true, "Enables authentication and authorization of the metrics endpoint")
	requestMergePolicy  = flag.String("request-merge-policy", "random-robin", "The request merge policy to use. Supported policies: random-robin")
	messageQueueImpl    = flag.String("message-queue-impl", "redis-pubsub", "The message queue implementation to use. Supported implementations: redis-pubsub")
)

func NewBatchRunner() *BatchRunner {
	return &BatchRunner{}
}

func (r *BatchRunner) Run(ctx context.Context) error {
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	initLogging(&opts)

	/*if *tracing {
		err := common.InitTracing(ctx, setupLog)
		if err != nil {
			return err
		}
	}*/

	////////setupLog.Info("GIE build", "commit-sha", version.CommitSHA, "build-ref", version.BuildRef)

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

	metrics.Register(r.customCollectors...)
	metrics.RecordInferenceExtensionInfo(version.CommitSHA, version.BuildRef)
	// Register metrics handler.
	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress: fmt.Sprintf(":%d", *metricsPort),
		FilterProvider: func() func(c *rest.Config, httpClient *http.Client) (metricsserver.Filter, error) {
			if *metricsEndpointAuth {
				return filters.WithAuthenticationAndAuthorization
			}

			return nil
		}(),
	}

	httpClient := &http.Client{
		// TODO: configure
	}

	msrv, _ := metricsserver.NewServer(metricsServerOptions, cfg, httpClient /* TODO: not sure about using the same one*/)
	go msrv.Start(ctx)

	var policy batch.RequestMergePolicy
	switch *requestMergePolicy {
	case "random-robin":
		policy = batch.NewRandomRobinPolicy()
	default:
		setupLog.Error(nil, "Unknown request merge policy", "request-merge-policy", *requestMergePolicy)
		return nil
	}

	var impl batch.Flow
	switch *messageQueueImpl {
	case "redis-pubsub":
		impl = redis.NewRedisMQFlow()
	default:
		setupLog.Error(nil, "Unknown message queue implementation", "message-queue-impl", *messageQueueImpl)
		return nil
	}

	requestChannel := policy.MergeRequestChannels(impl.RequestChannels()).Channel
	for w := 1; w <= *concurrency; w++ {
		go batch.Worker(ctx, *endpoint, *inferenceObjective, httpClient, requestChannel, impl.RetryChannel(), impl.ResultChannel())
	}

	impl.Start(ctx)
	<-ctx.Done()
	return nil
}

// TODO: is this dup of
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
func (r *BatchRunner) WithCustomCollectors(collectors ...prometheus.Collector) *BatchRunner {
	r.customCollectors = collectors
	return r
}
func validateFlags() error {

	return nil
}
