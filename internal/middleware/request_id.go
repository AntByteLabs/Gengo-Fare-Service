package middleware

import (
	"math/rand"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/oklog/ulid/v2"
)

const RequestIDKey = "requestId"

// RequestID reads X-Request-Id from the incoming request. If it is absent or
// empty, a new ULID is generated. The resolved ID is stored in Locals and
// echoed back on the response header.
func RequestID() fiber.Handler {
	entropy := ulid.Monotonic(rand.New(rand.NewSource(time.Now().UnixNano())), 0) //nolint:gosec
	return func(c *fiber.Ctx) error {
		id := c.Get("X-Request-Id")
		if id == "" {
			id = ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
		}
		c.Locals(RequestIDKey, id)
		c.Set("X-Request-Id", id)
		return c.Next()
	}
}
