// Package metrics provides a small, thread-safe metrics registry that renders
// the Prometheus text exposition format (version 0.0.4) using only the
// standard library.
//
// The zero Registry is an empty registry ready to use. Register a metric once
// at start-up, then mutate it from any goroutine and scrape it with
// Registry.WriteTo.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	typeCounter   = "counter"
	typeGauge     = "gauge"
	typeHistogram = "histogram"

	leLabel  = "le"
	infValue = "+Inf"
)

// Registry holds metric families and renders them for scraping. It is safe for
// concurrent use by multiple goroutines.
type Registry struct {
	mu       sync.RWMutex
	families map[string]family
}

// family is a single metric family that can render itself in the text
// exposition format.
type family interface {
	familyName() string
	writeTo(w *expositionWriter)
}

// Counter is a monotonically increasing metric. The unlabeled sample used by
// Inc and Add only exists when the counter declares no labels.
type Counter struct {
	name    string
	help    string
	labels  []string
	mu      sync.RWMutex
	samples map[string]*CounterSample
}

// CounterSample is a Counter bound to a fixed set of label values.
type CounterSample struct {
	parent *Counter
	values []string
	value  float64
}

// Gauge is a metric that can go up and down. The unlabeled sample used by Set,
// Inc and Dec only exists when the gauge declares no labels.
type Gauge struct {
	name    string
	help    string
	labels  []string
	mu      sync.RWMutex
	samples map[string]*GaugeSample
}

// GaugeSample is a Gauge bound to a fixed set of label values.
type GaugeSample struct {
	parent *Gauge
	values []string
	value  float64
}

// Histogram counts observations into a fixed set of buckets. Buckets are
// sorted ascending when the histogram is created; a copy of the caller's
// slice is taken so the caller's slice is never modified.
type Histogram struct {
	name    string
	help    string
	labels  []string
	bounds  []float64
	mu      sync.RWMutex
	samples map[string]*HistogramSample
}

// HistogramSample is a Histogram bound to a fixed set of label values.
type HistogramSample struct {
	parent *Histogram
	values []string
	counts []uint64
	sum    float64
	count  uint64
}

// Counter registers and returns a counter family. Label values passed to With
// are matched to labels in declaration order. It panics if a metric with the
// same name is already registered.
func (r *Registry) Counter(name, help string, labels ...string) *Counter {
	counter := newCounter(name, help, labels)
	r.register(counter)
	return counter
}

// Gauge registers and returns a gauge family. Label values passed to With are
// matched to labels in declaration order. It panics if a metric with the same
// name is already registered.
func (r *Registry) Gauge(name, help string, labels ...string) *Gauge {
	gauge := newGauge(name, help, labels)
	r.register(gauge)
	return gauge
}

// Histogram registers and returns a histogram family. The buckets slice is
// copied and sorted ascending. Label values passed to With are matched to
// labels in declaration order. It panics if a metric with the same name is
// already registered.
func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) *Histogram {
	histogram := newHistogram(name, help, buckets, labels)
	r.register(histogram)
	return histogram
}

// WriteTo renders every registered metric family in the Prometheus text
// exposition format and returns the number of bytes written. Families and the
// samples inside them are ordered by name and by label value respectively, so
// two consecutive calls over an unchanged registry produce identical bytes.
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	r.mu.RLock()
	families := make([]family, 0, len(r.families))
	for _, registered := range r.families {
		families = append(families, registered)
	}
	r.mu.RUnlock()

	sort.Slice(families, func(i, j int) bool {
		return families[i].familyName() < families[j].familyName()
	})

	exposition := &expositionWriter{w: w}
	for _, registered := range families {
		registered.writeTo(exposition)
	}
	return exposition.n, exposition.err
}

func (r *Registry) register(registered family) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.families[registered.familyName()]; exists {
		panic(fmt.Sprintf("metrics: metric %q is already registered", registered.familyName()))
	}
	if r.families == nil {
		r.families = make(map[string]family)
	}
	r.families[registered.familyName()] = registered
}

