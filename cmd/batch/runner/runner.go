package batchrunner

import (
	"context"
	"flag"
	"net/http"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/batch"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/batch/redis"
	uberzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

type BatchRunner struct {
}

var (
	setupLog     = ctrl.Log.WithName("setup")
	logVerbosity = flag.Int("v", logging.DEFAULT, "number for the log level verbosity")
	concurrency  = flag.Int("concurrency", 8, "number of concurrent workers")
	endpoint     = flag.String("endpoint", "http://localhost:30080/v1/completions", "inference endpoint")
	redisAddr    = flag.String("redis-addr", "localhost:16379", "address of the Redis server")
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

	httpClient := &http.Client{
		// TODO: configure
	}
	var policy batch.RequestPolicy = batch.NewRandomRobinPolicy()

	var impl batch.Flow = redis.NewRedisMQFlow(*redisAddr)
	requestChannel := policy.MergeRequestChannels(impl.RequestChannels()).Channel
	for w := 1; w <= *concurrency; w++ {
		go batch.Worker(ctx, *endpoint, httpClient, requestChannel, impl.RetryChannel(), impl.ResultChannel())
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

func validateFlags() error {

	return nil
}
