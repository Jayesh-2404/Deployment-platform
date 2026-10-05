package metrics

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestRegistryWriteTo(t *testing.T) {
	tests := []struct {
		name  string
		build func() *Registry
		want  string
	}{
		{
			name:  "empty registry writes nothing",
			build: func() *Registry { return &Registry{} },
			want:  "",
		},
		{
			name: "counter without labels is emitted at zero",
			build: func() *Registry {
				registry := &Registry{}
				registry.Counter("deployments_total", "Total deployments")
				return registry
			},
			want: "# HELP deployments_total Total deployments\n" +
				"# TYPE deployments_total counter\n" +
				"deployments_total 0\n",
		},
		{
			name: "counter accumulates",
			build: func() *Registry {
				registry := &Registry{}
				counter := registry.Counter("deployments_total", "Total deployments")
				counter.Inc()
				counter.Inc()
				counter.Add(2.5)
				return registry
			},
			want: "# HELP deployments_total Total deployments\n" +
				"# TYPE deployments_total counter\n" +
				"deployments_total 4.5\n",
		},
		{
			name: "gauge set inc dec",
			build: func() *Registry {
				registry := &Registry{}
				gauge := registry.Gauge("workers_busy", "Busy workers")
				gauge.Set(10)
				gauge.Inc()
				gauge.Dec()
				gauge.Dec()
				gauge.Add(-0.5)
				return registry
			},
			want: "# HELP workers_busy Busy workers\n" +
				"# TYPE workers_busy gauge\n" +
				"workers_busy 8.5\n",
		},
		{
			name: "gauge without labels is emitted at zero",
			build: func() *Registry {
				registry := &Registry{}
				registry.Gauge("workers_busy", "Busy workers")
				return registry
			},
			want: "# HELP workers_busy Busy workers\n" +
				"# TYPE workers_busy gauge\n" +
				"workers_busy 0\n",
		},
		{
			name: "counter samples are ordered by label value",
			build: func() *Registry {
				registry := &Registry{}
				counter := registry.Counter("deployments_total", "Total deployments", "project", "status")
				counter.With("payapp", "success").Inc()
				counter.With("payapp", "failed").Inc()
				counter.With("payapp", "failed").Inc()
				counter.With("payapp", "success").Add(3)
				return registry
			},
			want: "# HELP deployments_total Total deployments\n" +
				"# TYPE deployments_total counter\n" +
				"deployments_total{project=\"payapp\",status=\"failed\"} 2\n" +
				"deployments_total{project=\"payapp\",status=\"success\"} 4\n",
		},
		{
			name: "gauge samples are ordered by label value",
			build: func() *Registry {
				registry := &Registry{}
				gauge := registry.Gauge("queue_depth", "Queued deployments", "stream", "project")
				gauge.With("out", "payapp").Set(7)
				gauge.With("in", "api").Set(1)
				return registry
			},
			want: "# HELP queue_depth Queued deployments\n" +
				"# TYPE queue_depth gauge\n" +
				"queue_depth{stream=\"in\",project=\"api\"} 1\n" +
				"queue_depth{stream=\"out\",project=\"payapp\"} 7\n",
		},
		{
			name: "labeled metric with no samples still declares help and type",
			build: func() *Registry {
				registry := &Registry{}
				registry.Counter("attempts_total", "Attempts", "reason")
				return registry
			},
			want: "# HELP attempts_total Attempts\n" +
				"# TYPE attempts_total counter\n",
		},
		{
			name: "families are sorted by name",
			build: func() *Registry {
				registry := &Registry{}
				registry.Counter("zeta_total", "Zeta")
				registry.Gauge("alpha_value", "Alpha")
				registry.Counter("mid_total", "Mid")
				return registry
			},
			want: "# HELP alpha_value Alpha\n" +
				"# TYPE alpha_value gauge\n" +
				"alpha_value 0\n" +
				"# HELP mid_total Mid\n" +
				"# TYPE mid_total counter\n" +
				"mid_total 0\n" +
				"# HELP zeta_total Zeta\n" +
				"# TYPE zeta_total counter\n" +
				"zeta_total 0\n",
		},
		{
			name: "histogram with no observations",
			build: func() *Registry {
				registry := &Registry{}
				registry.Histogram("deploy_seconds", "Deployment duration", []float64{0.25, 0.5, 1})
				return registry
			},
			want: "# HELP deploy_seconds Deployment duration\n" +
				"# TYPE deploy_seconds histogram\n" +
				"deploy_seconds_bucket{le=\"0.25\"} 0\n" +
				"deploy_seconds_bucket{le=\"0.5\"} 0\n" +
				"deploy_seconds_bucket{le=\"1\"} 0\n" +
				"deploy_seconds_bucket{le=\"+Inf\"} 0\n" +
				"deploy_seconds_sum 0\n" +
				"deploy_seconds_count 0\n",
		},
		{
			name: "histogram observes values",
			build: func() *Registry {
				registry := &Registry{}
				histogram := registry.Histogram("deploy_seconds", "Deployment duration", []float64{0.25, 0.5, 1})
				for _, value := range []float64{0.25, 0.5, 0.75, 1, 2.5} {
					histogram.Observe(value)
				}
				return registry
			},
			want: "# HELP deploy_seconds Deployment duration\n" +
				"# TYPE deploy_seconds histogram\n" +
				"deploy_seconds_bucket{le=\"0.25\"} 1\n" +
				"deploy_seconds_bucket{le=\"0.5\"} 2\n" +
				"deploy_seconds_bucket{le=\"1\"} 4\n" +
				"deploy_seconds_bucket{le=\"+Inf\"} 5\n" +
				"deploy_seconds_sum 5\n" +
				"deploy_seconds_count 5\n",
		},
		{
			name: "histogram buckets are sorted and the caller slice is untouched",
			build: func() *Registry {
				registry := &Registry{}
				registry.Histogram("deploy_seconds", "Deployment duration", []float64{1, 0.25, 0.5})
				return registry
			},
			want: "# HELP deploy_seconds Deployment duration\n" +
				"# TYPE deploy_seconds histogram\n" +
				"deploy_seconds_bucket{le=\"0.25\"} 0\n" +
				"deploy_seconds_bucket{le=\"0.5\"} 0\n" +
				"deploy_seconds_bucket{le=\"1\"} 0\n" +
				"deploy_seconds_bucket{le=\"+Inf\"} 0\n" +
				"deploy_seconds_sum 0\n" +
				"deploy_seconds_count 0\n",
		},
		{
			name: "histogram with labels",
			build: func() *Registry {
				registry := &Registry{}
				histogram := registry.Histogram("build_seconds", "Build duration", []float64{1, 2}, "project")
				histogram.With("payapp").Observe(1.5)
				histogram.With("api").Observe(0.5)
				histogram.With("api").Observe(4)
				return registry
			},
			want: "# HELP build_seconds Build duration\n" +
				"# TYPE build_seconds histogram\n" +
				"build_seconds_bucket{project=\"api\",le=\"1\"} 1\n" +
				"build_seconds_bucket{project=\"api\",le=\"2\"} 1\n" +
				"build_seconds_bucket{project=\"api\",le=\"+Inf\"} 2\n" +
				"build_seconds_sum{project=\"api\"} 4.5\n" +
				"build_seconds_count{project=\"api\"} 2\n" +
				"build_seconds_bucket{project=\"payapp\",le=\"1\"} 0\n" +
				"build_seconds_bucket{project=\"payapp\",le=\"2\"} 1\n" +
				"build_seconds_bucket{project=\"payapp\",le=\"+Inf\"} 1\n" +
				"build_seconds_sum{project=\"payapp\"} 1.5\n" +
				"build_seconds_count{project=\"payapp\"} 1\n",
		},
		{
			name: "help text is escaped",
			build: func() *Registry {
				registry := &Registry{}
				registry.Counter("weird_total", "a \\ backslash, a \" quote\nand a newline")
				return registry
			},
			want: "# HELP weird_total a \\\\ backslash, a \\\" quote\\nand a newline\n" +
				"# TYPE weird_total counter\n" +
				"weird_total 0\n",
		},
		{
			name: "label values are escaped",
			build: func() *Registry {
				registry := &Registry{}
				counter := registry.Counter("logs_total", "Log lines", "stream")
				counter.With("say \"hi\"\nplease").Inc()
				counter.With("c:\\logs\\out").Inc()
				return registry
			},
			want: "# HELP logs_total Log lines\n" +
				"# TYPE logs_total counter\n" +
				"logs_total{stream=\"c:\\\\logs\\\\out\"} 1\n" +
				"logs_total{stream=\"say \\\"hi\\\"\\nplease\"} 1\n",
		},
		{
			name: "empty help text is allowed",
			build: func() *Registry {
				registry := &Registry{}
				registry.Counter("bare_total", "")
				return registry
			},
			want: "# HELP bare_total \n" +
				"# TYPE bare_total counter\n" +
				"bare_total 0\n",
		},
		{
			name: "negative and fractional values are rendered",
			build: func() *Registry {
				registry := &Registry{}
				registry.Counter("delta_total", "Delta").Add(-0.125)
				registry.Gauge("ratio", "Ratio").Set(1e-7)
				return registry
			},
			want: "# HELP delta_total Delta\n" +
				"# TYPE delta_total counter\n" +
				"delta_total -0.125\n" +
				"# HELP ratio Ratio\n" +
				"# TYPE ratio gauge\n" +
				"ratio 1e-07\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := render(t, test.build())

			if got != test.want {
				t.Fatalf("WriteTo() output mismatch\n got: %q\nwant: %q", got, test.want)
			}
			if test.want != "" {
				if !strings.HasSuffix(got, "\n") {
					t.Fatalf("expected a trailing newline, got %q", got)
				}
				if strings.Contains(got, "\n\n") {
					t.Fatalf("expected no blank lines between metrics, got %q", got)
				}
			}
		})
	}
}

