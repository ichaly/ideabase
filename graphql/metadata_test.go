package graphql

import (
	"github.com/ichaly/ideabase/service"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
	"testing"
)

type _MetadataSuite struct {
	suite.Suite
	d *gorm.DB
	v *viper.Viper
}

func TestMetadata(t *testing.T) {
	suite.Run(t, new(_MetadataSuite))
}

func (my *_MetadataSuite) SetupSuite() {
	var err error
	my.v, err = service.NewViper("../configs/dev.yml")
	my.Require().NoError(err)
	my.d, err = service.NewConnect(my.v, []gorm.Plugin{service.NewSonyFlake()}, []interface{}{})
	my.Require().NoError(err)
}

func (my *_MetadataSuite) TestMetadata() {
	metadata, err := NewMetadata(my.v, my.d)
	my.Require().NoError(err)
	str, err := metadata.Marshal()
	my.Require().NoError(err)
	my.T().Log(str)
}
