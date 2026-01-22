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

	// Treat empty string (trimmed) as nil
	if str, ok := fieldValue.(string); ok {
		if value := strings.TrimSpace(str); value == "" {
			return nil, nil
		} else {
			fieldValue = value
		}
	}

	value := reflect.ValueOf(fieldValue)
	if value.Kind() == reflect.Ptr && value.IsNil() {
		return nil, nil
	}

	return json.Marshal(fieldValue)
}
