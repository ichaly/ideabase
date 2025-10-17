package std

import (
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
	translators   map[string]ut.Translator
	defaultLocale string
	registered    map[string]bool
	mutex         sync.RWMutex
}

// NewValidator 创建带默认语言（简体中文）的校验器实例
func NewValidator() (*Validator, error) {
	enLocale, zhLocale := en.New(), zh.New()
	v := &Validator{
		validate:      validator.New(),
		universal:     ut.New(enLocale, enLocale, zhLocale),
		translators:   make(map[string]ut.Translator),
		defaultLocale: LocaleChinese,
		registered:    make(map[string]bool),
	}

	// 注册默认翻译
	if err := v.RegisterTranslation(LocaleEnglish, enTranslations.RegisterDefaultTranslations); err != nil {
		return nil, err
	}
	if err := v.RegisterTranslation(LocaleChinese, zhTranslations.RegisterDefaultTranslations); err != nil {
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

// RegisterTranslation 注册指定语言的翻译函数
func (my *Validator) RegisterTranslation(locale string, register func(*validator.Validate, ut.Translator) error) error {
	translator, err := my.ensureTranslator(locale)
	if err != nil {
		return err
	}
	my.mutex.RLock()
	if my.registered[translator.Locale()] {
		my.mutex.RUnlock()
		return nil
	}
	my.mutex.RUnlock()

	if err := register(my.validate, translator); err != nil {
		return err
	}

	my.mutex.Lock()
	my.registered[translator.Locale()] = true
	my.mutex.Unlock()
	return nil
}

// Translator 获取指定语言的翻译器
func (my *Validator) Translator(locale string) (ut.Translator, error) {
	return my.ensureTranslator(locale)
}

// AddTranslator 注册新的语言翻译器，便于后续调用 RegisterTranslation。
// override 为 true 时会覆盖同名 translator。
func (my *Validator) AddTranslator(trans locales.Translator, override bool) error {
	if trans == nil {
		return fmt.Errorf("validator: translator 不能为空")
	}
	my.mutex.Lock()
	defer my.mutex.Unlock()
	if err := my.universal.AddTranslator(trans, override); err != nil {
		return err
	}
	locale := strings.ToLower(trans.Locale())
	delete(my.translators, locale)
	delete(my.registered, locale)
	return nil
}

// SetDefaultLocale 设置默认语言
func (my *Validator) SetDefaultLocale(locale string) error {
	locale = normalizeLocale(locale)
	if locale == "" {
		return fmt.Errorf("validator: 语言代码不能为空")
	}
	if _, err := my.ensureTranslator(locale); err != nil {
		return err
	}
	my.mutex.Lock()
	defer my.mutex.Unlock()
	my.defaultLocale = locale
	return nil
}

// Struct 校验结构体并返回国际化后的错误
func (my *Validator) Struct(target any) error {
	if err := my.validate.Struct(target); err != nil {
		return my.translateRawError(target, err)
	}
	return nil
}

// Var 校验单个字段并返回国际化后的错误
func (my *Validator) Var(field any, tag string) error {
	if err := my.validate.Var(field, tag); err != nil {
		return my.translateRawError(nil, err)
	}
	return nil
}

// Check 执行结构体校验并返回高阶错误，业务侧只需判空。
func (my *Validator) Check(payload any) error {
	if err := my.Struct(payload); err != nil {
		return my.toValidationError(payload, err)
	}
	return nil
}

func (my *Validator) translateRawError(payload any, rawErr error) error {
	if rawErr == nil {
		return nil
	}
	if _, ok := rawErr.(*ValidationError); ok {
		return rawErr
	}
	errs, ok := rawErr.(validator.ValidationErrors)
	if !ok {
		return rawErr
	}
	translator, transErr := my.Translator(my.defaultLocale)
	if transErr != nil {
		return rawErr
	}
	fieldLookup := buildFieldLookup(payload)
	fields := make([]FieldError, 0, len(errs))
	messages := make([]string, 0, len(errs))
	for _, fe := range errs {
		message := fe.Translate(translator)
		if message == "" {
			message = fe.Error()
		}
		messages = append(messages, message)

		fieldName := fe.StructField()
		if alias, ok := fieldLookup[fieldName]; ok {
			fieldName = alias
		}
		fields = append(fields, FieldError{Field: fieldName, Message: message})
	}
	return &ValidationError{fields: fields, detail: rawErr.Error(), err: rawErr}
}

func (my *Validator) toValidationError(payload any, rawErr error) error {
	if rawErr == nil {
		return nil
	}
	if ve, ok := rawErr.(*ValidationError); ok {
		return ve
	}
	errs, ok := rawErr.(validator.ValidationErrors)
	if !ok {
		return &ValidationError{detail: rawErr.Error(), err: rawErr}
	}
	translator, transErr := my.Translator(my.defaultLocale)
	if transErr != nil {
		return &ValidationError{detail: rawErr.Error(), err: rawErr}
	}
	fieldLookup := buildFieldLookup(payload)
	fields := make([]FieldError, 0, len(errs))
	messages := make([]string, 0, len(errs))
	for _, fe := range errs {
		message := fe.Error()
		if translator != nil {
			if translated := fe.Translate(translator); translated != "" {
				message = translated
			}
		}
		messages = append(messages, message)

		fieldName := fe.StructField()
		if alias, ok := fieldLookup[fieldName]; ok {
			fieldName = alias
		}
		fields = append(fields, FieldError{Field: fieldName, Message: message})
	}
	return &ValidationError{fields: fields, detail: rawErr.Error(), err: rawErr}
}

func (my *Validator) ensureTranslator(locale string) (ut.Translator, error) {
	locale = normalizeLocale(locale)
	if locale == "" {
		my.mutex.RLock()
		locale = my.defaultLocale
		my.mutex.RUnlock()
	}

	my.mutex.RLock()
	translator, ok := my.translators[locale]
	my.mutex.RUnlock()
	if ok {
		return translator, nil
	}

	translator, found := my.universal.GetTranslator(locale)
	if !found {
		return nil, fmt.Errorf("validator: 未支持的语言代码 %q", locale)
	}

	my.mutex.Lock()
	my.translators[locale] = translator
	my.mutex.Unlock()
	return translator, nil
}

func normalizeLocale(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return ""
	}
	locale = strings.ReplaceAll(locale, "-", "_")
	return strings.ToLower(locale)
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
