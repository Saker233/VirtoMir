// Package insights provides operations intelligence for VirtoMir: capacity
// forecasting, right-sizing and anomaly detection (ADR-0066).
//
// Persistence-free design: the Engine holds no state of its own. It recomputes
// every forecast on demand from the bounded history rings in a history.Store.
// The rings themselves are saved periodically by the daemon (see saveHistory),
// so a restart keeps the data the Engine queries. No separate Engine
// persistence is needed.
package insights

import (
	"fmt"
	"math"
	"time"

	"github.com/onurbaydas/virtomir/internal/history"
)

// Querier answers history queries (satisfied by *history.Store).
type Querier interface {
	Query(names []string, rng time.Duration, now time.Time) (history.Result, error)
}

// Machine describes a virtual machine for operations-intelligence queries.
type Machine struct {
	UUID    string
	Name    string
	VCPUs   int
	MemMiB  uint64
	Running bool
}

// Pool describes a storage pool for capacity forecasting.
type Pool struct {
	Name          string
	CapacityBytes uint64
	FreeBytes     uint64
}

// HostCap describes a host's capacity for cluster-level forecasting.
type HostCap struct {
	ID     string
	Name   string
	CPUs   int
	MemMiB uint64
}

// Series name constants mirror the live package's recording names so the
// Engine stays self-contained (no dependency on the live package).
const (
	seriesHostCPU       = "host.cpu"
	seriesHostMem       = "host.mem"
	seriesHostDiskRead  = "host.disk.read"
	seriesHostDiskWrite = "host.disk.write"
)

// vmSeries returns the name of a machine's series, e.g. vmSeries(uuid, "cpu").
func vmSeries(uuid, metric string) string { return "vm." + uuid + "." + metric }

// Engine provides operations intelligence by querying bounded history rings on
// demand. The default analysis window is 30 days, matching the coarsest
// history.Tier.
type Engine struct {
	q      Querier
	window time.Duration
}

// New returns an Engine backed by q with a default 30-day analysis window,
// matching the coarsest history.Tier (1 h × 30 × 24).
func New(q Querier) *Engine {
	return &Engine{q: q, window: 30 * 24 * time.Hour}
}

// HostForecast produces capacity forecasts for a single host over the engine
// window. CPU and memory are percentages (capacity = 100); disk I/O series are
// throughput trends with no finite capacity ceiling.
func (e *Engine) HostForecast(host HostCap, now time.Time) (cpu, mem Forecast, disk map[string]Forecast, err error) {
	names := []string{seriesHostCPU, seriesHostMem, seriesHostDiskRead, seriesHostDiskWrite}
	res, err := e.q.Query(names, e.window, now)
	if err != nil {
		return Forecast{}, Forecast{}, nil, err
	}

	cpu = ForecastCapacity(Decode(res, seriesHostCPU), 100.0, 24*time.Hour, now)
	cpu.Metric = seriesHostCPU

	mem = ForecastCapacity(Decode(res, seriesHostMem), 100.0, 24*time.Hour, now)
	mem.Metric = seriesHostMem

	disk = make(map[string]Forecast, 2)
	for _, name := range []string{seriesHostDiskRead, seriesHostDiskWrite} {
		f := ForecastCapacity(Decode(res, name), math.MaxFloat64, 24*time.Hour, now)
		f.Metric = name
		if math.IsInf(f.DaysUntilFull, 1) {
			f.DaysUntilFull = -1
		}
		disk[name] = f
	}
	return cpu, mem, disk, nil
}

// ClusterForecast computes cluster-wide CPU% and Mem% forecasts by weighting
// each host's history by its vCPU count (CPU) and memory size (Mem). The
// weighted average series is then fed to ForecastCapacity with capacity 100.
func (e *Engine) ClusterForecast(hosts []HostCap, now time.Time) (cpu, mem Forecast, err error) {
	if len(hosts) == 0 {
		return Forecast{}, Forecast{}, fmt.Errorf("no hosts")
	}

	cpuNames := make([]string, len(hosts))
	memNames := make([]string, len(hosts))
	cpuWeights := make([]float64, len(hosts))
	memWeights := make([]float64, len(hosts))
	var names []string
	for i, h := range hosts {
		if h.ID == "" {
			cpuNames[i] = seriesHostCPU
			memNames[i] = seriesHostMem
		} else {
			cpuNames[i] = "host." + h.ID + ".cpu"
			memNames[i] = "host." + h.ID + ".mem"
		}
		names = append(names, cpuNames[i], memNames[i])
		cpuWeights[i] = float64(max(h.CPUs, 1))
		memWeights[i] = float64(h.MemMiB)
		if memWeights[i] <= 0 {
			memWeights[i] = 1
		}
	}

	res, err := e.q.Query(names, e.window, now)
	if err != nil {
		return Forecast{}, Forecast{}, err
	}

	cpuPts := weightedAverage(res, cpuNames, cpuWeights)
	cpu = ForecastCapacity(cpuPts, 100.0, 24*time.Hour, now)
	cpu.Metric = "cluster.cpu"

	memPts := weightedAverage(res, memNames, memWeights)
	mem = ForecastCapacity(memPts, 100.0, 24*time.Hour, now)
	mem.Metric = "cluster.mem"

	return cpu, mem, nil
}