func TestCounterAddAndInc(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*Counter)
		want  string
	}{
		{name: "nothing", apply: func(*Counter) {}, want: "0"},
		{name: "inc", apply: func(c *Counter) { c.Inc() }, want: "1"},
		{name: "add", apply: func(c *Counter) { c.Add(2.5) }, want: "2.5"},
		{name: "add negative", apply: func(c *Counter) { c.Add(-1.5) }, want: "-1.5"},
		{name: "inc twice", apply: func(c *Counter) { c.Inc(); c.Inc() }, want: "2"},
		{name: "inc and add", apply: func(c *Counter) { c.Inc(); c.Add(0.5) }, want: "1.5"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := &Registry{}
			counter := registry.Counter("hits_total", "Hits")
			test.apply(counter)

			if got := sampleValue(t, render(t, registry), "hits_total", ""); got != test.want {
				t.Fatalf("hits_total = %s, want %s", got, test.want)
			}
		})
	}
}

func TestGaugeSetIncDec(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*Gauge)
		want  string
	}{
		{name: "nothing", apply: func(*Gauge) {}, want: "0"},
		{name: "set", apply: func(g *Gauge) { g.Set(3) }, want: "3"},
		{name: "inc", apply: func(g *Gauge) { g.Inc() }, want: "1"},
		{name: "dec", apply: func(g *Gauge) { g.Dec() }, want: "-1"},
		{name: "set overwrites", apply: func(g *Gauge) { g.Set(9); g.Set(-2) }, want: "-2"},
		{name: "inc then dec", apply: func(g *Gauge) { g.Inc(); g.Inc(); g.Dec() }, want: "1"},
		{name: "dec below zero", apply: func(g *Gauge) { g.Dec() }, want: "-1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := &Registry{}
			gauge := registry.Gauge("temperature", "Temperature")
			test.apply(gauge)

			if got := sampleValue(t, render(t, registry), "temperature", ""); got != test.want {
				t.Fatalf("temperature = %s, want %s", got, test.want)
			}
		})
	}
}

