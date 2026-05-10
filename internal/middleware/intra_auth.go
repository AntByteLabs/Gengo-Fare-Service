package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"github.com/gofiber/fiber/v2"
)

// IntraClusterAuth gates fare-svc behind a shared HMAC header so it can only
// be reached from inside the cluster (api-gateway, trip-svc).
//
// fare-svc is intentionally stateless and exposes no public surface — every
// caller is another service. We mirror the simplest pattern compatible with
// the deployment shape: callers compute hex(HMAC-SHA256(secret, requestId))
// and send it as `X-Internal-Auth`. Verification is done with hmac.Equal so
// timing leaks are bounded.
//
// When `secret` is empty the middleware is a no-op (intended for local dev
// behind docker-compose, where the only callers are other services on the
// same network). The boot-time loadConfig() warns loudly in that case.
func IntraClusterAuth(secret string) fiber.Handler {
	if secret == "" {
		return func(c *fiber.Ctx) error { return c.Next() }
	}
	key := []byte(secret)
	return func(c *fiber.Ctx) error {
		// /health must remain reachable to liveness probes.
		if c.Path() == "/health" {
			return c.Next()
		}

		got := c.Get("X-Internal-Auth")
		if got == "" {
			return fiber.NewError(fiber.StatusUnauthorized, "missing X-Internal-Auth header")
		}
		gotBytes, err := hex.DecodeString(got)
		if err != nil {
			return fiber.NewError(fiber.StatusUnauthorized, "invalid X-Internal-Auth encoding")
		}

		requestID, _ := c.Locals(RequestIDKey).(string)
		if requestID == "" {
			// Should be impossible — RequestID middleware runs first — but
			// fail closed rather than authenticate an empty string.
			return fiber.NewError(fiber.StatusUnauthorized, "missing request id")
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(requestID))
		want := mac.Sum(nil)

		if !hmac.Equal(gotBytes, want) {
			return fiber.NewError(fiber.StatusUnauthorized, "X-Internal-Auth mismatch")
		}
		return c.Next()
	}
}
