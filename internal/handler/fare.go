package handler

import (
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/gengo/fare-svc/internal/domain"
	"github.com/gengo/fare-svc/internal/middleware"
	"github.com/gengo/fare-svc/internal/service"
)

// successResponse wraps a FareEstimate in the standard API envelope.
type successResponse struct {
	Success bool                `json:"success"`
	Data    domain.FareEstimate `json:"data"`
	Meta    middleware.MetaBlock `json:"meta"`
}

// EstimateFare handles POST /v1/fare/estimate.
func EstimateFare(c *fiber.Ctx) error {
	var req domain.FareRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid JSON body: "+err.Error())
	}

	if err := service.ValidateRequest(req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, err.Error())
	}

	estimate := service.ComputeFare(req, time.Now())

	requestID, _ := c.Locals(middleware.RequestIDKey).(string)
	return c.Status(fiber.StatusOK).JSON(successResponse{
		Success: true,
		Data:    estimate,
		Meta: middleware.MetaBlock{
			RequestID: requestID,
			Ts:        time.Now().UnixMilli(),
		},
	})
}

// Health handles GET /health.
func Health(c *fiber.Ctx) error {
	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"status": "ok",
	})
}
