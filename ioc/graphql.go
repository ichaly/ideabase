package ioc

import (
	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/std"
	"go.uber.org/fx"
)

func init() {
	Add(fx.Module("graphql",
		fx.Provide(
			gql.NewMetadata,
			gql.NewRenderer,
			gql.NewCompiler,
			fx.Annotate(
				gql.NewExecutor,
				fx.As(new(std.Plugin)),
				fx.ResultTags(`group:"plugin"`),
			),
		),
	))
}
