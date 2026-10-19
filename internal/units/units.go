// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package units formats metric values in their units, for the report's
// tables and for the notes that explain a verdict.
package units

import (
	"fmt"
	"math"

	"github.com/just-very-quickly/jvq/internal/adapter"
)

// Value formats a metric value in its unit, scaling times to ns, µs, ms or s.
func Value(metric, unit string, v float64) string {
	if metric == adapter.Time {
		return Duration(v)
	}
	switch metric {
	case adapter.Bytes:
		return trim(v) + " B"
	case adapter.Allocs:
		return trim(v)
	case adapter.Size:
		return Size(v)
	case adapter.Instructions:
		return Count(v)
	}
	return trim(v) + " " + unit
}

// Size formats a file size in bytes, in decimal units as file managers
// show them.
func Size(b float64) string {
	switch a := math.Abs(b); {
	case a >= 1e9:
		return fmt.Sprintf("%.4g GB", b/1e9)
	case a >= 1e6:
		return fmt.Sprintf("%.4g MB", b/1e6)
	case a >= 1e4:
		return fmt.Sprintf("%.4g kB", b/1e3)
	}
	return trim(b) + " B"
}

// Count formats a count, such as instructions, with k, M or G.
func Count(n float64) string {
	switch a := math.Abs(n); {
	case a >= 1e9:
		return fmt.Sprintf("%.4g G", n/1e9)
	case a >= 1e6:
		return fmt.Sprintf("%.4g M", n/1e6)
	case a >= 1e4:
		return fmt.Sprintf("%.4g k", n/1e3)
	}
	return trim(n)
}

// Duration formats nanoseconds.
func Duration(ns float64) string {
	switch a := math.Abs(ns); {
	case a >= 1e9:
		return trim3(ns/1e9) + " s"
	case a >= 1e6:
		return trim3(ns/1e6) + " ms"
	case a >= 1e3:
		return trim3(ns/1e3) + " µs"
	}
	return trim3(ns) + " ns"
}

func trim3(v float64) string { return fmt.Sprintf("%.3g", v) }

func trim(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.4g", v)
}
