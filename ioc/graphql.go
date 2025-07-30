package ioc

import (
	// 导入MySQL方言实现
	_ "github.com/ichaly/ideabase/gql/compiler/mysql"
	// 导入PostgreSQL方言实现
	_ "github.com/ichaly/ideabase/gql/compiler/pgsql"
)

func init() {
	//Add(fx.Module("graphql",
	//	fx.Provide(
	//		gql.NewMetadata,
	//		gql.NewRenderer,
	//		gql.NewCompiler,
	//		fx.Annotate(
	//			gql.NewExecutor,
	//			fx.As(new(std.Plugin)),
	//			fx.ResultTags(`group:"plugin"`),
	//		),
	//	),
	//))
}
