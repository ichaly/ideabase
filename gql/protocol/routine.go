package protocol

import "fmt"

// RoutineArgument describes one declared routine argument, including its
// PostgreSQL mode and default expression.
type RoutineArgument struct {
	Name    string `json:"name,omitempty"`
	Mode    string `json:"mode"`
	Type    string `json:"type"`
	Default string `json:"default,omitempty"`
}

const (
	RoutineArgIn       = "in"
	RoutineArgOut      = "out"
	RoutineArgInOut    = "inout"
	RoutineArgVariadic = "variadic"
	RoutineArgTable    = "table"
)

type RoutineKind string

const (
	RoutineFunction  RoutineKind = "function"
	RoutineProcedure RoutineKind = "procedure"
)

type RoutineVolatility string

const (
	RoutineVolatilityImmutable RoutineVolatility = "immutable"
	RoutineVolatilityStable    RoutineVolatility = "stable"
	RoutineVolatilityVolatile  RoutineVolatility = "volatile"
)

// Routine is read-only PostgreSQL routine catalog metadata. It is deliberately
// independent from Class so it cannot accidentally become a GraphQL field.
type Routine struct {
	Schema          string            `json:"schema"`
	Name            string            `json:"name"`
	Kind            RoutineKind       `json:"kind"`
	Identity        string            `json:"identity"`
	Arguments       []RoutineArgument `json:"arguments,omitempty"`
	ReturnType      string            `json:"returnType,omitempty"`
	ReturnsSet      bool              `json:"returnsSet,omitempty"`
	Volatility      RoutineVolatility `json:"volatility"`
	SecurityDefiner bool              `json:"securityDefiner,omitempty"`
}

func (routine *Routine) Validate() error {
	if routine == nil || routine.Schema == "" || routine.Name == "" {
		return fmt.Errorf("routine缺少schema或name")
	}
	if routine.Kind != RoutineFunction && routine.Kind != RoutineProcedure {
		return fmt.Errorf("routine %s kind无效: %s", routine.Name, routine.Kind)
	}
	if routine.Volatility != RoutineVolatilityImmutable && routine.Volatility != RoutineVolatilityStable && routine.Volatility != RoutineVolatilityVolatile {
		return fmt.Errorf("routine %s volatility无效: %s", routine.Name, routine.Volatility)
	}
	if routine.Kind == RoutineFunction && routine.ReturnType == "" {
		return fmt.Errorf("function %s缺少return type", routine.Name)
	}
	for _, argument := range routine.Arguments {
		if argument.Type == "" || (argument.Mode != RoutineArgIn && argument.Mode != RoutineArgOut && argument.Mode != RoutineArgInOut && argument.Mode != RoutineArgVariadic && argument.Mode != RoutineArgTable) {
			return fmt.Errorf("routine %s参数无效: %s", routine.Name, argument.Name)
		}
	}
	return nil
}

// RoutineHoster is optional so existing third-party Hoster implementations
// remain source-compatible.
type RoutineHoster interface {
	PutRoutine(key string, routine *Routine) error
}
