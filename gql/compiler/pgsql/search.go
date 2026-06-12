// Package pgsql 全文搜索编译模块
// 三档模式（启动探测或配置指定）：
//   - tsvector：分词检索（装有pg_jieba/zhparser时自动选用其分词配置，中文真分词）
//   - trigram：pg_trgm三元组（官方镜像自带的contrib模块，中文子串检索可用且有索引加速）
//   - ilike：无任何扩展时的降级形态（正确但无索引加速与相关度）
//
// 搜索列由实体配置声明（metadata.classes.X.search），无声明的实体不渲染search参数
package pgsql

import (
	"fmt"
	"regexp"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
)

// 模式常量
const (
	searchTsvector = "tsvector"
	searchTrigram  = "trigram"
	searchIlike    = "ilike"
)

// configPattern 分词配置名白名单（拼入SQL字面量，防注入）
var configPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// searcher 单元的搜索编译参数
type searcher struct {
	mode    string
	config  string   // tsvector模式的text search配置
	columns []string // 参与搜索的列
	value   *ast.Value
}

// newSearcher 解析search参数；未携带返回nil
func newSearcher(ctx *compiler.Context, sc scope, args ast.ArgumentList) (*searcher, error) {
	arg := args.ForName(protocol.SEARCH)
	if arg == nil || arg.Value == nil {
		return nil, nil
	}
	if len(sc.class.Search) == 0 {
		return nil, fmt.Errorf("实体 %s 未声明搜索列（metadata.classes.%s.search）", sc.class.Name, sc.class.Name)
	}

	mode, config := ctx.SearchMode()
	if mode == "" {
		mode = searchIlike // 无数据库连接的纯编译场景
	}
	if mode == searchTsvector {
		if config == "" {
			config = "simple"
		}
		if !configPattern.MatchString(config) {
			return nil, fmt.Errorf("非法的分词配置名: %s", config)
		}
	}

	my := &searcher{mode: mode, config: config, value: arg.Value}
	for _, name := range sc.class.Search {
		field, ok := sc.class.Fields[name]
		if !ok || field.Column == "" {
			return nil, fmt.Errorf("搜索列 %s 不是实体 %s 的真实列", name, sc.class.Name)
		}
		my.columns = append(my.columns, field.Column)
	}
	return my, nil
}

// buildCondition 搜索过滤条件（各列OR）
func (my *searcher) buildCondition(d *Dialect, ctx *compiler.Context, sc scope) error {
	column := func(name string) {
		ctx.Quote(sc.qualifier).Write(`.`).Quote(name)
	}

	ctx.Write(`(`)
	for i, name := range my.columns {
		if i > 0 {
			ctx.Write(` OR `)
		}
		switch my.mode {
		case searchTsvector:
			ctx.Write(`to_tsvector('`, my.config, `', `)
			column(name)
			ctx.Write(`) @@ websearch_to_tsquery('`, my.config, `', `)
			if err := d.buildParam(ctx, my.value); err != nil {
				return err
			}
			ctx.Write(`)`)
		default: // trigram与ilike的过滤形态一致，差异在索引与排序
			column(name)
			ctx.Write(` ILIKE '%' || `)
			if err := d.buildParam(ctx, my.value); err != nil {
				return err
			}
			ctx.Write(` || '%'`)
		}
	}
	ctx.Write(`)`)
	return nil
}

// buildRank 相关度排序表达式；ilike模式无相关度返回false
func (my *searcher) buildRank(d *Dialect, ctx *compiler.Context, sc scope) (bool, error) {
	if my.mode == searchIlike {
		return false, nil
	}

	column := func(name string) {
		ctx.Quote(sc.qualifier).Write(`.`).Quote(name)
	}
	ctx.Write(`GREATEST(`)
	for i, name := range my.columns {
		if i > 0 {
			ctx.Write(`, `)
		}
		switch my.mode {
		case searchTsvector:
			ctx.Write(`ts_rank(to_tsvector('`, my.config, `', `)
			column(name)
			ctx.Write(`), websearch_to_tsquery('`, my.config, `', `)
			if err := d.buildParam(ctx, my.value); err != nil {
				return false, err
			}
			ctx.Write(`))`)
		case searchTrigram:
			ctx.Write(`similarity(`)
			column(name)
			ctx.Write(`, `)
			if err := d.buildParam(ctx, my.value); err != nil {
				return false, err
			}
			ctx.Write(`)`)
		}
	}
	ctx.Write(`) DESC`)
	return true, nil
}
