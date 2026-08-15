package gql

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The root package deliberately has one transport path: HTTP and SSE live in
// gql/transport. Keep the deprecated Fiber/fasthttp adapters out of production
// files so new callers cannot accidentally depend on a second protocol stack.
func TestRootProductionHasNoLegacyTransportAdapters(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate architecture test")
	}
	root := filepath.Dir(thisFile)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, forbidden := range []string{
			"github.com/gofiber/fiber",
			"github.com/valyala/fasthttp",
			"github.com/fasthttp/websocket",
			"func (my *Engine) Bind(",
			"func (my *Engine) Handler(",
			"func (my *Engine) UpgradeContext(",
		} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s contains removed legacy transport API %q", entry.Name(), forbidden)
			}
		}
	}
}