func TestSampleMutators(t *testing.T) {
	registry := &Registry{}
	counter := registry.Counter("c_total", "C", "status")
	counter.With("ok").Inc()
	counter.With("ok").Add(2)
	gauge := registry.Gauge("g_value", "G", "status")
	gauge.With("ok").Set(4)
	gauge.With("ok").Inc()
	gauge.With("ok").Dec()
	gauge.With("ok").Add(-1.5)
	histogram := registry.Histogram("h_seconds", "H", []float64{1}, "status")
	histogram.With("ok").Observe(0.5)
	histogram.With("ok").Observe(1.5)

	output := render(t, registry)
	if got := sampleValue(t, output, "c_total", `{status="ok"}`); got != "3" {
		t.Fatalf(`c_total{status="ok"} = %s, want 3`, got)
	}
	if got := sampleValue(t, output, "g_value", `{status="ok"}`); got != "2.5" {
		t.Fatalf(`g_value{status="ok"} = %s, want 2.5`, got)
	}
	if got := sampleValue(t, output, "h_seconds_sum", `{status="ok"}`); got != "2" {
		t.Fatalf(`h_seconds_sum{status="ok"} = %s, want 2`, got)
	}
	if got := sampleValue(t, output, "h_seconds_count", `{status="ok"}`); got != "2" {
		t.Fatalf(`h_seconds_count{status="ok"} = %s, want 2`, got)
	}
}