func newCounter(name, help string, labels []string) *Counter {
	counter := &Counter{
		name:    name,
		help:    help,
		labels:  append([]string(nil), labels...),
		samples: make(map[string]*CounterSample),
	}
	if len(counter.labels) == 0 {
		counter.samples[sampleKey(nil)] = &CounterSample{parent: counter}
	}
	return counter
}

// Inc adds one to the counter. It panics if the counter declares labels, since
// an unlabeled sample cannot be identified.
func (c *Counter) Inc() {
	c.Add(1)
}

// Add adds delta to the counter. It panics if the counter declares labels,
// since an unlabeled sample cannot be identified.
func (c *Counter) Add(delta float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unlabeledLocked().value += delta
}

// With returns the counter sample for the given label values, creating it on
// first use. It panics if the number of label values does not match the number
// of declared label names.
func (c *Counter) With(labelValues ...string) *CounterSample {
	checkLabelValues(typeCounter, c.name, len(c.labels), labelValues)
	return sampleFor(&c.mu, c.samples, labelValues, func() *CounterSample {
		return &CounterSample{parent: c, values: append([]string(nil), labelValues...)}
	})
}

func (c *Counter) familyName() string {
	return c.name
}

func (c *Counter) writeTo(w *expositionWriter) {
	w.writeHelp(c.name, c.help)
	w.writeType(c.name, typeCounter)

	c.mu.RLock()
	lines := make([]counterLine, 0, len(c.samples))
	for _, sample := range c.samples {
		lines = append(lines, counterLine{values: sample.values, value: sample.value})
	}
	c.mu.RUnlock()

	sortByLabels(lines, func(line counterLine) []string { return line.values })
	for _, line := range lines {
		w.writeSample(c.name, renderLabels(c.labels, line.values, "", ""), formatValue(line.value))
	}
}

func (c *Counter) unlabeledLocked() *CounterSample {
	if len(c.labels) > 0 {
		panic(fmt.Sprintf("metrics: counter %q declares %d label name(s); use With to bind label values", c.name, len(c.labels)))
	}
	return c.samples[sampleKey(nil)]
}

// Inc adds one to the counter sample.
func (s *CounterSample) Inc() {
	s.Add(1)
}

// Add adds delta to the counter sample.
func (s *CounterSample) Add(delta float64) {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()
	s.value += delta
}

func newGauge(name, help string, labels []string) *Gauge {
	gauge := &Gauge{
		name:    name,
		help:    help,
		labels:  append([]string(nil), labels...),
		samples: make(map[string]*GaugeSample),
	}
	if len(gauge.labels) == 0 {
		gauge.samples[sampleKey(nil)] = &GaugeSample{parent: gauge}
	}
	return gauge
}

// Set replaces the gauge value. It panics if the gauge declares labels, since
// an unlabeled sample cannot be identified.
func (g *Gauge) Set(value float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.unlabeledLocked().value = value
}

// Inc increases the gauge by one. It panics if the gauge declares labels,
// since an unlabeled sample cannot be identified.
func (g *Gauge) Inc() {
	g.Add(1)
}

// Dec decreases the gauge by one. It panics if the gauge declares labels,
// since an unlabeled sample cannot be identified.
func (g *Gauge) Dec() {
	g.Add(-1)
}

// Add changes the gauge by delta. It panics if the gauge declares labels, since
// an unlabeled sample cannot be identified.
func (g *Gauge) Add(delta float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.unlabeledLocked().value += delta
}

// With returns the gauge sample for the given label values, creating it on
// first use. It panics if the number of label values does not match the number
// of declared label names.
func (g *Gauge) With(labelValues ...string) *GaugeSample {
	checkLabelValues(typeGauge, g.name, len(g.labels), labelValues)
	return sampleFor(&g.mu, g.samples, labelValues, func() *GaugeSample {
		return &GaugeSample{parent: g, values: append([]string(nil), labelValues...)}
	})
}

func (g *Gauge) familyName() string {
	return g.name
}

func (g *Gauge) writeTo(w *expositionWriter) {
	w.writeHelp(g.name, g.help)
	w.writeType(g.name, typeGauge)

	g.mu.RLock()
	lines := make([]gaugeLine, 0, len(g.samples))
	for _, sample := range g.samples {
		lines = append(lines, gaugeLine{values: sample.values, value: sample.value})
	}
	g.mu.RUnlock()

	sortByLabels(lines, func(line gaugeLine) []string { return line.values })
	for _, line := range lines {
		w.writeSample(g.name, renderLabels(g.labels, line.values, "", ""), formatValue(line.value))
	}
}

