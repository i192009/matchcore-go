package observability

import "github.com/prometheus/client_golang/prometheus"

// LatencySummary returns histogram upper-bound estimates in milliseconds.
// Empty histograms are omitted so the UI can distinguish no observations.
func LatencySummary() map[string]float64 {
	out := map[string]float64{}
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return out
	}
	for _, family := range families {
		prefix := ""
		switch family.GetName() {
		case "matchcore_matching_seconds":
			prefix = "matching"
		case "matchcore_command_seconds":
			prefix = "command"
		case "matchcore_commit_seconds":
			prefix = "commit"
		default:
			continue
		}
		for _, metric := range family.Metric {
			h := metric.GetHistogram()
			if h == nil || h.GetSampleCount() == 0 {
				continue
			}
			for name, q := range map[string]float64{"p50": .5, "p95": .95, "p99": .99} {
				target := uint64(float64(h.GetSampleCount())*q + .999999)
				for _, bucket := range h.Bucket {
					if bucket.GetCumulativeCount() >= target {
						out[prefix+"_"+name+"_ms"] = bucket.GetUpperBound() * 1000
						break
					}
				}
			}
		}
	}
	return out
}
