package consumption

// Config holds model coefficients.
// Units:
//   - PIdle/PMax: Watts
//   - Gamma: dimensionless (CPU nonlinearity)
//   - ER/EW: Joules per byte (disk read/write)
//   - EMemRef/EMemRSS: Joules per byte (RAM proxies)
//   - Alpha: fraction of idle to charge to process share [0..1]
type Config struct {
	PIdle   float64
	PMax    float64
	Gamma   float64
	ER      float64
	EW      float64
	EMemRef float64
	EMemRSS float64
	Alpha   float64
}

// _defaultConfig returns a Config pre-filled with reasonable default coefficients.
// These are the same values you used in your shell experiments.
func _defaultConfig() *Config {
	return &Config{
		PIdle:   5.0,    // W at idle
		PMax:    20.0,   // W at full utilization
		Gamma:   1.3,    // CPU curve exponent
		ER:      4.8e-8, // J/byte disk read
		EW:      9.5e-8, // J/byte disk write
		EMemRef: 7e-10,  // J/byte refault (RAM proxy, v2 only)
		EMemRSS: 3e-10,  // J/byte RSS churn
		Alpha:   0.0,    // fraction of idle to distribute
	}
}

// Result is the instantaneous power breakdown for one snapshot.
type Result struct {
	PCPU   float64 // W
	PDisk  float64 // W
	PRAM   float64 // W
	PTotal float64 // W
}
