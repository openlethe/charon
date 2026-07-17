// Package metrics is a minimal in-process metrics registry with Prometheus
// text exposition. Counters and gauges are safe for concurrent use; there is
// no external dependency.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Counter is a monotonically increasing count.
type Counter struct{ v atomic.Int64 }

// Inc adds 1 to the counter.
func (c *Counter) Inc() { c.v.Add(1) }

// Add adds n to the counter.
func (c *Counter) Add(n int64) { c.v.Add(n) }

// Gauge is a value that can go up and down.
type Gauge struct{ v atomic.Int64 }

// Set replaces the gauge value.
func (g *Gauge) Set(n int64) { g.v.Store(n) }

// Inc/Dec adjust the gauge by one.
func (g *Gauge) Inc() { g.v.Add(1) }
func (g *Gauge) Dec() { g.v.Add(-1) }

var (
	registryMu sync.RWMutex
	counters   = map[string]*Counter{}
	gauges     = map[string]*Gauge{}
)

// Counter returns the named counter, creating it on first use.
func GetCounter(name string) *Counter {
	registryMu.RLock()
	c, ok := counters[name]
	registryMu.RUnlock()
	if ok {
		return c
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if c, ok := counters[name]; ok {
		return c
	}
	c = &Counter{}
	counters[name] = c
	return c
}

// Gauge returns the named gauge, creating it on first use.
func GetGauge(name string) *Gauge {
	registryMu.RLock()
	g, ok := gauges[name]
	registryMu.RUnlock()
	if ok {
		return g
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if g, ok := gauges[name]; ok {
		return g
	}
	g = &Gauge{}
	gauges[name] = g
	return g
}

// Inc adds 1 to the named counter.
func Inc(name string) { GetCounter(name).Inc() }

// SetGauge sets the named gauge.
func SetGauge(name string, n int64) { GetGauge(name).Set(n) }

// Expose renders the registry in Prometheus text exposition format.
func Expose() string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var sb strings.Builder
	names := make([]string, 0, len(counters))
	for name := range counters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&sb, "# TYPE %s counter\n%s %d\n", name, name, counters[name].v.Load())
	}
	gaugeNames := make([]string, 0, len(gauges))
	for name := range gauges {
		gaugeNames = append(gaugeNames, name)
	}
	sort.Strings(gaugeNames)
	for _, name := range gaugeNames {
		fmt.Fprintf(&sb, "# TYPE %s gauge\n%s %d\n", name, name, gauges[name].v.Load())
	}
	return sb.String()
}

// Charon metric names. Keep this list aligned with docs/observability.md.
const (
	AuthzDenials            = "charon_authz_denials_total"
	IdempotentReplays       = "charon_idempotent_replays_total"
	IdempotencyMismatches   = "charon_idempotency_mismatches_total"
	AuditOutboxBacklog      = "charon_audit_outbox_backlog"
	AuditAppendFailures     = "charon_audit_append_failures_total"
	LedgerAppendFailures    = "charon_ledger_append_failures_total"
	LedgerVerifyFailures    = "charon_ledger_verify_failures_total"
	MergeAttempts           = "charon_merge_attempts_total"
	MergeCASConflicts       = "charon_merge_cas_conflicts_total"
	ProposalCreated         = "charon_proposals_created_total"
	ProposalDecided         = "charon_proposals_decided_total"
	ConflictRetired         = "charon_conflicts_retired_total"
	UpstreamErrors          = "charon_upstream_errors_total"
	StrictParsingRejections = "charon_strict_parsing_rejections_total"
)
