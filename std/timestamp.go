package std

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

const timeLayout = "2006-01-02 15:04:05"

var timeLayouts = [...]string{
	time.RFC3339Nano,
	time.RFC3339,
	timeLayout,
	"2006-01-02",
}

type DateTime struct {
	sql.NullTime
}

type FlagTime struct {
	gorm.DeletedAt
}

func (my DateTime) MarshalJSON() ([]byte, error) {
	return encodeTime(my.Valid, my.Time), nil
}

func (my *DateTime) UnmarshalJSON(data []byte) error {
	if my == nil {
		return nil
	}
	parsed, present, err := parseTimeToken(string(data))
	if err != nil {
		return err
	}
	if !present {
		assignTime(&my.Valid, &my.Time, time.Time{})
		return nil
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
	parsed, present, err := parseTimeToken(string(data))
	if err != nil {
		return err
	}
	if !present {
		assignTime(&my.Valid, &my.Time, time.Time{})
		return nil
	}
	assignTime(&my.Valid, &my.Time, parsed)
	return nil
}

func parseTimeToken(raw string) (time.Time, bool, error) {
	token := strings.Trim(raw, "\" \t\r\n")
	if token == "" || token == "null" {
		return time.Time{}, false, nil
	}

	if unix, err := strconv.ParseInt(token, 10, 64); err == nil {
		// 兼容秒/毫秒时间戳
		const maxUnixMilli = int64(253402300799999) // 9999-12-31 23:59:59.999
		const maxUnixSec = int64(253402300799)      // 9999-12-31 23:59:59
		if unix > 1e12 {
			if unix > maxUnixMilli {
				return time.Time{}, true, fmt.Errorf("时间戳过大: %s", token)
			}
			return time.UnixMilli(unix), true, nil
		}
		if unix > maxUnixSec {
			return time.Time{}, true, fmt.Errorf("时间戳过大: %s", token)
		}
		return time.Unix(unix, 0), true, nil
	}

	for _, layout := range timeLayouts {
		if t, err := time.ParseInLocation(layout, token, time.Local); err == nil {
			return t, true, nil
		}
	}

	return time.Time{}, true, fmt.Errorf("时间格式不正确: %s", token)
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