func TestWithPanicsOnLabelMismatch(t *testing.T) {
	tests := []struct {
		name         string
		labels       []string
		labelValues  []string
		wantPanicked bool
	}{
		{name: "no labels no values", labels: nil, labelValues: nil, wantPanicked: false},
		{name: "no labels one value", labels: nil, labelValues: []string{"a"}, wantPanicked: true},
		{name: "no labels two values", labels: nil, labelValues: []string{"a", "b"}, wantPanicked: true},
		{name: "one label one value", labels: []string{"project"}, labelValues: []string{"a"}, wantPanicked: false},
		{name: "one label no values", labels: []string{"project"}, labelValues: nil, wantPanicked: true},
		{name: "one label two values", labels: []string{"project"}, labelValues: []string{"a", "b"}, wantPanicked: true},
		{name: "two labels one value", labels: []string{"project", "status"}, labelValues: []string{"a"}, wantPanicked: true},
		{name: "two labels three values", labels: []string{"project", "status"}, labelValues: []string{"a", "b", "c"}, wantPanicked: true},
		{name: "reversed declaration order", labels: []string{"project", "status"}, labelValues: []string{"a", "b"}, wantPanicked: false},
	}

	metrics := []struct {
		kind string
		bind func(*Registry, []string, []string) func()
	}{
		{
			kind: "counter",
			bind: func(registry *Registry, labels []string, values []string) func() {
				counter := registry.Counter("m_total", "M", labels...)
				return func() { counter.With(values...) }
			},
		},
		{
			kind: "gauge",
			bind: func(registry *Registry, labels []string, values []string) func() {
				gauge := registry.Gauge("m_value", "M", labels...)
				return func() { gauge.With(values...) }
			},
		},
		{
			kind: "histogram",
			bind: func(registry *Registry, labels []string, values []string) func() {
				histogram := registry.Histogram("m_seconds", "M", []float64{1}, labels...)
				return func() { histogram.With(values...) }
			},
		},
	}

	for _, metric := range metrics {
		for _, test := range tests {
			t.Run(metric.kind+"/"+test.name, func(t *testing.T) {
				registry := &Registry{}
				invoke := metric.bind(registry, test.labels, test.labelValues)

				if panicked := didPanic(invoke); panicked != test.wantPanicked {
					t.Fatalf("panic = %v, wantPanicked = %v", panicked, test.wantPanicked)
				}
			})
		}
	}
}

