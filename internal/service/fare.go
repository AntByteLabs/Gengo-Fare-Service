package service

import (
	"fmt"
	"math"
	"time"

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
func ValidateRequest(req domain.FareRequest) error {
	switch req.VehicleType {
	case domain.VehicleBike, domain.VehicleCar, domain.VehicleElectric, domain.VehicleAuto:
		// valid
	default:
		return fmt.Errorf("vehicleType %q is not supported; must be one of bike, car, electric, auto", req.VehicleType)
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
func ComputeFare(req domain.FareRequest, now time.Time) domain.FareEstimate {
	rates := domain.VehicleRates[req.VehicleType]

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

	subtotal := rates.BaseFare + distanceFare + timeFare

	// --- Night surcharge (NPT = UTC+5:45) ---
	nptLoc := time.FixedZone("NPT", 5*3600+45*60)
	nptNow := now.In(nptLoc)
	hour := nptNow.Hour()

	var nightSurcharge int64
	if hour >= nightStartHour || hour < nightEndHour {
		nightSurcharge = int64(float64(subtotal) * nightSurchargeRate)
	}

	// --- Surge (currently static 1.0; kept as a hook for future dynamic surge) ---
	surgeMultiplier := 1.0
	surgedSubtotal := float64(subtotal+nightSurcharge) * surgeMultiplier
	surgeAmount := int64(surgedSubtotal) - (subtotal + nightSurcharge)

	// --- Minimum fare enforcement ---
	preBooking := int64(math.Max(float64(int64(surgedSubtotal)), float64(rates.MinFare)))
	total := preBooking + rates.BookingFee

	// --- Tax (13% VAT) ---
	tax := int64(math.Floor(float64(total) * taxRate))
	total = total + tax

	// --- Round to nearest 100 paisa ---
	total = roundNearest(total, roundTo)

	breakdown := domain.FareBreakdown{
		BaseFare:         rates.BaseFare,
		DistanceFare:     distanceFare,
		TimeFare:         timeFare,
		WaitingFare:      0,
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
