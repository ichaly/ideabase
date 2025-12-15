package std

import (
	"strconv"
	"strings"
	"unsafe"

	jsoniter "github.com/json-iterator/go"
	"github.com/modern-go/reflect2"
	"github.com/sqids/sqids-go"

	"github.com/ichaly/ideabase/utl"
)

var shortId, _ = sqids.New()

func parseIdToken(token string) (Id, error) {
	token = strings.TrimSpace(token)
	if token == "" || token == "null" {
		return 0, nil
	}
	// 数字优先：避免 "123" 被误解为可解码的 shortId
	if v, err := strconv.ParseUint(token, 10, 64); err == nil {
		return Id(v), nil
	}
	if decoded := shortId.Decode(token); len(decoded) > 0 {
		return Id(decoded[0]), nil
	}
	return 0, strconv.ErrSyntax
}

var fiberJSON = func() jsoniter.API {
	api := utl.NewJSON()

	// Fiber 输出：std.Id 统一编码为 shortId 字符串。
	api.RegisterExtension(jsoniter.EncoderExtension{
		reflect2.TypeOf(Id(0)): idShortEncoder{},
	})
	api.RegisterExtension(jsoniter.DecoderExtension{
		reflect2.TypeOf(Id(0)): idShortDecoder{},
	})

	return api
}()

type idShortEncoder struct{}

func (idShortEncoder) Encode(ptr unsafe.Pointer, stream *jsoniter.Stream) {
	id := *(*Id)(ptr)
	if str, err := shortId.Encode([]uint64{uint64(id)}); err == nil {
		stream.WriteString(str)
		return
	}
	stream.WriteString(strconv.FormatUint(uint64(id), 10))
}

func (idShortEncoder) IsEmpty(ptr unsafe.Pointer) bool {
	return *(*Id)(ptr) == 0
}

type idShortDecoder struct{}

func (idShortDecoder) Decode(ptr unsafe.Pointer, iter *jsoniter.Iterator) {
	switch iter.WhatIsNext() {
	case jsoniter.NilValue:
		iter.ReadNil()
		*(*Id)(ptr) = 0
	case jsoniter.NumberValue:
		*(*Id)(ptr) = Id(iter.ReadUint64())
	case jsoniter.StringValue:
		token := iter.ReadString()
		id, err := parseIdToken(token)
		if err != nil {
			iter.ReportError("std.Id", err.Error())
			return
		}
		*(*Id)(ptr) = id
	default:
		iter.ReportError("std.Id", "expected string/number/null")
	}
}
