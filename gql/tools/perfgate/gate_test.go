package perfgate

import (
	"github.com/stretchr/testify/require"
	"strconv"
	"strings"
	"testing"
)

func benchmark(value int) string {
	var out strings.Builder
	for range 10 {
		out.WriteString("BenchmarkHot-8 1000  ")
		out.WriteString(strconv.Itoa(value))
		out.WriteString(" ns/op 8 B/op 1 allocs/op\n")
	}
	return out.String()
}
func TestCompareRejectsSignificantRegressionPastBudget(t *testing.T) {
	require.ErrorContains(t, Compare(strings.NewReader(benchmark(100)), strings.NewReader(benchmark(120)), 0.10), "Hot-8")
}
func TestCompareAcceptsChangeWithinBudget(t *testing.T) {
	require.NoError(t, Compare(strings.NewReader(benchmark(100)), strings.NewReader(benchmark(109)), 0.10))
}
func TestCompareRequiresTenSamples(t *testing.T) {
	require.ErrorContains(t, Compare(strings.NewReader(strings.Repeat("BenchmarkHot-8 1000 100 ns/op\n", 9)), strings.NewReader(benchmark(100)), 0.10), "10 samples")
}
func TestCompareRejectsMissingBenchmark(t *testing.T) {
	require.ErrorContains(t, Compare(strings.NewReader(benchmark(100)), strings.NewReader("BenchmarkOther-8 1000 100 ns/op\n"+strings.Repeat("BenchmarkOther-8 1000 100 ns/op\n", 9)), 0.10), "missing")
}
func TestCompareRejectsBenchmarkAddedToHead(t *testing.T) {
	require.ErrorContains(t, Compare(strings.NewReader(benchmark(100)), strings.NewReader(benchmark(100)+strings.ReplaceAll(benchmark(100), "Hot", "New")), 0.10), "missing from base")
}
