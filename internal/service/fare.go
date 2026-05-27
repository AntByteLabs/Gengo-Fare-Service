package service

import (
	"fmt"
	"math"
	"time"

	"github.com/gengo/fare-svc/internal/config"
	"github.com/gengo/fare-svc/internal/domain"
)

const (
	// taxRate is Nepal's VAT rate (13%).
	taxRate = 0.13

	// roadFactor converts straight-line (Haversine) distance to estimated road
	// distance.
	roadFactor = 1.3

	// earthRadiusM is the mean radius of Earth in metres.
	earthRadiusM = 6_371_000.0

	// nightStartHour and nightEndHour define NPT night hours (22:00–06:00).
	nightStartHour = 22
	nightEndHour   = 6

	// nightSurchargeRate is applied to the subtotal during night hours.
	nightSurchargeRate = 0.20

	// roundTo is the rounding granularity in paisa.
	roundTo = 100
)

// ValidateRequest checks that the FareRequest fields are sensible before any
// computation is performed. It returns a descriptive error on the first
// violated constraint.
//
// VehicleType is intentionally loose: any non-empty lowercase id is
// accepted. Unknown ids fall back to bike defaults in resolveRates, which
// keeps the fare service forward-compatible with admin-added vehicles
// (e.g. "scooter", "premium", "xl") without redeploying.
func ValidateRequest(req domain.FareRequest) error {
	if req.VehicleType == "" {
		return fmt.Errorf("vehicleType must not be empty")
	}

	if req.DistanceM < 0 {
		return fmt.Errorf("distanceM must be >= 0")
	}
	if req.DurationS < 0 {
		return fmt.Errorf("durationS must be >= 0")
	}

	return nil
}

// ComputeFare computes a complete fare estimate for the given request.
// now is accepted as a parameter so night-surcharge logic is fully testable
// without mocking time.Now.
//
// Admin-configured overrides (from app-config-svc) take precedence over the
// bundled defaults: every BaseFare / PerKm / PerMin / MinFare value can be
// adjusted live without a deploy. When the snapshot is missing (e.g. the
// service just started and the first poll hasn't landed), we fall through
// to domain.VehicleRates so fares are never zero.
func ComputeFare(req domain.FareRequest, now time.Time) domain.FareEstimate {
	rates := resolveRates(req.VehicleType)

	// --- Distance ---
	distanceM := req.DistanceM
	if distanceM <= 0 {
		distanceM = int64(haversineM(req.Pickup, req.Dropoff) * roadFactor)
	}
	distanceKm := float64(distanceM) / 1000.0

	// --- Duration ---
	durationS := req.DurationS
	if durationS <= 0 {
		// No duration supplied; derive a rough estimate: assume 20 km/h avg speed.
		durationS = int64((distanceKm / 20.0) * 3600)
	}
	durationMin := float64(durationS) / 60.0

	// --- Component fares ---
	// distanceFare: free first 1 km
	freeKm := 1.0
	chargeableKm := math.Max(0, distanceKm-freeKm)
	distanceFare := int64(chargeableKm * float64(rates.PerKmRate))
	timeFare := int64(durationMin * float64(rates.PerMinRate))

	// Pickup-leg fare — driver's deadhead km. Computed only when the
	// caller supplied a non-zero DriverPickupDistanceM (typically once
	// a driver is matched and we know their position). At rider-side
	// estimate time this is zero, so the line stays out of the breakdown.
	var pickupLegFare int64
	if req.DriverPickupDistanceM > 0 {
		if snap := configSnapshot(); snap != nil && snap.PickupFareNPRPerKm > 0 {
			pickupKm := float64(req.DriverPickupDistanceM) / 1000.0
			chargeablePickupKm := math.Max(0, pickupKm-snap.PickupFreeKm)
			pickupLegFare = int64(chargeablePickupKm * snap.PickupFareNPRPerKm * 100)
		}
	}

	subtotal := rates.BaseFare + distanceFare + timeFare + pickupLegFare

	// --- Night surcharge (NPT = UTC+5:45) ---
	nptLoc := time.FixedZone("NPT", 5*3600+45*60)
	nptNow := now.In(nptLoc)
	hour := nptNow.Hour()

	var nightSurcharge int64
	if hour >= nightStartHour || hour < nightEndHour {
		// Admin-configured flat amount (paisa) trumps the bundled 20%
		// multiplier when set. Lets ops apply a fixed "after-hours" fee
		// without rebuilding the service.
		if snap := configSnapshot(); snap != nil && snap.NightSurchargePaisa > 0 {
			nightSurcharge = snap.NightSurchargePaisa
		} else {
			nightSurcharge = int64(float64(subtotal) * nightSurchargeRate)
		}
	}

	// --- Surge (currently static 1.0; kept as a hook for future dynamic surge) ---
	surgeMultiplier := 1.0
	surgedSubtotal := float64(subtotal+nightSurcharge) * surgeMultiplier
	surgeAmount := int64(surgedSubtotal) - (subtotal + nightSurcharge)

	// --- Minimum fare enforcement ---
	preBooking := int64(math.Max(float64(int64(surgedSubtotal)), float64(rates.MinFare)))
	total := preBooking + rates.BookingFee

	// --- Tax — admin can override the 13% default per market ---
	effectiveTaxRate := taxRate
	if snap := configSnapshot(); snap != nil && snap.TaxRatePercent > 0 {
		effectiveTaxRate = snap.TaxRatePercent / 100
	}
	tax := int64(math.Floor(float64(total) * effectiveTaxRate))
	total = total + tax

	// --- Round to nearest 100 paisa ---
	total = roundNearest(total, roundTo)

	breakdown := domain.FareBreakdown{
		BaseFare:         rates.BaseFare,
		DistanceFare:     distanceFare,
		TimeFare:         timeFare,
		WaitingFare:      0,
		PickupLegFare:    pickupLegFare,
		Surge:            surgeAmount,
		NightSurcharge:   nightSurcharge,
		AirportSurcharge: 0,
		Voucher:          0,
		Tax:              tax,
		BookingFee:       rates.BookingFee,
		Total:            total,
	}

	return domain.FareEstimate{
		VehicleType:     req.VehicleType,
		DistanceM:       distanceM,
		DurationS:       durationS,
		FarePaisa:       total,
		SurgeMultiplier: surgeMultiplier,
		FareBreakdown:   breakdown,
	}
}

