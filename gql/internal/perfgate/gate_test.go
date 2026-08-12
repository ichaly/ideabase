package perfgate

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
	err := Compare(strings.NewReader(benchmark(100)), strings.NewReader(benchmark(120)), 0.10)
	require.ErrorContains(t, err, "Hot-8")
}

func TestCompareAcceptsChangeWithinBudget(t *testing.T) {
	require.NoError(t, Compare(strings.NewReader(benchmark(100)), strings.NewReader(benchmark(109)), 0.10))
}

func TestCompareRequiresTenSamples(t *testing.T) {
	err := Compare(strings.NewReader(strings.Repeat("BenchmarkHot-8 1000 100 ns/op\n", 9)), strings.NewReader(benchmark(100)), 0.10)
	require.ErrorContains(t, err, "10 samples")
}

func TestCompareRejectsMissingBenchmark(t *testing.T) {
	err := Compare(strings.NewReader(benchmark(100)), strings.NewReader("BenchmarkOther-8 1000 100 ns/op\n"+strings.Repeat("BenchmarkOther-8 1000 100 ns/op\n", 9)), 0.10)
	require.ErrorContains(t, err, "missing")
}

func TestCompareRejectsBenchmarkAddedToHead(t *testing.T) {
	err := Compare(strings.NewReader(benchmark(100)), strings.NewReader(benchmark(100)+strings.ReplaceAll(benchmark(100), "Hot", "New")), 0.10)
	require.ErrorContains(t, err, "missing from base")
}
