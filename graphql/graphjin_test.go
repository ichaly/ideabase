package graphql

import (
	"database/sql"
	"encoding/json"
	"github.com/dosco/graphjin/core/v3"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/ichaly/ideabase/graphql/internal/intro"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/suite"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"net/http"
	"testing"
)

type _GraphJinSuite struct {
	_MetadataSuite
	db   *sql.DB
	meta *Metadata
}

func TestGraphJin(t *testing.T) {
	suite.Run(t, new(_GraphJinSuite))
}

func (my *_GraphJinSuite) SetupSuite() {
	var err error
	my._MetadataSuite.SetupSuite()

	my.meta, err = NewMetadata(my.v, my.d)
	my.Require().NoError(err)

	my.db, err = sql.Open("pgx", "postgres://postgres:postgres@localhost:5678/demo?sslmode=disable")
	my.Require().NoError(err)
}

func (my *_GraphJinSuite) TestGraphJin() {
	gj, err := core.NewGraphJin(&core.Config{
		EnableCamelcase: true,
		DisableAgg:      true,
		DisableFuncs:    true,
	}, my.db)
	my.Require().NoError(err)

	r := gin.Default()
	r.Match([]string{http.MethodGet, http.MethodPost}, "/v0/graphql", func(ctx *gin.Context) {
		var req struct {
			Query     string          `form:"query"`
			Operation string          `form:"operationName" json:"operationName"`
			Variables json.RawMessage `form:"variables"`
		}
		_ = ctx.ShouldBindBodyWith(&req, binding.JSON)
		res, _ := gj.GraphQL(ctx, req.Query, req.Variables, nil)
		println(res.SQL())
		ctx.JSON(http.StatusOK, res)
	})
	r.Match([]string{http.MethodGet, http.MethodPost}, "/v1/graphql", func(ctx *gin.Context) {
		schema, err := my.meta.Marshal()
		my.Require().NoError(err)

		s, err := gqlparser.LoadSchema(&ast.Source{Name: "schema", Input: schema})
		my.Require().NoError(err)

		ctx.JSON(http.StatusOK, gin.H{"data": intro.New(s)})
	})
	_ = r.Run(":8081")
}
