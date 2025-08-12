package utl

import (
	"testing"
)

// 测试结构体
type TestStruct struct {
	// 主名大小写不敏感测试
	Speed string `json:"speed,omitempty"`

	// 别名测试
	Wind string `json:"wind,from=windDir|wind_direction"`

	// 混合测试：主名大小写不敏感 + 别名
	Temp string `json:"temp,from=temperature|Temperature,omitempty"`

	// 多个from测试
	Humidity string `json:"humidity,from=hum,from=humid"`

	// 无别名字段
	Icon string `json:"icon"`

	// 忽略字段
	Ignore string `json:"-"`
}

func TestMainNameCaseInsensitive(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"小写主名", `{"speed":"10"}`, "10"},
		{"大写主名", `{"Speed":"20"}`, "20"},
		{"全大写主名", `{"SPEED":"30"}`, "30"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var obj TestStruct
			if err := Unmarshal([]byte(tt.json), &obj); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if obj.Speed != tt.want {
				t.Errorf("期望 Speed=%s, 实际 %s", tt.want, obj.Speed)
			}
		})
	}
}

func TestAliases(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"主名", `{"wind":"north"}`, "north"},
		{"别名1", `{"windDir":"south"}`, "south"},
		{"别名2", `{"wind_direction":"east"}`, "east"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var obj TestStruct
			if err := Unmarshal([]byte(tt.json), &obj); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if obj.Wind != tt.want {
				t.Errorf("期望 Wind=%s, 实际 %s", tt.want, obj.Wind)
			}
		})
	}
}

func TestMixedCaseAndAliases(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"小写主名", `{"temp":"25"}`, "25"},
		{"大写主名", `{"Temp":"26"}`, "26"},
		{"全大写主名", `{"TEMP":"27"}`, "27"},
		{"小写别名", `{"temperature":"28"}`, "28"},
		{"大写别名", `{"Temperature":"29"}`, "29"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var obj TestStruct
			if err := Unmarshal([]byte(tt.json), &obj); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if obj.Temp != tt.want {
				t.Errorf("期望 Temp=%s, 实际 %s", tt.want, obj.Temp)
			}
		})
	}
}

func TestMultipleFromOptions(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"主名", `{"humidity":"60"}`, "60"},
		{"别名1", `{"hum":"65"}`, "65"},
		{"别名2", `{"humid":"70"}`, "70"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var obj TestStruct
			if err := Unmarshal([]byte(tt.json), &obj); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if obj.Humidity != tt.want {
				t.Errorf("期望 Humidity=%s, 实际 %s", tt.want, obj.Humidity)
			}
		})
	}
}

func TestSerialization(t *testing.T) {
	obj := TestStruct{
		Speed:    "10",
		Wind:     "north",
		Temp:     "25",
		Humidity: "60",
		Icon:     "sunny",
	}

	data, err := Marshal(obj)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}

	// 验证序列化结果只使用主名
	expected := `{"speed":"10","wind":"north","temp":"25","humidity":"60","icon":"sunny"}`
	var expectedObj, actualObj map[string]string
	if err := Unmarshal([]byte(expected), &expectedObj); err != nil {
		t.Fatalf("解析期望结果失败: %v", err)
	}
	if err := Unmarshal(data, &actualObj); err != nil {
		t.Fatalf("解析实际结果失败: %v", err)
	}

	for k, v := range expectedObj {
		if actualObj[k] != v {
			t.Errorf("键 %s: 期望 %s, 实际 %s", k, v, actualObj[k])
		}
	}
}

func TestIgnoredFields(t *testing.T) {
	json := `{"speed":"10","ignore":"should_be_ignored"}`
	var obj TestStruct
	if err := Unmarshal([]byte(json), &obj); err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if obj.Speed != "10" {
		t.Errorf("期望 Speed=10, 实际 %s", obj.Speed)
	}
	if obj.Ignore != "" {
		t.Errorf("期望 Ignore 为空, 实际 %s", obj.Ignore)
	}
}

func TestEmptyAndInvalidAliases(t *testing.T) {
	type TestEmptyAlias struct {
		Field1 string `json:"field1,from="`       // 空别名
		Field2 string `json:"field2,from=|-|  |"` // 包含空值和-的别名
		Field3 string `json:"field3,from=valid|"` // 混合有效和无效别名
	}

	json := `{"field1":"test1","valid":"test3"}`
	var obj TestEmptyAlias
	if err := Unmarshal([]byte(json), &obj); err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if obj.Field1 != "test1" {
		t.Errorf("期望 Field1=test1, 实际 %s", obj.Field1)
	}
	if obj.Field3 != "test3" {
		t.Errorf("期望 Field3=test3, 实际 %s", obj.Field3)
	}
}

func TestComplexJSON(t *testing.T) {
	json := `{
		"SPEED": "100",
		"windDir": "northwest", 
		"Temperature": "30",
		"hum": "80",
		"icon": "cloudy"
	}`

	var obj TestStruct
	if err := Unmarshal([]byte(json), &obj); err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if obj.Speed != "100" {
		t.Errorf("期望 Speed=100, 实际 %s", obj.Speed)
	}
	if obj.Wind != "northwest" {
		t.Errorf("期望 Wind=northwest, 实际 %s", obj.Wind)
	}
	if obj.Temp != "30" {
		t.Errorf("期望 Temp=30, 实际 %s", obj.Temp)
	}
	if obj.Humidity != "80" {
		t.Errorf("期望 Humidity=80, 实际 %s", obj.Humidity)
	}
	if obj.Icon != "cloudy" {
		t.Errorf("期望 Icon=cloudy, 实际 %s", obj.Icon)
	}
}
