package perfgate

import (
	"errors"
	"fmt"
	"io"
	"slices"

	"golang.org/x/perf/benchfmt"
	"golang.org/x/perf/benchmath"
)

const minimumSamples = 10

// Compare rejects statistically significant time regressions beyond budget.
// Both inputs must contain the same Go benchmarks with at least ten samples.
func Compare(base, head io.Reader, budget float64) error {
	if budget < 0 {
		return fmt.Errorf("performance budget must be non-negative")
	}
	baseSamples, err := readSamples(base, "base")
	if err != nil {
		return err
	}
	headSamples, err := readSamples(head, "head")
	if err != nil {
		return err
	}
	if len(baseSamples) == 0 {
		return fmt.Errorf("no ns/op benchmarks found")
	}
	thresholds := benchmath.DefaultThresholds
	thresholds.CompareAlpha = 0.01
	var failures []error
	for name, baseValues := range baseSamples {
		headValues, ok := headSamples[name]
		if !ok {
			return fmt.Errorf("benchmark %s missing from head", name)
		}
		if len(baseValues) < minimumSamples || len(headValues) < minimumSamples {
			return fmt.Errorf("benchmark %s requires at least %d samples", name, minimumSamples)
		}
		old := benchmath.NewSample(slices.Clone(baseValues), &thresholds)
		current := benchmath.NewSample(slices.Clone(headValues), &thresholds)
		comparison := benchmath.AssumeNothing.Compare(old, current)
		oldCenter := benchmath.AssumeNothing.Summary(old, 0.95).Center
		newCenter := benchmath.AssumeNothing.Summary(current, 0.95).Center
		delta := newCenter/oldCenter - 1
		if comparison.P < comparison.Alpha && delta > budget {
			failures = append(failures, fmt.Errorf("benchmark %s regressed %.2f%% (budget %.2f%%)", name, delta*100, budget*100))
		}
	}
	for name := range headSamples {
		if _, ok := baseSamples[name]; !ok {
			return fmt.Errorf("benchmark %s missing from base", name)
		}
	}
	return errors.Join(failures...)
}

func readSamples(input io.Reader, name string) (map[string][]float64, error) {
	samples := make(map[string][]float64)
	reader := benchfmt.NewReader(input, name)
	for reader.Scan() {
		result, ok := reader.Result().(*benchfmt.Result)
		if !ok {
			continue
		}
		for _, value := range result.Values {
			if value.Unit == "sec/op" {
				samples[string(result.Name)] = append(samples[string(result.Name)], value.Value)
			}
		}
	}
	if err := reader.Err(); err != nil {
		return nil, fmt.Errorf("read %s benchmarks: %w", name, err)
	}
	return samples, nil
}
