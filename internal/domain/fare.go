package domain

// VehicleType represents the type of ride vehicle.
type VehicleType string

const (
	VehicleBike     VehicleType = "bike"
	VehicleCar      VehicleType = "car"
	VehicleElectric VehicleType = "electric"
	VehicleAuto     VehicleType = "auto"
)

// VehicleRate holds the pricing parameters for a vehicle type. All monetary
// values are in paisa (1 NPR = 100 paisa).
type VehicleRate struct {
	BaseFare    int64 // paisa
	PerKmRate   int64 // paisa per km (charged after first free km)
	PerMinRate  int64 // paisa per minute
	MinFare     int64 // minimum fare in paisa (before booking fee)
	BookingFee  int64 // flat booking fee in paisa
}

// VehicleRates is the canonical rate table for all vehicle types.
var VehicleRates = map[VehicleType]VehicleRate{
	VehicleBike: {
		BaseFare:   5000,
		PerKmRate:  2000,
		PerMinRate: 100,
		MinFare:    10000,
		BookingFee: 500,
	},
	VehicleCar: {
		BaseFare:   8000,
		PerKmRate:  4000,
		PerMinRate: 150,
		MinFare:    20000,
		BookingFee: 1000,
	},
	VehicleElectric: {
		BaseFare:   6000,
		PerKmRate:  2500,
		PerMinRate: 120,
		MinFare:    15000,
		BookingFee: 500,
	},
	VehicleAuto: {
		BaseFare:   4000,
		PerKmRate:  1800,
		PerMinRate: 80,
		MinFare:    8000,
		BookingFee: 300,
	},
}

// LatLng is a geographic coordinate pair.
type LatLng struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// FareRequest is the inbound payload for fare estimation.
type FareRequest struct {
	VehicleType VehicleType `json:"vehicleType"`
	Pickup      LatLng      `json:"pickup"`
	Dropoff     LatLng      `json:"dropoff"`
	// DistanceM is optional. When zero, Haversine × 1.3 is used.
	DistanceM int64 `json:"distanceM"`
	// DurationS is optional. When zero, a default estimate is derived.
	DurationS int64 `json:"durationS"`
	// DriverPickupDistanceM is the deadhead distance the driver rides
	// alone to reach the pickup. Optional — zero at request-ride time
	// (no driver matched yet). When > 0, the fare gains a pickup-leg
	// line item; the proceeds are paid to the driver.
	DriverPickupDistanceM int64 `json:"driverPickupDistanceM,omitempty"`
}

// FareBreakdown contains every line-item of the fare computation.
// All values are in paisa.
type FareBreakdown struct {
	BaseFare         int64 `json:"baseFare"`
	DistanceFare     int64 `json:"distanceFare"`
	TimeFare         int64 `json:"timeFare"`
	WaitingFare      int64 `json:"waitingFare"`
	/** Pickup-leg charge — driver's deadhead km to the customer/parcel.
	 *  Zero when no driver-pickup distance was supplied. Goes 100% to driver. */
	PickupLegFare    int64 `json:"pickupLegFare"`
	Surge            int64 `json:"surge"`
	NightSurcharge   int64 `json:"nightSurcharge"`
	AirportSurcharge int64 `json:"airportSurcharge"`
	Voucher          int64 `json:"voucher"`
	Tax              int64 `json:"tax"`
	BookingFee       int64 `json:"bookingFee"`
	Total            int64 `json:"total"`
}

// FareEstimate is the outbound data payload returned from ComputeFare.
type FareEstimate struct {
	VehicleType     VehicleType   `json:"vehicleType"`
	DistanceM       int64         `json:"distanceM"`
	DurationS       int64         `json:"durationS"`
	FarePaisa       int64         `json:"farePaisa"`
	SurgeMultiplier float64       `json:"surgeMultiplier"`
	FareBreakdown   FareBreakdown `json:"fareBreakdown"`
}
