package vllm

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// promSnapshot is a parsed Prometheus /metrics text exposition limited to
// the field shapes vLLM emits: gauges, counters, and histograms. We do not
// support summaries (vLLM doesn't use them for the metrics we care about).
type promSnapshot struct {
	gauges     map[string][]labeledSample
	counters   map[string][]labeledSample
	histograms map[string][]labeledHistogram
}

type labeledSample struct {
	labels labelSet
	value  float64
}

type labeledHistogram struct {
	labels  labelSet
	buckets []bucket // sorted by upper, +Inf last
	sum     float64
	count   float64
}

type bucket struct {
	upper float64 // le="..." threshold; math.Inf(1) for +Inf
	count float64 // cumulative observations <= upper
}

// labelSet is an ordered slice of (k,v) pairs. Comparisons treat two label
// sets as equal when every requested key matches — order-insensitive on
// the lookup side.
type labelSet [][2]string

func (l labelSet) matches(want map[string]string) bool {
	for k, v := range want {
		found := false
		for _, kv := range l {
			if kv[0] == k && kv[1] == v {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// parsePrometheus consumes a Prometheus text-format dump and returns a
// snapshot. Lines starting with `#` are ignored except for `# TYPE` (which
// is used to tell histograms from plain gauges/counters).
func parsePrometheus(text string) *promSnapshot {
	s := &promSnapshot{
		gauges:     map[string][]labeledSample{},
		counters:   map[string][]labeledSample{},
		histograms: map[string][]labeledHistogram{},
	}
	types := map[string]string{}

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				types[parts[2]] = parts[3]
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		metric, labels, value, ok := parseSampleLine(line)
		if !ok {
			continue
		}

		// Histogram base names are metric minus _bucket/_sum/_count suffix.
		base := metric
		var role string
		switch {
		case strings.HasSuffix(metric, "_bucket"):
			base = strings.TrimSuffix(metric, "_bucket")
			role = "bucket"
		case strings.HasSuffix(metric, "_sum"):
			base = strings.TrimSuffix(metric, "_sum")
			role = "sum"
		case strings.HasSuffix(metric, "_count"):
			base = strings.TrimSuffix(metric, "_count")
			role = "count"
		}

		typ := types[base]
		if typ == "" {
			typ = types[metric]
		}

		if typ == "histogram" && role != "" {
			s.appendHistogram(base, role, labels, value)
			continue
		}
		switch typ {
		case "counter":
			s.counters[metric] = append(s.counters[metric], labeledSample{labels: stripBucketLE(labels), value: value})
		case "gauge", "":
			s.gauges[metric] = append(s.gauges[metric], labeledSample{labels: labels, value: value})
		}
	}
	for _, hs := range s.histograms {
		for i := range hs {
			sort.Slice(hs[i].buckets, func(a, b int) bool {
				return hs[i].buckets[a].upper < hs[i].buckets[b].upper
			})
		}
	}
	return s
}

func stripBucketLE(l labelSet) labelSet {
	out := make(labelSet, 0, len(l))
	for _, kv := range l {
		if kv[0] == "le" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// appendHistogram folds bucket/sum/count lines into the histograms map.
func (s *promSnapshot) appendHistogram(base, role string, labels labelSet, value float64) {
	// Look up or create the labeled histogram for this label set (excluding
	// "le", which only matters for bucket lines).
	key := stripBucketLE(labels)
	hs := s.histograms[base]
	idx := -1
	for i := range hs {
		if labelSetsEqual(hs[i].labels, key) {
			idx = i
			break
		}
	}
	if idx < 0 {
		hs = append(hs, labeledHistogram{labels: key})
		idx = len(hs) - 1
	}
	switch role {
	case "bucket":
		var upper float64
		for _, kv := range labels {
			if kv[0] == "le" {
				if kv[1] == "+Inf" {
					upper = math.Inf(1)
				} else {
					f, err := strconv.ParseFloat(kv[1], 64)
					if err == nil {
						upper = f
					}
				}
				break
			}
		}
		hs[idx].buckets = append(hs[idx].buckets, bucket{upper: upper, count: value})
	case "sum":
		hs[idx].sum = value
	case "count":
		hs[idx].count = value
	}
	s.histograms[base] = hs
}

func labelSetsEqual(a, b labelSet) bool {
	if len(a) != len(b) {
		return false
	}
	// Order-insensitive
	for _, kv := range a {
		found := false
		for _, kv2 := range b {
			if kv[0] == kv2[0] && kv[1] == kv2[1] {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// parseSampleLine splits a Prometheus sample line into (name, labels, value).
// Format: `metric{k="v",k2="v2"} 1.234` or `metric 1.234`.
func parseSampleLine(line string) (string, labelSet, float64, bool) {
	// Trim trailing timestamp if present (Prometheus permits one).
	// We don't use it; just drop after the value.
	var name string
	var labels labelSet
	var rest string

	if i := strings.Index(line, "{"); i >= 0 {
		name = strings.TrimSpace(line[:i])
		j := strings.Index(line, "}")
		if j < 0 || j < i {
			return "", nil, 0, false
		}
		labels = parseLabelSet(line[i+1 : j])
		rest = strings.TrimSpace(line[j+1:])
	} else {
		// No labels — split on whitespace.
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return "", nil, 0, false
		}
		name = parts[0]
		rest = strings.Join(parts[1:], " ")
	}

	// rest is "<value> [timestamp]".
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return "", nil, 0, false
	}
	v, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		// "Nan", "+Inf", "-Inf" handled by ParseFloat already; anything else is bogus.
		return "", nil, 0, false
	}
	return name, labels, v, true
}

func parseLabelSet(s string) labelSet {
	// Split on commas, respecting that values are quoted and may contain commas.
	// vLLM doesn't put commas inside values, but be defensive.
	var out labelSet
	for len(s) > 0 {
		// key
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		key := strings.TrimSpace(s[:eq])
		s = s[eq+1:]
		// value (quoted)
		s = strings.TrimLeft(s, " \t")
		if !strings.HasPrefix(s, `"`) {
			break
		}
		s = s[1:]
		end := -1
		for i := 0; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if s[i] == '"' {
				end = i
				break
			}
		}
		if end < 0 {
			break
		}
		val := s[:end]
		s = s[end+1:]
		out = append(out, [2]string{key, val})
		s = strings.TrimLeft(s, ", \t")
	}
	return out
}

// gauge returns the value of the named gauge metric with all of the given
// labels. Returns (0, false) when not found.
func (s *promSnapshot) gauge(name string, labels map[string]string) (float64, bool) {
	for _, ls := range s.gauges[name] {
		if ls.labels.matches(labels) {
			return ls.value, true
		}
	}
	return 0, false
}

// counter returns the cumulative value of the named counter.
func (s *promSnapshot) counter(name string, labels map[string]string) (float64, bool) {
	for _, ls := range s.counters[name] {
		if ls.labels.matches(labels) {
			return ls.value, true
		}
	}
	return 0, false
}

// histogramQuantile returns the value at the given quantile (0..1) using
// linear interpolation across bucket edges. Same algorithm as Prometheus
// histogram_quantile(). Returns (0, false) when the histogram is absent
// or has zero observations.
func (s *promSnapshot) histogramQuantile(name string, labels map[string]string, q float64) (float64, bool) {
	for _, h := range s.histograms[name] {
		if !h.labels.matches(labels) {
			continue
		}
		if len(h.buckets) == 0 || h.count <= 0 {
			return 0, false
		}
		target := q * h.count
		var prevUpper float64
		var prevCount float64
		for _, b := range h.buckets {
			if b.count >= target {
				if math.IsInf(b.upper, 1) {
					return prevUpper, true
				}
				width := b.upper - prevUpper
				inBucket := b.count - prevCount
				if inBucket <= 0 {
					return b.upper, true
				}
				return prevUpper + width*((target-prevCount)/inBucket), true
			}
			prevUpper = b.upper
			prevCount = b.count
		}
		return prevUpper, true
	}
	return 0, false
}