func TestUnlabeledMutationPanicsOnLabeledMetric(t *testing.T) {
	registry := &Registry{}
	counter := registry.Counter("c_total", "C", "project")
	gauge := registry.Gauge("g_value", "G", "project")
	histogram := registry.Histogram("h_seconds", "H", []float64{1}, "project")

	tests := []struct {
		name string
		call func()
	}{
		{name: "counter inc", call: counter.Inc},
		{name: "counter add", call: func() { counter.Add(1) }},
		{name: "gauge set", call: func() { gauge.Set(1) }},
		{name: "gauge inc", call: gauge.Inc},
		{name: "gauge dec", call: gauge.Dec},
		{name: "gauge add", call: func() { gauge.Add(1) }},
		{name: "histogram observe", call: func() { histogram.Observe(1) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !didPanic(test.call) {
				t.Fatalf("expected a panic when mutating a labeled metric without label values")
			}
		})
	}
}

func TestWithReturnsSameSampleForSameValues(t *testing.T) {
	registry := &Registry{}
	counter := registry.Counter("c_total", "C", "a", "b")
	gauge := registry.Gauge("g_value", "G", "a", "b")
	histogram := registry.Histogram("h_seconds", "H", []float64{1}, "a", "b")

	if counter.With("1", "2") != counter.With("1", "2") {
		t.Fatalf("expected counter.With to return the same sample for equal label values")
	}
	if gauge.With("1", "2") != gauge.With("1", "2") {
		t.Fatalf("expected gauge.With to return the same sample for equal label values")
	}
	if histogram.With("1", "2") != histogram.With("1", "2") {
		t.Fatalf("expected histogram.With to return the same sample for equal label values")
	}
	if counter.With("1", "2") == counter.With("2", "1") {
		t.Fatalf("expected different samples for a different label value order")
	}
	if counter.With("1", "2") == counter.With("1", "3") {
		t.Fatalf("expected different samples for different label values")
	}
}

func TestLabelOrderFollowsDeclaration(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		values []string
		want   string
	}{
		{
			name:   "not alphabetical",
			labels: []string{"status", "project", "attempt"},
			values: []string{"failed", "payapp", "3"},
			want:   `c_total{status="failed",project="payapp",attempt="3"} 1`,
		},
		{
			name:   "single label",
			labels: []string{"project"},
			values: []string{"payapp"},
			want:   `c_total{project="payapp"} 1`,
		},
		{
			name:   "empty label value",
			labels: []string{"project", "status"},
			values: []string{"", ""},
			want:   `c_total{project="",status=""} 1`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := &Registry{}
			registry.Counter("c_total", "C", test.labels...).With(test.values...).Inc()

			// Declaration order drives rendering order, and an unlabeled
			// sample is never emitted for a labeled metric.
			got := sampleLines(render(t, registry))
			if len(got) != 1 || got[0] != test.want {
				t.Fatalf("sample lines = %v, want [%s]", got, test.want)
			}
		})
	}
}

func TestHistogramBucketCountsAreCumulative(t *testing.T) {
	tests := []struct {
		name         string
		buckets      []float64
		observations []float64
		want         []string
	}{
		{
			name:         "cumulative up to each bound",
			buckets:      []float64{0.25, 0.5, 1},
			observations: []float64{0.25, 0.5, 0.75, 1, 2.5},
			want:         []string{"1", "2", "4", "5"},
		},
		{
			name:         "observation exactly on a bound lands in that bucket",
			buckets:      []float64{1, 2, 3},
			observations: []float64{1, 1, 2, 3, 3, 3},
			want:         []string{"2", "3", "6", "6"},
		},
		{
			name:         "observations above every bound only hit +Inf",
			buckets:      []float64{1, 2},
			observations: []float64{5, 6, 7},
			want:         []string{"0", "0", "3"},
		},
		{
			name:         "single bucket",
			buckets:      []float64{1},
			observations: []float64{0.5, 1, 1.5},
			want:         []string{"2", "3"},
		},
		{
			name:         "negative observations",
			buckets:      []float64{-1, 0, 1},
			observations: []float64{-5, -1, 0, 1, 2},
			want:         []string{"2", "3", "4", "5"},
		},
		{
			name:         "no buckets declared",
			buckets:      nil,
			observations: []float64{1, 2, 3},
			want:         []string{"3"},
		},
		{
			name:         "no observations",
			buckets:      []float64{1, 2},
			observations: nil,
			want:         []string{"0", "0", "0"},
		},
		{
			name:         "unsorted buckets are sorted at construction",
			buckets:      []float64{2, 1},
			observations: []float64{1, 2, 3},
			want:         []string{"1", "2", "3"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := &Registry{}
			histogram := registry.Histogram("h_seconds", "H", test.buckets)
			for _, observation := range test.observations {
				histogram.Observe(observation)
			}

			got := bucketCounts(t, render(t, registry), "h_seconds")
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Fatalf("bucket counts = %v, want %v", got, test.want)
			}
		})
	}
}

