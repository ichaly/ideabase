package std

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/go-playground/locales"
	"github.com/go-playground/locales/en"
	"github.com/go-playground/locales/zh"
	ut "github.com/go-playground/universal-translator"
	"github.com/go-playground/validator/v10"
	enTranslations "github.com/go-playground/validator/v10/translations/en"
	zhTranslations "github.com/go-playground/validator/v10/translations/zh"
)

const (
	// LocaleEnglish 英文语言码
	LocaleEnglish = "en"
	// LocaleChinese 中文语言码
	LocaleChinese = "zh"
)

// FieldError 字段级错误信息
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError 封装后的校验错误
type ValidationError struct {
	fields []FieldError
	detail string
	err    error
}

func (my *ValidationError) Error() string {
	if my.detail != "" {
		return my.detail
	}
	if my.err != nil {
		return my.err.Error()
	}
	return "参数校验失败"
}

func (my *ValidationError) Unwrap() error { return my.err }

func (my *ValidationError) Extensions() Extension {
	if my == nil {
		return nil
	}
	ext := make(Extension)
	if len(my.fields) > 0 {
		for _, f := range my.fields {
			ext[f.Field] = f.Message
		}
	}
	return ext
}

// Validator 公共校验器，封装了 go-playground/validator 并支持多语言翻译
type Validator struct {
	validate      *validator.Validate
	universal     *ut.UniversalTranslator
	defaultLocale string
	registered    map[string]struct{}
	mutex         sync.RWMutex
}

// NewValidator 创建带默认语言（简体中文）的校验器实例
func NewValidator() (*Validator, error) {
	enLocale, zhLocale := en.New(), zh.New()
	v := &Validator{
		validate:      validator.New(),
		universal:     ut.New(enLocale, enLocale, zhLocale),
		defaultLocale: LocaleChinese,
		registered:    make(map[string]struct{}),
	}

	// 注册默认翻译
	if err := v.RegisterTranslation(enLocale, enTranslations.RegisterDefaultTranslations); err != nil {
		return nil, err
	}
	if err := v.RegisterTranslation(zhLocale, zhTranslations.RegisterDefaultTranslations); err != nil {
		return nil, err
	}

	// 先取 label 标签若为空，则退回到结构体字段原名。
	// 这样一来，当校验失败时，错误信息里的字段名会更贴近接口返回或业务描述，避免直接暴露 Go 字段名。
	v.validate.RegisterTagNameFunc(func(field reflect.StructField) string {
		if label := strings.TrimSpace(field.Tag.Get("label")); label != "" {
			return label
		}
		return field.Name
	})

	return v, nil
}

// RegisterValidation 包装原生注册校验函数
func (my *Validator) RegisterValidation(tag string, fn validator.Func, callValidationEvenIfNull ...bool) error {
	return my.validate.RegisterValidation(tag, fn, callValidationEvenIfNull...)
}

// RegisterTranslation 注册指定语言翻译，必要时自动挂载 translator。
func (my *Validator) RegisterTranslation(trans locales.Translator, register func(*validator.Validate, ut.Translator) error) error {
	if trans == nil {
		return fmt.Errorf("validator: translator 不能为空")
	}
	if register == nil {
		return fmt.Errorf("validator: register 函数不能为空")
	}

	locale := strings.TrimSpace(trans.Locale())
	if locale == "" {
		return fmt.Errorf("validator: translator locale 不能为空")
	}

	my.mutex.Lock()
	defer my.mutex.Unlock()
	if err := my.universal.AddTranslator(trans, true); err != nil {
		return err
	}
	if _, ok := my.registered[locale]; ok {
		return nil
	}

	translator, found := my.universal.GetTranslator(locale)
	if !found {
		return fmt.Errorf("validator: 未支持的语言代码 %q", locale)
	}

	if err := register(my.validate, translator); err != nil {
		return err
	}

	my.registered[locale] = struct{}{}
	return nil
}

// SetDefaultLocale 设置默认语言
func (my *Validator) SetDefaultLocale(locale string) error {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return fmt.Errorf("validator: 语言代码不能为空")
	}
	if _, found := my.universal.GetTranslator(locale); !found {
		return fmt.Errorf("validator: 未支持的语言代码 %q", locale)
	}
	my.mutex.Lock()
	defer my.mutex.Unlock()
	my.defaultLocale = locale
	return nil
}

// Struct 校验结构体并返回国际化后的错误
func (my *Validator) Struct(target any) error {
	if err := my.validate.Struct(target); err != nil {
		if converted, ok := my.translateError(target, err); ok {
			return converted.err
		}
		return err
	}
	return nil
}

// Var 校验单个字段并返回国际化后的错误
func (my *Validator) Var(field any, tag string) error {
	if err := my.validate.Var(field, tag); err != nil {
		if converted, ok := my.translateError(nil, err); ok {
			return converted.err
		}
		return err
	}
	return nil
}

// Check 执行结构体校验并返回高阶错误，业务侧只需判空。
func (my *Validator) Check(payload any) error {
	if err := my.Struct(payload); err != nil {
		var e *ValidationError
		if errors.As(err, &e) {
			return e
		}
		return &ValidationError{detail: err.Error(), err: err}
	}
	return nil
}

func buildFieldLookup(payload any) map[string]string {
	if payload == nil {
		return nil
	}

	typ := reflect.TypeOf(payload)
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}

	result := make(map[string]string, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		jsonKey := field.Tag.Get("json")
		if idx := strings.Index(jsonKey, ","); idx >= 0 {
			jsonKey = jsonKey[:idx]
		}
		if jsonKey == "" || jsonKey == "-" {
			name := field.Name
			jsonKey = strings.ToLower(name[:1]) + name[1:]
		}
		result[field.Name] = jsonKey
	}
	return result
}

func (my *Validator) loadDefaultTranslator() (ut.Translator, bool) {
	my.mutex.RLock()
	locale := my.defaultLocale
	my.mutex.RUnlock()
	return my.universal.GetTranslator(locale)
}

func (my *Validator) translateError(payload any, raw error) (*ValidationError, bool) {
	var errs validator.ValidationErrors
	if !errors.As(raw, &errs) {
		return nil, false
	}
	translator, found := my.loadDefaultTranslator()
	if !found {
		return nil, false
	}
	fieldLookup := buildFieldLookup(payload)
	fields := make([]FieldError, 0, len(errs))
	for _, e := range errs {
		message := e.Error()
		if translator != nil {
			if translated := e.Translate(translator); translated != "" {
				message = translated
			}
		}

		fieldName := e.StructField()
		if alias, ok := fieldLookup[fieldName]; ok {
			fieldName = alias
		}
		fields = append(fields, FieldError{Field: fieldName, Message: message})
	}
	return &ValidationError{fields: fields, detail: raw.Error(), err: raw}, true
}
