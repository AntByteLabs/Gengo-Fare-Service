package middleware

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
)

// ErrorResponse is the envelope returned for all error responses.
type ErrorResponse struct {
	Success bool      `json:"success"`
	Error   ErrDetail `json:"error"`
	Meta    MetaBlock `json:"meta"`
}

// ErrDetail holds the machine-readable code and human-readable message.
type ErrDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MetaBlock is shared by both success and error envelopes.
type MetaBlock struct {
	RequestID string `json:"requestId"`
	Ts        int64  `json:"ts"`
}

// ErrorHandler is the Fiber-level error handler. It converts *fiber.Error and
// any other error into a structured JSON response.
func ErrorHandler(c *fiber.Ctx, err error) error {
	requestID, _ := c.Locals(RequestIDKey).(string)
	meta := MetaBlock{
		RequestID: requestID,
		Ts:        time.Now().UnixMilli(),
	}

	var fe *fiber.Error
	if errors.As(err, &fe) {
		code := httpStatusToCode(fe.Code)
		return c.Status(fe.Code).JSON(ErrorResponse{
			Success: false,
			Error:   ErrDetail{Code: code, Message: fe.Message},
			Meta:    meta,
		})
	}

	// Fallback: internal server error.
	return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
		Success: false,
		Error:   ErrDetail{Code: "INTERNAL_ERROR", Message: err.Error()},
		Meta:    meta,
	})
}

func httpStatusToCode(status int) string {
	switch status {
	case fiber.StatusBadRequest:
		return "BAD_REQUEST"
	case fiber.StatusUnprocessableEntity:
		return "VALIDATION_ERROR"
	case fiber.StatusNotFound:
		return "NOT_FOUND"
	case fiber.StatusMethodNotAllowed:
		return "METHOD_NOT_ALLOWED"
	default:
		return "INTERNAL_ERROR"
	}
}