func TestHistogramLeLabelsFollowSortedBuckets(t *testing.T) {
	buckets := []float64{10, 1, 5, 2}
	registry := &Registry{}
	registry.Histogram("h_seconds", "H", buckets).Observe(1)

	var want []string
	for _, le := range []string{"1", "2", "5", "10", "+Inf"} {
		want = append(want, fmt.Sprintf("h_seconds_bucket{le=%q} ", le))
	}

	output := render(t, registry)
	for _, prefix := range want {
		if !strings.Contains(output, prefix) {
			t.Fatalf("expected %q in output:\n%s", prefix, output)
		}
	}
	if got := fmt.Sprint(buckets); got != "[10 1 5 2]" {
		t.Fatalf("caller slice was modified: %s", got)
	}
}

func TestHistogramInfBucketEqualsCount(t *testing.T) {
	registry := &Registry{}
	histogram := registry.Histogram("h_seconds", "H", []float64{0.1, 1, 10}, "project")
	histogram.With("payapp").Observe(0.05)
	histogram.With("payapp").Observe(0.5)
	histogram.With("payapp").Observe(50)
	histogram.With("api")

	output := render(t, registry)

	if got := sampleValue(t, output, "h_seconds_bucket", `{project="api",le="+Inf"}`); got != "0" {
		t.Fatalf(`api +Inf bucket = %s, want 0`, got)
	}
	if got := sampleValue(t, output, "h_seconds_count", `{project="api"}`); got != "0" {
		t.Fatalf(`api count = %s, want 0`, got)
	}
	if got := sampleValue(t, output, "h_seconds_bucket", `{project="payapp",le="+Inf"}`); got != "3" {
		t.Fatalf(`payapp +Inf bucket = %s, want 3`, got)
	}
	if got := sampleValue(t, output, "h_seconds_count", `{project="payapp"}`); got != "3" {
		t.Fatalf(`payapp count = %s, want 3`, got)
	}
}

func TestHistogramSumAccumulates(t *testing.T) {
	registry := &Registry{}
	histogram := registry.Histogram("h_seconds", "H", []float64{1, 2})
	for _, observation := range []float64{0.25, 0.5, 0.75, 2.5} {
		histogram.Observe(observation)
	}

	output := render(t, registry)
	if got := sampleValue(t, output, "h_seconds_sum", ""); got != "4" {
		t.Fatalf("h_seconds_sum = %s, want 4", got)
	}
	if got := sampleValue(t, output, "h_seconds_count", ""); got != "4" {
		t.Fatalf("h_seconds_count = %s, want 4", got)
	}
}

func TestWriteToIsDeterministic(t *testing.T) {
	registry := &Registry{}
	histogram := registry.Histogram("z_histogram", "Z", []float64{0.1, 1}, "a", "b")
	gauge := registry.Gauge("m_gauge", "M", "a", "b")
	counter := registry.Counter("a_counter", "A", "a", "b")

	// Bind in a scrambled order to make map iteration order irrelevant.
	for _, label := range []string{"one", "two", "three", "four", "five"} {
		histogram.With(label, label).Observe(0.5)
		counter.With(label, "b").Inc()
		gauge.With("a", label).Set(float64(len(label)))
	}

	first := render(t, registry)
	if !strings.HasSuffix(first, "\n") {
		t.Fatalf("expected a trailing newline, got %q", first)
	}

	for attempt := 0; attempt < 25; attempt++ {
		if got := render(t, registry); got != first {
			t.Fatalf("attempt %d: output changed between scrapes\n got: %q\nwant: %q", attempt, got, first)
		}
	}
}

