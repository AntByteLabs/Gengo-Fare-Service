package service

import (
	"testing"
	"time"

	"github.com/gengo/fare-svc/internal/domain"
)

// nptLoc mirrors the NPT (UTC+5:45) zone used inside ComputeFare. Building
// `now` directly in this zone means t.Hour() in NPT == the hour we pass in,
// giving fully deterministic control over the night-surcharge branch.
var nptLoc = time.FixedZone("NPT", 5*3600+45*60)

// dayNPT returns a fixed instant whose NPT hour is 12:00 (clearly daytime,
// so no night surcharge applies).
func dayNPT() time.Time {
	return time.Date(2025, time.January, 1, 12, 0, 0, 0, nptLoc)
}

// atNPTHour returns a fixed instant at the given NPT hour (minute 0).
func atNPTHour(h int) time.Time {
	return time.Date(2025, time.January, 1, h, 0, 0, 0, nptLoc)
}

// TestComputeFare_BasicByVehicle verifies a standard 5km / 10min daytime trip
// for each bundled vehicle type, asserting exact paisa on every deterministic
// line item and the rounded total. Values are hand-derived from the formula:
//
//	distanceFare = (km-1) * perKm
//	timeFare     = min     * perMin
//	subtotal     = base + distanceFare + timeFare
//	preBooking   = max(subtotal, minFare)   (surge=1.0, day => night=0)
//	total        = preBooking + bookingFee
//	tax          = floor(total * 0.13)
//	total        = round100(total + tax)
func TestComputeFare_BasicByVehicle(t *testing.T) {
	const distanceM = int64(5000) // 5 km -> 4 chargeable km
	const durationS = int64(600)  // 10 min

	tests := []struct {
		name             string
		vehicle          domain.VehicleType
		wantBaseFare     int64
		wantDistanceFare int64
		wantTimeFare     int64
		wantBookingFee   int64
		wantTax          int64
		wantTotal        int64
	}{
		{
			name:             "bike",
			vehicle:          domain.VehicleBike,
			wantBaseFare:     5000,
			wantDistanceFare: 8000, // 4km * 2000
			wantTimeFare:     1000, // 10min * 100
			wantBookingFee:   500,
			wantTax:          1885, // floor(14500 * 0.13)
			wantTotal:        16400,
		},
		{
			name:             "car",
			vehicle:          domain.VehicleCar,
			wantBaseFare:     8000,
			wantDistanceFare: 16000, // 4km * 4000
			wantTimeFare:     1500,  // 10min * 150
			wantBookingFee:   1000,
			wantTax:          3445, // floor(26500 * 0.13)
			wantTotal:        29900,
		},
		{
			name:             "electric",
			vehicle:          domain.VehicleElectric,
			wantBaseFare:     6000,
			wantDistanceFare: 10000, // 4km * 2500
			wantTimeFare:     1200,  // 10min * 120
			wantBookingFee:   500,
			wantTax:          2301, // floor(17700 * 0.13)
			wantTotal:        20000,
		},
		{
			name:             "auto",
			vehicle:          domain.VehicleAuto,
			wantBaseFare:     4000,
			wantDistanceFare: 7200, // 4km * 1800
			wantTimeFare:     800,  // 10min * 80
			wantBookingFee:   300,
			wantTax:          1599, // floor(12300 * 0.13)
			wantTotal:        13900,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := domain.FareRequest{
				VehicleType: tc.vehicle,
				DistanceM:   distanceM,
				DurationS:   durationS,
			}
			est := ComputeFare(req, dayNPT())

			b := est.FareBreakdown
			if b.BaseFare != tc.wantBaseFare {
				t.Errorf("BaseFare = %d, want %d", b.BaseFare, tc.wantBaseFare)
			}
			if b.DistanceFare != tc.wantDistanceFare {
				t.Errorf("DistanceFare = %d, want %d", b.DistanceFare, tc.wantDistanceFare)
			}
			if b.TimeFare != tc.wantTimeFare {
				t.Errorf("TimeFare = %d, want %d", b.TimeFare, tc.wantTimeFare)
			}
			if b.BookingFee != tc.wantBookingFee {
				t.Errorf("BookingFee = %d, want %d", b.BookingFee, tc.wantBookingFee)
			}
			if b.Tax != tc.wantTax {
				t.Errorf("Tax = %d, want %d", b.Tax, tc.wantTax)
			}
			if b.NightSurcharge != 0 {
				t.Errorf("NightSurcharge = %d, want 0 (daytime)", b.NightSurcharge)
			}
			if b.Total != tc.wantTotal {
				t.Errorf("breakdown.Total = %d, want %d", b.Total, tc.wantTotal)
			}
			if est.FarePaisa != tc.wantTotal {
				t.Errorf("FarePaisa = %d, want %d", est.FarePaisa, tc.wantTotal)
			}
			// Echoed request fields.
			if est.DistanceM != distanceM {
				t.Errorf("DistanceM = %d, want %d", est.DistanceM, distanceM)
			}
			if est.DurationS != durationS {
				t.Errorf("DurationS = %d, want %d", est.DurationS, durationS)
			}
		})
	}
}