// weightedAverage produces a single []Point where each time bucket holds the
// weighted average of the per-host values at that bucket. Hosts with missing
// data at a given bucket are excluded from the average for that bucket.
func weightedAverage(res history.Result, names []string, weights []float64) []Point {
	count := 0
	for _, name := range names {
		if vals := res.Series[name]; vals != nil && len(vals) > count {
			count = len(vals)
		}
	}
	if count == 0 {
		return nil
	}

	pts := make([]Point, 0, count)
	for i := 0; i < count; i++ {
		var sum, wSum float64
		for j, name := range names {
			vals := res.Series[name]
			if vals == nil || i >= len(vals) || vals[i] == nil {
				continue
			}
			v := *vals[i]
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			w := weights[j]
			if w <= 0 {
				w = 1
			}
			sum += v * w
			wSum += w
		}
		if wSum > 0 {
			t := time.Unix(res.Start+int64(i)*res.Step, 0).UTC()
			pts = append(pts, Point{T: t, V: sum / wSum})
		}
	}
	return pts
}

// PoolForecast forecasts storage pool usage. Storage pool usage is not recorded
// as a history series in the current recording design, so when the
// "pool.<name>.used" series is unavailable the function returns
// InsufficientData. Host disk throughput series exist but do not represent
// capacity, so they cannot substitute for pool usage data.
func (e *Engine) PoolForecast(p Pool, now time.Time) (Forecast, error) {
	name := "pool." + p.Name + ".used"
	res, err := e.q.Query([]string{name}, e.window, now)
	if err != nil {
		return Forecast{}, err
	}
	pts := Decode(res, name)
	if len(pts) < 50 || pts[len(pts)-1].T.Sub(pts[0].T) < 14*24*time.Hour {
		// No storage pool usage series are recorded by the live sampler.
		// Returning InsufficientData rather than fabricating capacity data.
		return Forecast{
			Metric:           name,
			InsufficientData: true,
			DaysUntilFull:    -1,
		}, nil
	}
	capacity := float64(p.CapacityBytes)
	if capacity <= 0 {
		return Forecast{
			Metric:           name,
			InsufficientData: true,
			DaysUntilFull:    -1,
		}, nil
	}
	f := ForecastCapacity(pts, capacity, 24*time.Hour, now)
	f.Metric = name
	return f, nil
}

// VMRightSize produces CPU and memory right-sizing recommendations for a
// virtual machine. The rng defaults to the engine window (30 days). Guest
// memory is recorded as a percentage of the balloon; it is converted back to
// MiB using the machine's MemMiB before calling RightSize. If the guest memory
// series is absent (no balloon agent), memory recommendations are skipped and
// a reason is appended.
func (e *Engine) VMRightSize(m Machine, rng time.Duration, now time.Time) Recommendation {
	if rng <= 0 {
		rng = e.window
	}
	cpuName := vmSeries(m.UUID, "cpu")
	memName := vmSeries(m.UUID, "mem")
	res, err := e.q.Query([]string{cpuName, memName}, rng, now)
	if err != nil {
		return Recommendation{
			UUID:            m.UUID,
			CurrentVCPUs:    m.VCPUs,
			CurrentMemMiB:   m.MemMiB,
			SuggestedVCPUs:  m.VCPUs,
			SuggestedMemMiB: m.MemMiB,
			Reasons:         []string{fmt.Sprintf("history query failed: %v", err)},
		}
	}

	cpuVals := pointsToValues(Decode(res, cpuName))
	memVals := pointsToValues(Decode(res, memName))

	rec := RightSize(cpuVals, memVals, struct {
		VCPUs  int
		MemMiB uint64
	}{VCPUs: m.VCPUs, MemMiB: m.MemMiB}, 0)

	rec.UUID = m.UUID
	if len(memVals) == 0 {
		rec.Reasons = append(rec.Reasons, "guest agent memory data unavailable")
	}
	return rec
}

// VMDetect runs anomaly detection over the last 24 h for six VM metric series,
// returning anomalies keyed by their short metric name (e.g. "disk.latency").
func (e *Engine) VMDetect(m Machine, now time.Time) map[string][]Anomaly {
	metrics := []string{"cpu", "disk.latency", "disk.read", "disk.write", "net.rx", "net.tx"}
	var names []string
	for _, metric := range metrics {
		names = append(names, vmSeries(m.UUID, metric))
	}
	res, err := e.q.Query(names, 24*time.Hour, now)
	if err != nil {
		return make(map[string][]Anomaly)
	}

	result := make(map[string][]Anomaly)
	for _, metric := range metrics {
		seriesName := vmSeries(m.UUID, metric)
		pts := Decode(res, seriesName)
		anomalies := Detect(pts, seriesName, 24*time.Hour, 5.0, now)
		if len(anomalies) > 0 {
			result[metric] = anomalies
		}
	}
	return result
}

// pointsToValues extracts the float64 values from a slice of Points, preserving
// order.
func pointsToValues(pts []Point) []float64 {
	vals := make([]float64, len(pts))
	for i, p := range pts {
		vals[i] = p.V
	}
	return vals
}
