// fare-svc is a stateless fare-quoting service. It sits behind the
// api-gateway and is reachable only from inside the cluster (api-gateway,
// trip-svc). It does not own a database and does not produce Kafka events.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/gengo/fare-svc/internal/config"
	"github.com/gengo/fare-svc/internal/handler"
	"github.com/gengo/fare-svc/internal/middleware"
	"github.com/gengo/fare-svc/internal/service"
)

func main() {
	// ---- Logger ----
	zapLog, err := buildLogger()
	if err != nil {
		log.Fatalf("failed to initialise zap logger: %v", err)
	}
	defer zapLog.Sync() //nolint:errcheck

	// ---- Config (fail-loud) ----
	cfg, err := loadConfig()
	if err != nil {
		zapLog.Fatal("invalid configuration", zap.Error(err))
	}
	zapLog.Info("fare-svc config resolved",
		zap.String("port", cfg.Port),
		zap.String("logLevel", cfg.LogLevel),
		zap.String("appConfigSvcURL", cfg.AppConfigSvcURL),
		zap.Bool("redisConfigured", cfg.RedisURL != ""),
		zap.Bool("intraClusterAuth", cfg.IntraAuthSecret != ""),
		zap.Int("bodyLimitBytes", cfg.BodyLimitBytes),
	)

	// ---- Fiber app ----
	app := fiber.New(fiber.Config{
		// Delegate all error handling to our structured handler.
		ErrorHandler: middleware.ErrorHandler,
		// Cap request bodies. Fare requests are small JSON objects; anything
		// larger is almost certainly abuse.
		BodyLimit: cfg.BodyLimitBytes,
		// Reasonable transport-level timeouts so a hung client cannot tie up
		// a worker.
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
		// Do not expose Go stack traces in error responses.
		EnablePrintRoutes: false,
		// Suppress the Fiber banner — it leaks the framework version and
		// process metadata to anyone with stdout access.
		DisableStartupMessage: true,
	})

	// ---- Global middleware ----
	app.Use(recover.New())
	app.Use(middleware.RequestID())
	app.Use(middleware.ZapLogger(zapLog))

	// ---- Live config (admin-tunable fare rates) ----
	// Cancellable context so the poller goroutine and pubsub subscriber
	// shut down with the process. Redis client is optional; without it the
	// config client falls back to TTL polling alone.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cfg.AppConfigSvcURL != "" {
		var rdb *redis.Client
		if cfg.RedisURL != "" {
			opts, err := redis.ParseURL(cfg.RedisURL)
			if err != nil {
				zapLog.Warn("config-client: invalid REDIS_URL; falling back to TTL-only", zap.Error(err))
			} else {
				rdb = redis.NewClient(opts)
				defer rdb.Close()
			}
		}
		client := config.NewClient(ctx, cfg.AppConfigSvcURL, 60*time.Second, rdb, func(format string, args ...any) {
			zapLog.Info("config-client " + fmt.Sprintf(format, args...))
		})
		service.SetConfigClient(client)
	} else {
		zapLog.Info("APP_CONFIG_SVC_URL not set; using bundled fare rates")
	}

	// ---- Routes ----
	// /health stays outside the version prefix and outside the intra-cluster
	// auth gate so liveness probes don't burn through anything.
	app.Get("/health", handler.Health)

	// Versioned routes are gated by the intra-cluster HMAC. Empty secret =>
	// no-op (local dev), and we logged a warning above.
	intraAuth := middleware.IntraClusterAuth(cfg.IntraAuthSecret)
	v1 := app.Group("/v1", intraAuth)
	fare := v1.Group("/fare")
	fare.Post("/estimate", handler.EstimateFare)

	// ---- Start + graceful shutdown ----
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		zapLog.Info("fare-svc starting", zap.String("port", cfg.Port))
		if err := app.Listen(":" + cfg.Port); err != nil {
			zapLog.Fatal("server error", zap.Error(err))
		}
	}()

	sig := <-sigCh
	zapLog.Info("signal received, shutting down", zap.String("signal", sig.String()))

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	// Cancel the long-lived context so the config-client poller exits.
	cancel()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		zapLog.Error("graceful shutdown error", zap.Error(err))
	}
	zapLog.Info("fare-svc stopped")
}

// ---- config ----

type appConfig struct {
	Port            string
	LogLevel        string
	AppConfigSvcURL string
	RedisURL        string
	IntraAuthSecret string // X-Internal-Auth HMAC secret
	BodyLimitBytes  int
}

// loadConfig reads env vars once at boot, applying defaults for optional
// fields and refusing to start when production-required vars are missing.
//
// "Production" is signalled by NODE_ENV=production / GO_ENV=production /
// APP_ENV=production — we accept any of those because the platform is
// polyglot. In dev we soft-warn instead of fatal so docker-compose works
// out of the box.
func loadConfig() (appConfig, error) {
	cfg := appConfig{
		Port:            getEnv("PORT", "3005"),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
		AppConfigSvcURL: os.Getenv("APP_CONFIG_SVC_URL"),
		RedisURL:        os.Getenv("REDIS_URL"),
		IntraAuthSecret: os.Getenv("INTRA_AUTH_SECRET"),
		BodyLimitBytes:  16 * 1024,
	}

	if v := os.Getenv("BODY_LIMIT_BYTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return cfg, fmt.Errorf("BODY_LIMIT_BYTES must be a positive integer, got %q", v)
		}
		cfg.BodyLimitBytes = n
	}

	if isProduction() {
		var missing []string
		if cfg.AppConfigSvcURL == "" {
			missing = append(missing, "APP_CONFIG_SVC_URL")
		}
		if cfg.RedisURL == "" {
			missing = append(missing, "REDIS_URL")
		}
		if cfg.IntraAuthSecret == "" {
			missing = append(missing, "INTRA_AUTH_SECRET")
		}
		if len(missing) > 0 {
			return cfg, fmt.Errorf("missing required env vars in production: %v", missing)
		}
	}

	return cfg, nil
}

func isProduction() bool {
	for _, k := range []string{"APP_ENV", "GO_ENV", "NODE_ENV"} {
		if v := os.Getenv(k); v == "production" || v == "prod" {
			return true
		}
	}
	return false
}

// buildLogger creates a zap logger whose level is controlled by the LOG_LEVEL
// environment variable (default: info).
func buildLogger() (*zap.Logger, error) {
	level := getEnv("LOG_LEVEL", "info")
	var cfg zap.Config
	if level == "debug" {
		cfg = zap.NewDevelopmentConfig()
	} else {
		cfg = zap.NewProductionConfig()
	}

	var zapLevel zap.AtomicLevel
	if err := zapLevel.UnmarshalText([]byte(level)); err == nil {
		cfg.Level = zapLevel
	}

	return cfg.Build()
}

// getEnv returns the value of the named environment variable, or fallback if
// the variable is not set or is empty.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
