package std

import (
	"database/sql"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

const timeLayout = "2006-01-02 15:04:05"

type DataTime struct {
	sql.NullTime
}

type FlagTime struct {
	gorm.DeletedAt
}

func (my DataTime) MarshalJSON() ([]byte, error) {
	return encodeTime(my.Valid, my.Time), nil
}

func (my *DataTime) UnmarshalJSON(data []byte) error {
	if my == nil {
		return nil
	}
	parsed, err := parseTime(data)
	if err != nil {
		return err
	}
	assignTime(&my.Valid, &my.Time, parsed)
	return nil
}

func (my FlagTime) MarshalJSON() ([]byte, error) {
	return encodeTime(my.Valid, my.Time), nil
}

func (my *FlagTime) UnmarshalJSON(data []byte) error {
	if my == nil {
		return nil
	}
	parsed, err := parseTime(data)
	if err != nil {
		return err
	}
	assignTime(&my.Valid, &my.Time, parsed)
	return nil
}

func parseTime(data []byte) (time.Time, error) {
	token := strings.Trim(string(data), "\" \t\r\n")
	if token == "" || token == "null" {
		return time.Time{}, nil
	}
	return time.ParseInLocation(timeLayout, token, time.Local)
}

func encodeTime(valid bool, t time.Time) []byte {
	if !valid || t.IsZero() {
		return []byte("null")
	}
	return strconv.AppendQuote(nil, t.Format(timeLayout))
}

func assignTime(valid *bool, target *time.Time, val time.Time) {
	if valid == nil || target == nil {
		return
	}
	if val.IsZero() {
		*target = time.Time{}
		*valid = false
		return
	}
	*target = val
	*valid = true
}