// configSnapshot is a small accessor for the admin-configured snapshot so
// surcharge / tax code can read fields without re-implementing the nil
// guards every time.
func configSnapshot() *config.Snapshot {
	c := configClientHolder.Load()
	if c == nil {
		return nil
	}
	return c.Snapshot()
}

// resolveRates merges the admin-configured snapshot over the bundled
// defaults. The snapshot is read via the package-level configClientHolder
// (populated in main.go); when it's nil — or doesn't have an entry for
// this vehicle — we return the default unchanged. We DO NOT mutate the
// default map.
//
// For unknown vehicle ids (admin added a new vehicle like "scooter" that
// isn't in the bundled domain.VehicleRates map), we fall back to the
// bike default rate so the fare is never computed against an all-zero
// rate row. If the admin snapshot carries an override for the unknown
// id, those overrides still apply on top.
func resolveRates(vt domain.VehicleType) domain.VehicleRate {
	defaults, hasDefaults := domain.VehicleRates[vt]
	if !hasDefaults {
		// Unknown id (e.g. admin-added "scooter"). Seed with bike defaults
		// so the rate row has sensible non-zero values before any admin
		// overrides are layered on.
		defaults = domain.VehicleRates[domain.VehicleBike]
	}
	c := configClientHolder.Load()
	if c == nil {
		return defaults
	}
	snap := c.Snapshot()
	if snap == nil {
		return defaults
	}
	override, ok := snap.VehicleRates[string(vt)]
	if !ok {
		return defaults
	}
	out := defaults
	// All overrides are in NPR; convert to paisa. Zero means "not set by
	// admin" so we keep the default (the AppConfig JSON Schema would never
	// emit a 0 for a positive-valued field anyway, but be defensive).
	// Nil pointer = admin didn't touch this field → keep default.
	// Non-nil pointer = admin explicitly set the value, INCLUDING zero.
	// This is the only way "perMinute = 0" (no time charge) can survive.
	if override.BaseFareNPR != nil {
		out.BaseFare = *override.BaseFareNPR * 100
	}
	if override.RatePerKm != nil {
		out.PerKmRate = *override.RatePerKm * 100
	}
	if override.RatePerMinute != nil {
		out.PerMinRate = *override.RatePerMinute * 100
	}
	// Per-vehicle minimum wins over global. Global wins over bundled default.
	if override.MinFareNPR != nil {
		out.MinFare = *override.MinFareNPR * 100
	} else if snap.MinFareNPR > 0 {
		out.MinFare = snap.MinFareNPR * 100
	}
	// Booking fee — per-vehicle override (paisa, already), then global, then default.
	if override.BookingFeePaisa != nil {
		out.BookingFee = *override.BookingFeePaisa
	} else if snap.BookingFeePaisa > 0 {
		out.BookingFee = snap.BookingFeePaisa
	}
	return out
}

// haversineM returns the great-circle distance in metres between two points.
func haversineM(a, b domain.LatLng) float64 {
	lat1 := toRad(a.Lat)
	lat2 := toRad(b.Lat)
	dLat := toRad(b.Lat - a.Lat)
	dLng := toRad(b.Lng - a.Lng)

	sinDLat := math.Sin(dLat / 2)
	sinDLng := math.Sin(dLng / 2)

	h := sinDLat*sinDLat + math.Cos(lat1)*math.Cos(lat2)*sinDLng*sinDLng
	c := 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
	return earthRadiusM * c
}

func toRad(deg float64) float64 {
	return deg * math.Pi / 180
}

// roundNearest rounds v to the nearest multiple of nearest.
func roundNearest(v, nearest int64) int64 {
	if nearest <= 0 {
		return v
	}
	half := nearest / 2
	return ((v + half) / nearest) * nearest
}