// TestComputeFare_FreeFirstKm pins the "first 1 km is free" rule by asserting
// the DistanceFare line item directly across the threshold. Bike rate is
// 2000 paisa/km after the free km.
func TestComputeFare_FreeFirstKm(t *testing.T) {
	tests := []struct {
		name             string
		distanceM        int64
		wantDistanceFare int64
	}{
		{name: "under 1km is free", distanceM: 500, wantDistanceFare: 0},
		{name: "just under 1km is free", distanceM: 999, wantDistanceFare: 0},
		{name: "exactly 1km is free", distanceM: 1000, wantDistanceFare: 0},
		{name: "1.5km charges half km", distanceM: 1500, wantDistanceFare: 1000}, // 0.5 * 2000
		{name: "2km charges one km", distanceM: 2000, wantDistanceFare: 2000},    // 1.0 * 2000
		{name: "5km charges four km", distanceM: 5000, wantDistanceFare: 8000},   // 4.0 * 2000
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := domain.FareRequest{
				VehicleType: domain.VehicleBike,
				DistanceM:   tc.distanceM,
				DurationS:   600,
			}
			est := ComputeFare(req, dayNPT())
			if got := est.FareBreakdown.DistanceFare; got != tc.wantDistanceFare {
				t.Errorf("DistanceFare for %dm = %d, want %d", tc.distanceM, got, tc.wantDistanceFare)
			}
		})
	}
}

// TestComputeFare_MinimumFareFloor verifies that a tiny trip is lifted to the
// per-vehicle minimum fare before booking fee and tax are applied.
//
// Bike: subtotal = base 5000 + dist 0 + time 100 = 5100, which is below the
// 10000 minimum. preBooking floors to 10000, +500 booking = 10500,
// tax floor(10500*0.13)=1365, total 11865 -> round100 = 11900.
func TestComputeFare_MinimumFareFloor(t *testing.T) {
	req := domain.FareRequest{
		VehicleType: domain.VehicleBike,
		DistanceM:   500, // free km, no distance fare
		DurationS:   60,  // 1 min -> 100 time fare
	}
	est := ComputeFare(req, dayNPT())

	if got := est.FareBreakdown.DistanceFare; got != 0 {
		t.Errorf("DistanceFare = %d, want 0", got)
	}
	if got := est.FareBreakdown.TimeFare; got != 100 {
		t.Errorf("TimeFare = %d, want 100", got)
	}
	// Tax is computed on the floored amount (10000+500=10500), proving the
	// floor kicked in: floor(10500*0.13)=1365. If the floor had NOT applied,
	// tax would be floor((5100+500)*0.13)=728.
	if got := est.FareBreakdown.Tax; got != 1365 {
		t.Errorf("Tax = %d, want 1365 (floor on min-fare base)", got)
	}
	const wantTotal = int64(11900)
	if est.FarePaisa != wantTotal {
		t.Errorf("FarePaisa = %d, want %d", est.FarePaisa, wantTotal)
	}
}