func (g *Gauge) unlabeledLocked() *GaugeSample {
	if len(g.labels) > 0 {
		panic(fmt.Sprintf("metrics: gauge %q declares %d label name(s); use With to bind label values", g.name, len(g.labels)))
	}
	return g.samples[sampleKey(nil)]
}

// Set replaces the gauge sample value.
func (s *GaugeSample) Set(value float64) {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()
	s.value = value
}

// Inc increases the gauge sample by one.
func (s *GaugeSample) Inc() {
	s.Add(1)
}

// Dec decreases the gauge sample by one.
func (s *GaugeSample) Dec() {
	s.Add(-1)
}

// Add changes the gauge sample by delta.
func (s *GaugeSample) Add(delta float64) {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()
	s.value += delta
}

func newHistogram(name, help string, buckets []float64, labels []string) *Histogram {
	bounds := append([]float64(nil), buckets...)
	sort.Float64s(bounds)

	histogram := &Histogram{
		name:    name,
		help:    help,
		labels:  append([]string(nil), labels...),
		bounds:  bounds,
		samples: make(map[string]*HistogramSample),
	}
	if len(histogram.labels) == 0 {
		histogram.samples[sampleKey(nil)] = newHistogramSample(histogram, nil)
	}
	return histogram
}

// Observe records a value in the unlabeled sample. It panics if the histogram
// declares labels, since an unlabeled sample cannot be identified.
func (h *Histogram) Observe(value float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unlabeledLocked().observeLocked(value)
}

// With returns the histogram sample for the given label values, creating it on
// first use. It panics if the number of label values does not match the number
// of declared label names.
func (h *Histogram) With(labelValues ...string) *HistogramSample {
	checkLabelValues(typeHistogram, h.name, len(h.labels), labelValues)
	return sampleFor(&h.mu, h.samples, labelValues, func() *HistogramSample {
		return newHistogramSample(h, labelValues)
	})
}

func (h *Histogram) familyName() string {
	return h.name
}

func (h *Histogram) writeTo(w *expositionWriter) {
	w.writeHelp(h.name, h.help)
	w.writeType(h.name, typeHistogram)

	h.mu.RLock()
	lines := make([]histogramLine, 0, len(h.samples))
	for _, sample := range h.samples {
		lines = append(lines, sample.snapshotLocked())
	}
	h.mu.RUnlock()

	sortByLabels(lines, func(line histogramLine) []string { return line.values })
	for _, line := range lines {
		for index, count := range line.counts {
			le := formatValue(line.bounds[index])
			w.writeSample(h.name+"_bucket", renderLabels(h.labels, line.values, leLabel, le), formatValue(float64(count)))
		}
		// The terminal +Inf bucket always matches the observation count.
		w.writeSample(h.name+"_bucket", renderLabels(h.labels, line.values, leLabel, infValue), formatValue(float64(line.count)))
		w.writeSample(h.name+"_sum", renderLabels(h.labels, line.values, "", ""), formatValue(line.sum))
		w.writeSample(h.name+"_count", renderLabels(h.labels, line.values, "", ""), formatValue(float64(line.count)))
	}
}

func (h *Histogram) unlabeledLocked() *HistogramSample {
	if len(h.labels) > 0 {
		panic(fmt.Sprintf("metrics: histogram %q declares %d label name(s); use With to bind label values", h.name, len(h.labels)))
	}
	return h.samples[sampleKey(nil)]
}

func newHistogramSample(parent *Histogram, values []string) *HistogramSample {
	return &HistogramSample{
		parent: parent,
		values: append([]string(nil), values...),
		counts: make([]uint64, len(parent.bounds)),
	}
}

// Observe records a value in the histogram sample.
func (s *HistogramSample) Observe(value float64) {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()
	s.observeLocked(value)
}

func (s *HistogramSample) observeLocked(value float64) {
	if index := sort.SearchFloat64s(s.parent.bounds, value); index < len(s.parent.bounds) {
		s.counts[index]++
	}
	s.sum += value
	s.count++
}

