// Package config talks to app-config-svc and exposes the fare-relevant
// pieces (per-vehicle rates, global min fare) to the rest of fare-svc.
//
// Refresh strategy:
//   - TTL polling on a background goroutine (safety net heartbeat).
//   - Optional Redis pubsub subscription on `app:config:bumped` for
//     sub-second invalidation when an admin writes new config.
//
// Each refresh is best-effort; failures keep the previous snapshot in
// place so the service stays available even when app-config-svc is
// degraded.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// MUST match REDIS_CHANNEL in
// services/app-config-svc/src/services/config.service.ts.
const bumpChannel = "app:config:bumped"

// VehicleRateOverride captures the subset of fare math that admins can tune
// from /v1/admin/app-config. Pointers (vs plain int64) so we can tell
// "admin deliberately set 0" from "admin didn't touch this field" — a
// previous version used `> 0` guards which silently ignored explicit zero
// values. Fields are NPR; callers convert to paisa as needed.
type VehicleRateOverride struct {
	BaseFareNPR     *int64
	RatePerKm       *int64
	RatePerMinute   *int64
	MinFareNPR      *int64
	BookingFeePaisa *int64
}

// Snapshot is an immutable view of the config at the time it was fetched.
// Callers receive a pointer to one and treat it as read-only; writes happen
// only inside the Client when a new poll succeeds.
type Snapshot struct {
	Version              int
	MinFareNPR           int64
	BookingFeePaisa      int64
	TaxRatePercent       float64
	NightSurchargePaisa  int64
	AirportSurchargePaisa int64
	// Pickup-leg charge. PickupFareNPRPerKm is in NPR/km (multiply by km to
	// get NPR, then ×100 for paisa). PickupFreeKm is the allowance below
	// which no pickup fee is charged.
	PickupFareNPRPerKm   float64
	PickupFreeKm         float64
	VehicleRates         map[string]VehicleRateOverride
	FetchedAt            time.Time
}

// Client polls app-config-svc on a fixed interval and caches the most-recent
// successful snapshot. Methods are goroutine-safe; the Snapshot they return
// is shared but immutable.
type Client struct {
	baseURL  string
	interval time.Duration
	http     *http.Client
	logger   func(format string, args ...any)

	mu      sync.RWMutex
	current *Snapshot
}

// NewClient builds a polling client. The first refresh runs synchronously so
// the caller has a snapshot in hand before serving traffic; subsequent
// refreshes happen in a background goroutine until ctx is cancelled.
//
// When `rdb` is non-nil the client also subscribes to Redis pubsub
// `app:config:bumped` for instant invalidation on admin writes.
func NewClient(
	ctx context.Context,
	baseURL string,
	interval time.Duration,
	rdb *redis.Client,
	logger func(string, ...any),
) *Client {
	if logger == nil {
		logger = func(string, ...any) {}
	}
	c := &Client{
		baseURL:  baseURL,
		interval: interval,
		http:     &http.Client{Timeout: 5 * time.Second},
		logger:   logger,
	}
	// Initial sync refresh — best-effort. If it fails, the rest of the
	// process keeps booting with an empty snapshot and the fare service
	// falls back to bundled defaults.
	if err := c.refresh(ctx); err != nil {
		logger("config-client: initial refresh failed: %v", err)
	}
	go c.loop(ctx)
	if rdb != nil {
		go c.subscribe(ctx, rdb)
	}
	return c
}

// subscribe listens for `app:config:bumped` messages and refreshes on each
// one. Reconnects with exponential backoff if the subscription fails.
func (c *Client) subscribe(ctx context.Context, rdb *redis.Client) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		sub := rdb.Subscribe(ctx, bumpChannel)
		// go-redis reconnects internally; the outer for-loop only runs again
		// on a definitive Channel close. Reset backoff so a single transient
		// failure doesn't leave us at 30s forever after recovery.
		backoff = time.Second
		ch := sub.Channel()
		c.logger("config-client: subscribed to %s", bumpChannel)
		for msg := range ch {
			_ = msg
			if err := c.refresh(ctx); err != nil {
				c.logger("config-client: pubsub-triggered refresh failed: %v", err)
			}
		}
		_ = sub.Close()
		if ctx.Err() != nil {
			return
		}
		c.logger("config-client: pubsub channel closed; reconnecting in %v", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// Snapshot returns the most recent successful poll. Returns nil before the
// first successful refresh — callers must handle the nil case (typically by
// falling back to bundled defaults).
func (c *Client) Snapshot() *Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.current
}

