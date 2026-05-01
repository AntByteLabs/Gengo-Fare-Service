package main

import (
	"log"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"go.uber.org/zap"

	"github.com/gengo/fare-svc/internal/handler"
	"github.com/gengo/fare-svc/internal/middleware"
)

func main() {
	// ---- Logger ----
	zapLog, err := buildLogger()
	if err != nil {
		log.Fatalf("failed to initialise zap logger: %v", err)
	}
	defer zapLog.Sync() //nolint:errcheck

	// ---- Fiber app ----
	app := fiber.New(fiber.Config{
		// Delegate all error handling to our structured handler.
		ErrorHandler: middleware.ErrorHandler,
		// Do not expose Go stack traces in error responses.
		EnablePrintRoutes: false,
	})

	// ---- Global middleware ----
	app.Use(recover.New())
	app.Use(middleware.RequestID())
	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} - ${latency} ${method} ${path} rid=${locals:requestId}\n",
	}))

	// ---- Routes ----
	app.Get("/health", handler.Health)

	v1 := app.Group("/v1")
	fare := v1.Group("/fare")
	fare.Post("/estimate", handler.EstimateFare)

	// ---- Start ----
	port := getEnv("PORT", "3005")
	zapLog.Info("fare-svc starting", zap.String("port", port))
	if err := app.Listen(":" + port); err != nil {
		zapLog.Fatal("server error", zap.Error(err))
	}
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