// TestComputeFare_BookingFeeAdded confirms the booking fee is the raw
// per-vehicle fee (sits above the min-fare floor, below tax) and is echoed in
// the breakdown for each vehicle.
func TestComputeFare_BookingFeeAdded(t *testing.T) {
	for vt, rate := range domain.VehicleRates {
		vt, rate := vt, rate
		t.Run(string(vt), func(t *testing.T) {
			req := domain.FareRequest{
				VehicleType: vt,
				DistanceM:   5000,
				DurationS:   600,
			}
			est := ComputeFare(req, dayNPT())
			if got := est.FareBreakdown.BookingFee; got != rate.BookingFee {
				t.Errorf("BookingFee = %d, want %d", got, rate.BookingFee)
			}
		})
	}
}

// TestComputeFare_NightSurcharge exercises the night-surcharge branch (NPT
// 22:00-06:00) deterministically via the injected clock. The 20% surcharge is
// computed on the subtotal (base+dist+time), here 14000 -> 2800.
//
// Bike day total = 16400 (see basic test). Night recomputes:
// subtotal 14000 + night 2800 = 16800, +500 booking = 17300,
// tax floor(17300*0.13)=2249, total 19549 -> round100 = 19500.
func TestComputeFare_NightSurcharge(t *testing.T) {
	req := domain.FareRequest{
		VehicleType: domain.VehicleBike,
		DistanceM:   5000,
		DurationS:   600,
	}

	t.Run("daytime no surcharge", func(t *testing.T) {
		est := ComputeFare(req, dayNPT())
		if got := est.FareBreakdown.NightSurcharge; got != 0 {
			t.Errorf("NightSurcharge = %d, want 0", got)
		}
		if est.FarePaisa != 16400 {
			t.Errorf("FarePaisa = %d, want 16400", est.FarePaisa)
		}
	})

	t.Run("night applies 20pct of subtotal", func(t *testing.T) {
		est := ComputeFare(req, atNPTHour(23))
		if got := est.FareBreakdown.NightSurcharge; got != 2800 {
			t.Errorf("NightSurcharge = %d, want 2800", got)
		}
		if est.FarePaisa != 19500 {
			t.Errorf("FarePaisa = %d, want 19500", est.FarePaisa)
		}
	})
}

// TestComputeFare_NightWindowBoundaries pins the exact hours the surcharge
// turns on/off: active for hour >= 22 OR hour < 6.
func TestComputeFare_NightWindowBoundaries(t *testing.T) {
	req := domain.FareRequest{
		VehicleType: domain.VehicleBike,
		DistanceM:   5000,
		DurationS:   600,
	}
	tests := []struct {
		hour      int
		wantNight bool
	}{
		{hour: 0, wantNight: true},
		{hour: 5, wantNight: true},
		{hour: 6, wantNight: false}, // window ends at 06:00
		{hour: 12, wantNight: false},
		{hour: 21, wantNight: false},
		{hour: 22, wantNight: true}, // window starts at 22:00
		{hour: 23, wantNight: true},
	}
	for _, tc := range tests {
		t.Run(time.Date(0, 1, 1, tc.hour, 0, 0, 0, nptLoc).Format("15h"), func(t *testing.T) {
			est := ComputeFare(req, atNPTHour(tc.hour))
			gotNight := est.FareBreakdown.NightSurcharge > 0
			if gotNight != tc.wantNight {
				t.Errorf("NPT hour %d: night surcharge present = %v (amount %d), want %v",
					tc.hour, gotNight, est.FareBreakdown.NightSurcharge, tc.wantNight)
			}
		})
	}
}

