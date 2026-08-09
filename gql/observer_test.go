package gql

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type recordingObserver struct {
	mu     sync.Mutex
	events []Observation
	panic  bool
}

func (my *recordingObserver) Observe(_ context.Context, event Observation) {
	if my.panic {
		panic("observer must not affect execution")
	}
	my.mu.Lock()
	my.events = append(my.events, event)
	my.mu.Unlock()
}

func (my *recordingObserver) phases() map[Phase]Observation {
	my.mu.Lock()
	defer my.mu.Unlock()
	out := make(map[Phase]Observation, len(my.events))
	for _, event := range my.events {
		out[event.Phase] = event
	}
	return out
}

func TestObserverReportsLowCardinalityExecutionPhases(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	observer := &recordingObserver{}
	executor.observer = observer
	body, err := executor.Execute(context.Background(), `{ users(limit: 1) { total } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), `"data"`)
	phases := observer.phases()
	for _, phase := range []Phase{PhaseExecute, PhasePlan, PhaseAuthorize, PhaseDatabase} {
		require.Equal(t, "SUCCESS", phases[phase].Code, phase)
		require.Positive(t, phases[phase].Duration)
	}
}

func TestObserverPanicCannotBreakExecution(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	executor.observer = &recordingObserver{panic: true}
	body, err := executor.Execute(context.Background(), `{ users(limit: 1) { total } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), `"data"`)
}

func TestBuilderObserverFreezesWithBuilder(t *testing.T) {
	builder := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy()
	require.NoError(t, builder.Observe(&recordingObserver{}))
	engine, err := builder.Build()
	require.NoError(t, err)
	defer engine.Close()
	require.Error(t, builder.Observe(&recordingObserver{}))
}

func BenchmarkObserverDisabled(b *testing.B) {
	executor := &Engine{}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if done := executor.startPhase(ctx, PhaseExecute); done != nil {
			b.Fatal("nil observer returned a callback")
		}
	}
}
