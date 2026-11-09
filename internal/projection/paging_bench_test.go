package projection

import (
	"fmt"
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

// Package-level sink so the compiler cannot eliminate the measured results.
var pagingBenchSink any

func pagingBenchGraph(n int) *Graph {
	g := New()
	for i := 0; i < n; i++ {
		g.Apply(entityCreated(model.NewEntityID(), model.TypeHost, kv("host.id", fmt.Sprintf("h%d", i))))
	}
	return g
}

func BenchmarkPageEntitiesFirstPage(b *testing.B) {
	for _, n := range []int{100000, 1000000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			g := pagingBenchGraph(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				page, _, _ := g.PageEntitiesByFingerprint(model.TypeHost, "", 200)
				pagingBenchSink = page
			}
		})
	}
}

func BenchmarkPageEntitiesDeepPage(b *testing.B) {
	for _, n := range []int{100000, 1000000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			g := pagingBenchGraph(n)
			// Prepare the mid-park cursor outside the measured loop.
			cursor := func() string {
				mid, _, _ := g.PageEntitiesByFingerprint(model.TypeHost, "", n/2)
				if len(mid) == 0 {
					b.Fatalf("mid page is empty; cannot build a deep-page cursor")
				}
				// The prep slice is enclosed in this scope so it does not stay alive during the measurement.
				return mid[len(mid)-1].IdentityHash()
			}()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				page, _, _ := g.PageEntitiesByFingerprint(model.TypeHost, cursor, 200)
				pagingBenchSink = page
			}
		})
	}
}

func BenchmarkListEntitiesWholeType(b *testing.B) {
	for _, n := range []int{100000, 1000000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			g := pagingBenchGraph(n)
			b.ReportAllocs()
			b.ResetTimer()
			// This copies and sorts the whole type; the old GraphQL resolver called it once per page.
			for i := 0; i < b.N; i++ {
				pagingBenchSink = g.ListEntities(model.TypeHost)
			}
		})
	}
}