// TestComputeFare_SurgeIsNoOp documents that the static 1.0 surge multiplier
// neither scales the fare nor adds a surge line item.
func TestComputeFare_SurgeIsNoOp(t *testing.T) {
	req := domain.FareRequest{
		VehicleType: domain.VehicleCar,
		DistanceM:   5000,
		DurationS:   600,
	}
	est := ComputeFare(req, dayNPT())
	if est.SurgeMultiplier != 1.0 {
		t.Errorf("SurgeMultiplier = %v, want 1.0", est.SurgeMultiplier)
	}
	if est.FareBreakdown.Surge != 0 {
		t.Errorf("Surge line item = %d, want 0 (multiplier is 1.0)", est.FareBreakdown.Surge)
	}
}

// TestComputeFare_UnknownVehicleFallsBackToBike confirms an admin-added,
// non-bundled vehicle id is priced with bike defaults rather than zeros (with
// no config snapshot loaded in the test).
func TestComputeFare_UnknownVehicleFallsBackToBike(t *testing.T) {
	req := domain.FareRequest{
		VehicleType: domain.VehicleType("scooter"),
		DistanceM:   5000,
		DurationS:   600,
	}
	bikeReq := req
	bikeReq.VehicleType = domain.VehicleBike

	got := ComputeFare(req, dayNPT())
	wantBike := ComputeFare(bikeReq, dayNPT())

	if got.FarePaisa != wantBike.FarePaisa {
		t.Errorf("unknown vehicle FarePaisa = %d, want bike default %d", got.FarePaisa, wantBike.FarePaisa)
	}
	if got.FareBreakdown.BaseFare != domain.VehicleRates[domain.VehicleBike].BaseFare {
		t.Errorf("BaseFare = %d, want bike default %d",
			got.FareBreakdown.BaseFare, domain.VehicleRates[domain.VehicleBike].BaseFare)
	}
	if got.FarePaisa == 0 {
		t.Error("unknown vehicle priced to zero; fallback failed")
	}
}

// TestComputeFare_PickupLegRequiresConfig documents that the pickup-leg charge
// stays zero without a loaded config snapshot, even when a driver-pickup
// distance is supplied. (PickupFareNPRPerKm lives only in the admin snapshot;
// no snapshot is loaded in unit tests, so this path is intentionally not
// priced here. Full pickup-leg pricing would require injecting a config
// client, which we do not do to avoid touching production wiring.)
func TestComputeFare_PickupLegRequiresConfig(t *testing.T) {
	req := domain.FareRequest{
		VehicleType:           domain.VehicleBike,
		DistanceM:             5000,
		DurationS:             600,
		DriverPickupDistanceM: 3000,
	}
	est := ComputeFare(req, dayNPT())
	if got := est.FareBreakdown.PickupLegFare; got != 0 {
		t.Errorf("PickupLegFare = %d, want 0 (no config snapshot loaded)", got)
	}
	// Total must match the no-pickup basic bike fare.
	if est.FarePaisa != 16400 {
		t.Errorf("FarePaisa = %d, want 16400", est.FarePaisa)
	}
}

// TestValidateRequest covers the cheap input guards.
func TestValidateRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     domain.FareRequest
		wantErr bool
	}{
		{name: "valid", req: domain.FareRequest{VehicleType: domain.VehicleBike, DistanceM: 1000, DurationS: 60}},
		{name: "empty vehicle type", req: domain.FareRequest{DistanceM: 1000}, wantErr: true},
		{name: "negative distance", req: domain.FareRequest{VehicleType: domain.VehicleBike, DistanceM: -1}, wantErr: true},
		{name: "negative duration", req: domain.FareRequest{VehicleType: domain.VehicleBike, DurationS: -1}, wantErr: true},
		{name: "zero distance and duration allowed", req: domain.FareRequest{VehicleType: domain.VehicleBike}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRequest(tc.req)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateRequest() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