func TestWriteToReportsBytesWritten(t *testing.T) {
	registry := &Registry{}
	registry.Counter("hits_total", "Hits", "status").With("ok").Inc()

	var buffer bytes.Buffer
	written, err := registry.WriteTo(&buffer)
	if err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}
	if written != int64(buffer.Len()) {
		t.Fatalf("WriteTo() reported %d bytes, buffer holds %d", written, buffer.Len())
	}
	if written == 0 {
		t.Fatalf("expected a non-empty exposition")
	}

	if written, err := (&Registry{}).WriteTo(&buffer); written != 0 || err != nil {
		t.Fatalf("WriteTo() on an empty registry = (%d, %v), want (0, nil)", written, err)
	}
}

func TestWriteToPropagatesWriterError(t *testing.T) {
	registry := &Registry{}
	registry.Counter("a_total", "A")
	registry.Gauge("b_value", "B").Set(1)

	want := errors.New("connection reset")
	written, err := registry.WriteTo(&failingWriter{limit: 12, err: want})
	if !errors.Is(err, want) {
		t.Fatalf("WriteTo() error = %v, want %v", err, want)
	}
	if written != 12 {
		t.Fatalf("WriteTo() = %d bytes, want 12", written)
	}
}

func TestWriteToDetectsShortWrite(t *testing.T) {
	registry := &Registry{}
	registry.Counter("a_total", "A")

	if _, err := registry.WriteTo(&shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("WriteTo() error = %v, want %v", err, io.ErrShortWrite)
	}
}

func TestRegisterDuplicateNamePanics(t *testing.T) {
	tests := []struct {
		name     string
		register func(*Registry)
	}{
		{name: "counter after counter", register: func(r *Registry) { r.Counter("dup_total", "A") }},
		{name: "gauge after counter", register: func(r *Registry) { r.Gauge("dup_total", "A") }},
		{name: "counter after gauge", register: func(r *Registry) { r.Counter("dup_total", "A") }},
		{name: "histogram after counter", register: func(r *Registry) { r.Histogram("dup_total", "A", []float64{1}) }},
		{name: "counter after histogram", register: func(r *Registry) { r.Counter("dup_total", "A") }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := &Registry{}
			registry.Counter("dup_total", "First")

			if !didPanic(func() { test.register(registry) }) {
				t.Fatalf("expected a panic when registering a duplicate metric name")
			}
		})
	}
}

func TestSampleKeysDoNotCollide(t *testing.T) {
	tests := []struct {
		name        string
		left        []string
		right       []string
		wantSamples int
		wantValue   float64
	}{
		{name: "identical", left: []string{"a", "b"}, right: []string{"a", "b"}, wantSamples: 1, wantValue: 2},
		{name: "different values", left: []string{"a", "b"}, right: []string{"ab", ""}, wantSamples: 2, wantValue: 1},
		{name: "separator inside a value", left: []string{"a:1b", ""}, right: []string{"a", "1b"}, wantSamples: 2, wantValue: 1},
		{name: "prefix of another key", left: []string{"a", "bc"}, right: []string{"ab", "c"}, wantSamples: 2, wantValue: 1},
		{name: "empty values", left: []string{"", ""}, right: []string{"", ""}, wantSamples: 1, wantValue: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := &Registry{}
			counter := registry.Counter("c_total", "C", "a", "b")
			counter.With(test.left...).Inc()
			counter.With(test.right...).Inc()

			if len(counter.samples) != test.wantSamples {
				t.Fatalf("sample count = %d, want %d", len(counter.samples), test.wantSamples)
			}
			if got := counter.With(test.left...).value; got != test.wantValue {
				t.Fatalf("left sample value = %v, want %v", got, test.wantValue)
			}
		})
	}
}

func TestConcurrentScrapeAndMutation(t *testing.T) {
	registry := &Registry{}
	counter := registry.Counter("deploys_total", "Deploys", "status")
	gauge := registry.Gauge("queue_depth", "Queue", "status")
	histogram := registry.Histogram("deploy_seconds", "Seconds", []float64{1, 2}, "status")

	const (
		writers        = 4
		rounds         = 200
		perStatus      = writers * rounds
		scrapers       = 2
		scraperScrapes = 50
	)

	var workers sync.WaitGroup
	workers.Add(writers + scrapers)

	for writer := 0; writer < writers; writer++ {
		go func() {
			defer workers.Done()
			for round := 0; round < rounds; round++ {
				for _, status := range []string{"success", "failed"} {
					counter.With(status).Inc()
					gauge.With(status).Add(1)
					histogram.With(status).Observe(float64(round))
				}
			}
		}()
	}

	for scraper := 0; scraper < scrapers; scraper++ {
		go func() {
			defer workers.Done()
			for scrape := 0; scrape < scraperScrapes; scrape++ {
				if mustRender(registry) == "" {
					panic("expected a non-empty exposition during a concurrent scrape")
				}
			}
		}()
	}

	workers.Wait()

	output := render(t, registry)
	for _, status := range []string{"success", "failed"} {
		if got := sampleValue(t, output, "deploys_total", fmt.Sprintf(`{status=%q}`, status)); got != fmt.Sprint(perStatus) {
			t.Fatalf("deploys_total{status=%s} = %s, want %d", status, got, perStatus)
		}
		if got := sampleValue(t, output, "deploy_seconds_count", fmt.Sprintf(`{status=%q}`, status)); got != fmt.Sprint(perStatus) {
			t.Fatalf("deploy_seconds_count{status=%s} = %s, want %d", status, got, perStatus)
		}
		if got := sampleValue(t, output, "deploy_seconds_bucket", fmt.Sprintf(`{status=%q,le="+Inf"}`, status)); got != fmt.Sprint(perStatus) {
			t.Fatalf("deploy_seconds_bucket{status=%s,le=+Inf} = %s, want %d", status, got, perStatus)
		}
	}
	if got := sampleValue(t, output, "queue_depth", `{status="success"}`); got != fmt.Sprint(perStatus) {
		t.Fatalf("queue_depth{status=success} = %s, want %d", got, perStatus)
	}
}

func render(t *testing.T, registry *Registry) string {
	t.Helper()

	var buffer bytes.Buffer
	if _, err := registry.WriteTo(&buffer); err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}
	return buffer.String()
}

func mustRender(registry *Registry) string {
	var buffer bytes.Buffer
	if _, err := registry.WriteTo(&buffer); err != nil {
		panic(fmt.Sprintf("WriteTo() error = %v", err))
	}
	return buffer.String()
}

// sampleLines returns every sample line of an exposition, skipping the HELP
// and TYPE comment lines.
func sampleLines(output string) []string {
	lines := make([]string, 0, 4)
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// sampleValue returns the value of the exposition line for name and labels,
// failing the test when the line is missing.
func sampleValue(t *testing.T, output string, name string, labels string) string {
	t.Helper()

	prefix := name + labels + " "
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("sample %s%s not found in output:\n%s", name, labels, output)
	return ""
}

// bucketCounts returns the ordered bucket values of a histogram family,
// including the terminal +Inf bucket.
func bucketCounts(t *testing.T, output string, name string) []string {
	t.Helper()

	counts := make([]string, 0, 4)
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if !strings.HasPrefix(line, name+"_bucket{") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("malformed bucket line %q", line)
		}
		counts = append(counts, fields[1])
	}
	if len(counts) == 0 {
		t.Fatalf("no bucket lines found for %s in output:\n%s", name, output)
	}
	return counts
}

func didPanic(call func()) (panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	call()
	return false
}

// failingWriter accepts up to limit bytes and then fails with err.
type failingWriter struct {
	limit int
	err   error
	n     int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.n >= w.limit {
		return 0, w.err
	}
	remaining := w.limit - w.n
	if len(p) > remaining {
		w.n += remaining
		return remaining, w.err
	}
	w.n += len(p)
	return len(p), nil
}

// shortWriter reports one byte fewer than it was given without an error.
type shortWriter struct{}

func (*shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}
