package std

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"gorm.io/gorm/schema"
)

func init() {
	schema.RegisterSerializer("gson", &GsonSerializer{})
}

type GsonSerializer struct{}

// Scan unmarshals the database value into the destination field
func (my *GsonSerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue interface{}) (err error) {
	fieldValue := reflect.New(field.FieldType)

	if dbValue != nil {
		var bytes []byte
		switch v := dbValue.(type) {
		case []byte:
			bytes = v
		case string:
			bytes = []byte(v)
		default:
			return // Skip if not bytes or string
		}

		if len(bytes) > 0 {
			err = json.Unmarshal(bytes, fieldValue.Interface())
		}
	}

	field.ReflectValueOf(ctx, dst).Set(fieldValue.Elem())
	return
}

// Value marshals the field value into a JSON string/bytes for the database
func (my *GsonSerializer) Value(ctx context.Context, field *schema.Field, dst reflect.Value, fieldValue interface{}) (interface{}, error) {
	if fieldValue == nil {
		return nil, nil
	}

	val := reflect.ValueOf(fieldValue)
	// Dereference pointers to check for nil and get actual value
	for val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return nil, nil
		}
		val = val.Elem()
	}

	// Treat empty/whitespace-only string as nil
	if val.Kind() == reflect.String {
		if str := strings.TrimSpace(val.String()); str == "" {
			return nil, nil
		} else {
			// Marshal the trimmed string
			return json.Marshal(str)
		}
	}

	// For other types, marshal the original value
	return json.Marshal(fieldValue)
}
