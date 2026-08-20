package metadata

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
)

func TestLoaderIdentityAndNilSupport(t *testing.T) {
	loaders := []protocol.Loader{
		NewConfigLoader(nil),
		NewFileLoader(nil),
		NewPgsqlLoader(nil, nil),
		NewMysqlLoader(nil, nil),
	}
	for _, loader := range loaders {
		if loader.Name() == "" || loader.Priority() <= 0 || loader.Support() {
			t.Fatalf("invalid nil loader contract: %T name=%q priority=%d support=%v", loader, loader.Name(), loader.Priority(), loader.Support())
		}
	}
	if NewConfigLoader(&internal.Config{}).Support() {
		t.Fatal("empty config should not be supported")
	}
}
