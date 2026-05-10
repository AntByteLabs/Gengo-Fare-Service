package middleware

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// ZapLogger emits one structured JSON log line per request. Mirrors the
// fields used by trip-svc's text logger but in zap's structured format so
// they're searchable downstream:
//
//	{"level":"info","ts":..., "msg":"http", "method":"POST",
//	 "path":"/v1/fare/estimate", "status":200, "latencyMs":3,
//	 "requestId":"01H..."}
func ZapLogger(log *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		latency := time.Since(start)

		status := c.Response().StatusCode()
		requestID, _ := c.Locals(RequestIDKey).(string)

		fields := []zap.Field{
			zap.String("method", c.Method()),
			zap.String("path", c.Path()),
			zap.Int("status", status),
			zap.Int64("latencyMs", latency.Milliseconds()),
			zap.String("requestId", requestID),
		}
		switch {
		case status >= 500:
			log.Error("http", fields...)
		case status >= 400:
			log.Warn("http", fields...)
		default:
			log.Info("http", fields...)
		}
		return err
	}
}
