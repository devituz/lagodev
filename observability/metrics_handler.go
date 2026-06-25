package observability

import (
	"net/http"
	"strconv"
	"strings"
)

// MetricsHandler returns an http.Handler that renders reg in the Prometheus
// text exposition format (version 0.0.4). It depends only on the standard
// library; no Prometheus client is required.
//
// Counters and gauges are emitted as their respective TYPE; histograms are
// emitted as cumulative _bucket series (with a le="+Inf" bucket), plus _sum
// and _count. Series are written in a stable, deterministic order so the
// output is reproducible and diff-friendly.
//
//	mux.Handle("/metrics", observability.MetricsHandler(reg))
func MetricsHandler(reg *Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var b strings.Builder
		reg.writeProm(&b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(b.String()))
	})
}

// writeProm renders the whole registry into b. Families are written in
// insertion order; series within a family are written in insertion order.
func (r *Registry) writeProm(b *strings.Builder) {
	r.mu.RLock()
	order := make([]string, len(r.order))
	copy(order, r.order)
	families := make([]*metricFamily, 0, len(order))
	for _, name := range order {
		families = append(families, r.metrics[name])
	}
	r.mu.RUnlock()

	for _, f := range families {
		if f == nil {
			continue
		}
		f.writeProm(b)
	}
}

func (f *metricFamily) writeProm(b *strings.Builder) {
	f.mu.Lock()
	keys := make([]string, len(f.keys))
	copy(keys, f.keys)
	seriesList := make([]*series, 0, len(keys))
	for _, k := range keys {
		seriesList = append(seriesList, f.series[k])
	}
	f.mu.Unlock()

	switch f.kind {
	case kindCounter:
		b.WriteString("# TYPE ")
		b.WriteString(f.name)
		b.WriteString(" counter\n")
		for _, s := range seriesList {
			writeSample(b, f.name, s.labels, nil, s.c.Value())
		}
	case kindGauge:
		b.WriteString("# TYPE ")
		b.WriteString(f.name)
		b.WriteString(" gauge\n")
		for _, s := range seriesList {
			writeSample(b, f.name, s.labels, nil, s.g.Value())
		}
	case kindHistogram:
		b.WriteString("# TYPE ")
		b.WriteString(f.name)
		b.WriteString(" histogram\n")
		for _, s := range seriesList {
			snap := s.h.Snapshot()
			// Cumulative bucket series.
			for i, ub := range snap.Buckets {
				le := extraLabel{name: "le", value: formatFloat(ub)}
				writeSample(b, f.name+"_bucket", s.labels, &le, float64(snap.Counts[i]))
			}
			// +Inf bucket equals the total count.
			le := extraLabel{name: "le", value: "+Inf"}
			writeSample(b, f.name+"_bucket", s.labels, &le, float64(snap.Count))
			writeSample(b, f.name+"_sum", s.labels, nil, snap.Sum)
			writeSample(b, f.name+"_count", s.labels, nil, float64(snap.Count))
		}
	}
}

// extraLabel is an additional label appended after the series labels (used
// for the histogram le bound).
type extraLabel struct {
	name  string
	value string
}

// writeSample writes one Prometheus sample line:
//
//	name{label="value",...} value
func writeSample(b *strings.Builder, name string, ls labelSet, extra *extraLabel, value float64) {
	b.WriteString(name)
	writeLabels(b, ls, extra)
	b.WriteByte(' ')
	b.WriteString(formatFloat(value))
	b.WriteByte('\n')
}

func writeLabels(b *strings.Builder, ls labelSet, extra *extraLabel) {
	if len(ls.names) == 0 && extra == nil {
		return
	}
	b.WriteByte('{')
	first := true
	for i := range ls.names {
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteString(ls.names[i])
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(ls.values[i]))
		b.WriteByte('"')
	}
	if extra != nil {
		if !first {
			b.WriteByte(',')
		}
		b.WriteString(extra.name)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(extra.value))
		b.WriteByte('"')
	}
	b.WriteByte('}')
}

// escapeLabelValue escapes backslash, double-quote and newline per the
// Prometheus exposition format spec.
func escapeLabelValue(v string) string {
	if !strings.ContainsAny(v, "\\\"\n") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}

// formatFloat renders a float64 the way Prometheus expects: shortest exact
// decimal, with +Inf/-Inf/NaN spelled out.
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}
