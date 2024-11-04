package graphql

import (
	"github.com/stretchr/testify/suite"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"testing"
)

type _CompilerSuite struct {
	_MetadataSuite
	m *Metadata
	s *ast.Schema
}

func TestCompiler(t *testing.T) {
	suite.Run(t, new(_CompilerSuite))
}

func (my *_CompilerSuite) SetupSuite() {
	my._MetadataSuite.SetupSuite()
	var err error

	my.m, err = NewMetadata(my.v, my.d)
	my.Require().NoError(err)

	s := &ast.Source{Name: "metadata"}
	s.Input, err = my.m.Marshal()
	my.Require().NoError(err)

	my.s, err = gqlparser.LoadSchema(s)
	my.Require().NoError(err)
}

func (my *_CompilerSuite) TestCompiler() {
	c := NewCompiler(my.m)
	my.Require().NotNil(c)
}
