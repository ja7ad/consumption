package types

import "fmt"

// Bytes is a uint64 wrapper representing a size in bytes.
type Bytes uint64

// Humanized returns a human-readable string with automatic unit (B, KB, MB, GB, TB).
func (b Bytes) Humanized() string {
	const unit = 1024
	v := float64(b)
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.2f TB", v/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GB", v/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.2f MB", v/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.2f KB", v/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// KB returns the number of kilobytes (1024 base).
func (b Bytes) KB() float64 { return float64(b) / 1024 }

// MB returns the number of megabytes (1024 base).
func (b Bytes) MB() float64 { return float64(b) / (1024 * 1024) }

// GB returns the number of gigabytes (1024 base).
func (b Bytes) GB() float64 { return float64(b) / (1024 * 1024 * 1024) }
