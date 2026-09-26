package agent

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// MinProbeInterval is the floor for --probe-every / TANZHEN_PROBE_INTERVAL.
// Below this, rounds overlap and the numbers stop being comparable.
const MinProbeInterval = 10 * time.Second

// DefaultProbeInterval / DefaultProbeCount match historical agent defaults.
const (
	DefaultProbeInterval = 30 * time.Second
	DefaultProbeCount    = 4
)

// ProbeDisabled reports whether env asks the agent to skip 三网 probes entirely
// (zero probe traffic). Accepted truths: 1, true, yes, on (case-insensitive).
func ProbeDisabled() bool {
	return envTruthy("TANZHEN_PROBE_DISABLE")
}

// LoadTargets builds CT/CU/CM targets from the current process environment:
// TANZHEN_PROBE_PROVINCES (comma-separated codes) and optional full overrides
// TANZHEN_PROBE_HOSTS_{CT,CU,CM}. Empty provinces → the built-in representative set.
func LoadTargets() []ProbeTarget {
	return defaultProbeTargets()
}

// selectedProvinces returns the province codes used to build CDN hostnames.
// Invalid / empty tokens are dropped; an empty result falls back to defaults.
func selectedProvinces() []string {
	v := strings.TrimSpace(os.Getenv("TANZHEN_PROBE_PROVINCES"))
	if v == "" {
		return append([]string(nil), defaultProvinces...)
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] || !validProvinceCode(p) {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		return append([]string(nil), defaultProvinces...)
	}
	return out
}

func validProvinceCode(p string) bool {
	for _, c := range p {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

func envTruthy(key string) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// ProbeIntervalFromEnv reads TANZHEN_PROBE_INTERVAL, falling back to the
// legacy TANZHEN_PROBE_EVERY name. Empty / invalid → def.
func ProbeIntervalFromEnv(def time.Duration) time.Duration {
	for _, key := range []string{"TANZHEN_PROBE_INTERVAL", "TANZHEN_PROBE_EVERY"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			d, err := time.ParseDuration(v)
			if err == nil && d > 0 {
				return d
			}
		}
	}
	return def
}

// ProbeCountFromEnv reads TANZHEN_PROBE_COUNT. Empty / invalid → def.
func ProbeCountFromEnv(def int) int {
	v := strings.TrimSpace(os.Getenv("TANZHEN_PROBE_COUNT"))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return def
	}
	return n
}

// ClampProbeInterval enforces MinProbeInterval (unless disabled / zero).
func ClampProbeInterval(d time.Duration) time.Duration {
	if d > 0 && d < MinProbeInterval {
		return MinProbeInterval
	}
	return d
}
