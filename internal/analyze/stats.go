package analyze

import (
	"math"
	"sort"
)

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// quantile uses linear interpolation between order statistics (type 7, the
// default in R/NumPy) so small samples behave sensibly.
func quantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	pos := q * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return s[lo] + (s[hi]-s[lo])*(pos-float64(lo))
}

// modifiedZ returns the Iglewicz–Hoaglin modified z-score of every value:
// 0.6745·(x − median)/MAD. Unlike the mean/stddev z-score it is not dragged
// around by the very outliers we are trying to find, which matters for spend
// data where a handful of sessions dominate. Values above 3.5 are outliers.
// If MAD is zero (more than half the values identical) it falls back to the
// mean absolute deviation so the score stays defined.
func modifiedZ(xs []float64) []float64 {
	out := make([]float64, len(xs))
	if len(xs) == 0 {
		return out
	}
	m := median(xs)
	dev := make([]float64, len(xs))
	for i, x := range xs {
		dev[i] = math.Abs(x - m)
	}
	if mad := median(dev); mad > 0 {
		for i, x := range xs {
			out[i] = 0.6745 * (x - m) / mad
		}
		return out
	}
	var meanDev float64
	for _, d := range dev {
		meanDev += d
	}
	meanDev /= float64(len(xs))
	if meanDev == 0 {
		return out
	}
	for i, x := range xs {
		out[i] = (x - m) / (1.253314 * meanDev)
	}
	return out
}

// percentileRanks maps each value to its mid-rank percentile in [0,1]; ties
// share the average rank. This makes "how demanding is this session
// *relative to the rest of my own work*" independent of absolute units.
func percentileRanks(xs []float64) []float64 {
	n := len(xs)
	out := make([]float64, n)
	if n == 0 {
		return out
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return xs[idx[a]] < xs[idx[b]] })
	for i := 0; i < n; {
		j := i
		for j < n && xs[idx[j]] == xs[idx[i]] {
			j++
		}
		mid := (float64(i) + float64(j-1)) / 2
		for k := i; k < j; k++ {
			out[idx[k]] = (mid + 0.5) / float64(n)
		}
		i = j
	}
	return out
}

// ewma is an exponentially weighted moving average. alpha near 1 reacts
// quickly; near 0 smooths heavily. Recent days count more, so a one-off
// expensive day fades instead of permanently inflating the run-rate.
func ewma(xs []float64, alpha float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	v := xs[0]
	for _, x := range xs[1:] {
		v = alpha*x + (1-alpha)*v
	}
	return v
}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }
