package std

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

const timeLayout = "2006-01-02 15:04:05"

type Timestamp struct {
	gorm.DeletedAt
}

func (my Timestamp) MarshalJSON() ([]byte, error) {
	if !my.Valid || my.Time.IsZero() {
		return []byte("null"), nil
	}
	return strconv.AppendQuote(nil, my.Time.Format(timeLayout)), nil
}

func (my *Timestamp) UnmarshalJSON(data []byte) error {
	if my == nil {
		return nil
	}
	parsed, err := parseTimeToken(strings.Trim(string(data), "\" \t\r\n"))
	if err != nil {
		return err
	}
	my.set(parsed)
	return nil
}

func (my Timestamp) Value() (driver.Value, error) {
	if !my.Valid || my.Time.IsZero() {
		return nil, nil
	}
	return my.Time, nil
}

func (my *Timestamp) Scan(value any) error {
	if my == nil {
		return fmt.Errorf("std.Timestamp: Scan on nil pointer")
	}
	switch v := value.(type) {
	case time.Time:
		my.set(v)
	case []byte:
		parsed, err := parseTimeToken(strings.TrimSpace(string(v)))
		if err != nil {
			return err
		}
		my.set(parsed)
	case string:
		parsed, err := parseTimeToken(strings.TrimSpace(v))
		if err != nil {
			return err
		}
		my.set(parsed)
	case nil:
		my.set(time.Time{})
	default:
		return fmt.Errorf("std.Timestamp: unsupported Scan type %T", value)
	}
	return nil
}

func (Timestamp) GormDataType() string { return "time" }

func (my *Timestamp) set(val time.Time) {
	if my == nil {
		return
	}
	if val.IsZero() {
		my.Time = time.Time{}
		my.Valid = false
		return
	}
	my.Time = val
	my.Valid = true
}

func parseTimeToken(token string) (time.Time, error) {
	if token == "" || token == "null" {
		return time.Time{}, nil
	}
	return time.ParseInLocation(timeLayout, token, time.Local)
}