func (c *Client) loop(ctx context.Context) {
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.refresh(ctx); err != nil {
				c.logger("config-client: refresh failed: %v", err)
			}
		}
	}
}

func (c *Client) refresh(ctx context.Context) error {
	url := c.baseURL + "/v1/app-config"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("app-config-svc returned %d: %s", resp.StatusCode, string(body))
	}

	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Version int `json:"version"`
			Fares   struct {
				MinFareNPR            int64   `json:"minFareNPR"`
				BookingFeePaisa       int64   `json:"bookingFeePaisa"`
				TaxRatePercent        float64 `json:"taxRatePercent"`
				NightSurchargePaisa   int64   `json:"nightSurchargePaisa"`
				AirportSurchargePaisa int64   `json:"airportSurchargePaisa"`
				PickupFareNPRPerKm    float64 `json:"pickupFareNPRPerKm"`
				PickupFreeKm          float64 `json:"pickupFreeKm"`
				Vehicles              []struct {
					ID              string `json:"id"`
					// Pointers so absent JSON keys decode to nil — admin
					// patches one field at a time and we mustn't treat a
					// missing key as "set to 0".
					BaseFareNPR     *int64 `json:"baseFareNPR"`
					RatePerKm       *int64 `json:"ratePerKm"`
					RatePerMinute   *int64 `json:"ratePerMinute"`
					MinFareNPR      *int64 `json:"minFareNPR"`
					BookingFeePaisa *int64 `json:"bookingFeePaisa"`
				} `json:"vehicles"`
			} `json:"fares"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if !envelope.Success {
		return errors.New("app-config-svc returned success=false")
	}

	rates := make(map[string]VehicleRateOverride, len(envelope.Data.Fares.Vehicles))
	for _, v := range envelope.Data.Fares.Vehicles {
		rates[v.ID] = VehicleRateOverride{
			BaseFareNPR:     v.BaseFareNPR,
			RatePerKm:       v.RatePerKm,
			RatePerMinute:   v.RatePerMinute,
			MinFareNPR:      v.MinFareNPR,
			BookingFeePaisa: v.BookingFeePaisa,
		}
	}
	// Range-loop variable v already a struct copy; we keep the pointer
	// fields straight through. The map's value type is VehicleRateOverride
	// whose pointer fields point into envelope's heap-allocated decoded
	// values — those outlive this function via the snapshot we store below.

	snap := &Snapshot{
		Version:               envelope.Data.Version,
		MinFareNPR:            envelope.Data.Fares.MinFareNPR,
		BookingFeePaisa:       envelope.Data.Fares.BookingFeePaisa,
		TaxRatePercent:        envelope.Data.Fares.TaxRatePercent,
		NightSurchargePaisa:   envelope.Data.Fares.NightSurchargePaisa,
		AirportSurchargePaisa: envelope.Data.Fares.AirportSurchargePaisa,
		PickupFareNPRPerKm:    envelope.Data.Fares.PickupFareNPRPerKm,
		PickupFreeKm:          envelope.Data.Fares.PickupFreeKm,
		VehicleRates:          rates,
		FetchedAt:             time.Now(),
	}
	c.mu.Lock()
	prev := c.current
	// Short-circuit equal versions so future change-listeners aren't woken
	// for a no-op (TTL tick after a pubsub bump fetches the same version).
	if prev != nil && prev.Version == snap.Version {
		c.mu.Unlock()
		return nil
	}
	c.current = snap
	c.mu.Unlock()
	c.logger("config-client: refreshed to v%d (%d vehicles)", snap.Version, len(rates))
	return nil
}
