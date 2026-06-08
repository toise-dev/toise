// Package metrics exposes Toise's internals as Prometheus metrics at /metrics.
//
// The Toise-specific metrics are sampled at scrape time by a custom collector
// (it reads the live projection and store on each scrape) rather than by
// instrumenting the hot path — so it adds no overhead to ingestion and no metric
// can drift out of sync with the graph. The handler also registers the standard
// Go runtime and process collectors. See #44.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Grapher is the slice of the projection the collector samples.
type Grapher interface {
	EntityCount() int
	RelationCount() int
	CountByType() map[string]int
}

// Storer is the slice of the event store the collector samples.
type Storer interface {
	Sequence() uint64
	DiskUsage() uint64
	PrunedEvents() uint64
	PrunedBytes() uint64
	SnapshotSeq() uint64
	SnapshotsWritten() uint64
}

// Collector samples the live graph and store on each Prometheus scrape.
type Collector struct {
	graph Grapher
	store Storer

	version, commit string

	buildInfo      *prometheus.Desc
	entities       *prometheus.Desc
	entitiesByType *prometheus.Desc
	relations      *prometheus.Desc
	events         *prometheus.Desc
	diskBytes      *prometheus.Desc
	prunedEvents   *prometheus.Desc
	prunedBytes    *prometheus.Desc
	snapshotSeq    *prometheus.Desc
	snapshots      *prometheus.Desc
}

// NewCollector builds a collector over the graph and store, stamping build info
// from version/commit.
func NewCollector(g Grapher, s Storer, version, commit string) *Collector {
	return &Collector{
		graph:   g,
		store:   s,
		version: version,
		commit:  commit,
		buildInfo: prometheus.NewDesc("toise_build_info",
			"Build information, value is always 1.", []string{"version", "commit"}, nil),
		entities: prometheus.NewDesc("toise_entities",
			"Number of live entities in the projection.", nil, nil),
		entitiesByType: prometheus.NewDesc("toise_entities_by_type",
			"Number of live entities per type.", []string{"type"}, nil),
		relations: prometheus.NewDesc("toise_relations",
			"Number of live relations in the projection.", nil, nil),
		events: prometheus.NewDesc("toise_events_total",
			"Total number of change events appended to the log.", nil, nil),
		diskBytes: prometheus.NewDesc("toise_store_disk_bytes",
			"Approximate on-disk size of the event store in bytes.", nil, nil),
		prunedEvents: prometheus.NewDesc("toise_events_pruned_total",
			"Total events removed by retention pruning.", nil, nil),
		prunedBytes: prometheus.NewDesc("toise_bytes_pruned_total",
			"Total approximate bytes removed by retention pruning.", nil, nil),
		snapshotSeq: prometheus.NewDesc("toise_snapshot_seq",
			"Reference sequence of the last projection snapshot written (0 if none).", nil, nil),
		snapshots: prometheus.NewDesc("toise_snapshots_written_total",
			"Total projection snapshots written.", nil, nil),
	}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.buildInfo
	ch <- c.entities
	ch <- c.entitiesByType
	ch <- c.relations
	ch <- c.events
	ch <- c.diskBytes
	ch <- c.prunedEvents
	ch <- c.prunedBytes
	ch <- c.snapshotSeq
	ch <- c.snapshots
}

// Collect implements prometheus.Collector, sampling the live state.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(c.buildInfo, prometheus.GaugeValue, 1, c.version, c.commit)
	ch <- prometheus.MustNewConstMetric(c.entities, prometheus.GaugeValue, float64(c.graph.EntityCount()))
	ch <- prometheus.MustNewConstMetric(c.relations, prometheus.GaugeValue, float64(c.graph.RelationCount()))
	ch <- prometheus.MustNewConstMetric(c.events, prometheus.CounterValue, float64(c.store.Sequence()))
	ch <- prometheus.MustNewConstMetric(c.diskBytes, prometheus.GaugeValue, float64(c.store.DiskUsage()))
	ch <- prometheus.MustNewConstMetric(c.prunedEvents, prometheus.CounterValue, float64(c.store.PrunedEvents()))
	ch <- prometheus.MustNewConstMetric(c.prunedBytes, prometheus.CounterValue, float64(c.store.PrunedBytes()))
	ch <- prometheus.MustNewConstMetric(c.snapshotSeq, prometheus.GaugeValue, float64(c.store.SnapshotSeq()))
	ch <- prometheus.MustNewConstMetric(c.snapshots, prometheus.CounterValue, float64(c.store.SnapshotsWritten()))
	for typ, n := range c.graph.CountByType() {
		ch <- prometheus.MustNewConstMetric(c.entitiesByType, prometheus.GaugeValue, float64(n), typ)
	}
}

// Handler returns the /metrics HTTP handler: a private registry holding the Toise
// collector plus the standard Go runtime and process collectors.
func Handler(c *Collector) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(c, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}
