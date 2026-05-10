package service

import (
	"sync/atomic"

	"github.com/gengo/fare-svc/internal/config"
)

var configClientHolder atomic.Pointer[config.Client]

func SetConfigClient(c *config.Client) {
	configClientHolder.Store(c)
}