func (s *HistogramSample) snapshotLocked() histogramLine {
	counts := make([]uint64, len(s.counts))
	bounds := append([]float64(nil), s.parent.bounds...)
	var cumulative uint64
	for index, count := range s.counts {
		cumulative += count
		counts[index] = cumulative
	}
	return histogramLine{
		values: s.values,
		bounds: bounds,
		counts: counts,
		sum:    s.sum,
		count:  s.count,
	}
}

type counterLine struct {
	values []string
	value  float64
}

type gaugeLine struct {
	values []string
	value  float64
}

type histogramLine struct {
	values []string
	bounds []float64
	counts []uint64
	sum    float64
	count  uint64
}

// expositionWriter buffers the exposition format rules and remembers the first
// write error so a scrape stops early instead of hammering a broken writer.
type expositionWriter struct {
	w   io.Writer
	n   int64
	err error
}

func (w *expositionWriter) writeHelp(name string, help string) {
	w.write("# HELP ")
	w.write(name)
	w.write(" ")
	w.write(escapeText(help))
	w.write("\n")
}

func (w *expositionWriter) writeType(name string, metricType string) {
	w.write("# TYPE ")
	w.write(name)
	w.write(" ")
	w.write(metricType)
	w.write("\n")
}

func (w *expositionWriter) writeSample(name string, labels string, value string) {
	w.write(name)
	w.write(labels)
	w.write(" ")
	w.write(value)
	w.write("\n")
}

func (w *expositionWriter) write(text string) {
	if w.err != nil || text == "" {
		return
	}
	written, err := io.WriteString(w.w, text)
	w.n += int64(written)
	if err != nil {
		w.err = err
		return
	}
	if written != len(text) {
		w.err = io.ErrShortWrite
	}
}

// textEscaper escapes the characters that would otherwise break the single
// line structure of the exposition format.
var textEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)

func escapeText(value string) string {
	return textEscaper.Replace(value)
}

func renderLabels(names []string, values []string, extraName string, extraValue string) string {
	if len(names) == 0 && extraName == "" {
		return ""
	}

	var builder strings.Builder
	builder.WriteByte('{')
	for index := range names {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(names[index])
		builder.WriteByte('=')
		builder.WriteString(quoteValue(values[index]))
	}
	if extraName != "" {
		if len(names) > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(extraName)
		builder.WriteByte('=')
		builder.WriteString(quoteValue(extraValue))
	}
	builder.WriteByte('}')
	return builder.String()
}

func quoteValue(value string) string {
	return `"` + escapeText(value) + `"`
}

func formatValue(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// checkLabelValues fails fast when the supplied label values do not line up
// with the declared label names.
func checkLabelValues(metricType string, name string, declared int, values []string) {
	if len(values) != declared {
		panic(fmt.Sprintf("metrics: %s %q declares %d label name(s) but got %d label value(s)", metricType, name, declared, len(values)))
	}
}

// sampleFor returns the sample for values, creating it on first use. The lock
// must not be held by the caller.
func sampleFor[S any](mu *sync.RWMutex, samples map[string]S, values []string, create func() S) S {
	mu.Lock()
	defer mu.Unlock()

	key := sampleKey(values)
	if sample, ok := samples[key]; ok {
		return sample
	}
	sample := create()
	samples[key] = sample
	return sample
}

// sampleKey encodes label values without ambiguity, so values containing the
// separator cannot collide.
func sampleKey(values []string) string {
	var builder strings.Builder
	for _, value := range values {
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
	}
	return builder.String()
}

// sortByLabels orders lines by their label values so repeated scrapes of an
// unchanged registry produce identical bytes.
func sortByLabels[L any](lines []L, labelValues func(L) []string) {
	sort.SliceStable(lines, func(i, j int) bool {
		return compareValues(labelValues(lines[i]), labelValues(lines[j])) < 0
	})
}

func compareValues(left []string, right []string) int {
	for index := 0; index < len(left) && index < len(right); index++ {
		if order := strings.Compare(left[index], right[index]); order != 0 {
			return order
		}
	}
	return len(left) - len(right)
}
