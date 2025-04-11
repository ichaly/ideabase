package pgsql

import (
	"fmt"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"
)

// 测试用的schema定义
const testSchema = `
schema {
    query: Query
}

type Query {
    users(limit: Int, offset: Int): [User!]!
}

type User {
    id: ID!
    name: String!
    email: String!
}
`

// parseQuery 解析GraphQL查询
func parseQuery(query string) (*ast.OperationDefinition, error) {
	// 解析schema
	schemaAST, err := gqlparser.LoadSchema(&ast.Source{
		Input: testSchema,
		Name:  "schema.graphql",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load schema: %v", err)
	}

	// 解析查询
	queryDoc, err := parser.ParseQuery(&ast.Source{
		Input: query,
		Name:  "query.graphql",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to parse query: %v", err)
	}

	// 验证查询
	errs := validator.Validate(schemaAST, queryDoc)
	if len(errs) > 0 {
		return nil, fmt.Errorf("query validation failed: %v", errs)
	}

	// 返回第一个操作
	if len(queryDoc.Operations) == 0 {
		return nil, fmt.Errorf("no operations found in query")
	}
	return queryDoc.Operations[0], nil
}
