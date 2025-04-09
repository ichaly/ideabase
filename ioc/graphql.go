package ioc

import (
	"github.com/ichaly/ideabase/gql"
	// 导入MySQL方言实现
	_ "github.com/ichaly/ideabase/gql/compiler/mysql"
	// 导入PostgreSQL方言实现
	_ "github.com/ichaly/ideabase/gql/compiler/pgsql"
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
