package gql

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
)

func TestPlanKeyDoesNotRetainDocumentSource(t *testing.T) {
	query := strings.Repeat("query field ", 10_000)
	first := planKeyFor("Q", query, "shape", "view")
	second := planKeyFor("Q", query, "shape", "view")
	different := planKeyFor("Q", query+"x", "shape", "view")

	require.Equal(t, first, second)
	require.NotEqual(t, first, different)
	require.LessOrEqual(t, unsafe.Sizeof(first), uintptr(96))
}
