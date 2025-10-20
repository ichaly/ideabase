package std

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unicode"

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

	generalMessageKey = "__validation_error"
)

var defaultGeneralMessages = map[string]string{
	LocaleEnglish: "validation failed",
	LocaleChinese: "参数校验失败",
}

// FieldError 字段级错误信息
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError 封装后的校验错误
type ValidationError struct {
	fields  []FieldError
	message string
	err     error
}

// Error 返回错误描述
func (my *ValidationError) Error() string {
	if my == nil {
		return ""
	}
	if my.message != "" {
		return my.message
	}
	if my.err != nil {
		return my.err.Error()
	}
	return defaultGeneralMessages[LocaleChinese]
}

// Unwrap 返回底层错误
func (my *ValidationError) Unwrap() error {
	if my == nil {
		return nil
	}
	return my.err
}

// Extensions 以键值形式返回字段错误，方便统一序列化
func (my *ValidationError) Extensions() Extension {
	if my == nil || len(my.fields) == 0 {
		return nil
	}
	ext := make(Extension, len(my.fields)+2)
	cloned := make([]FieldError, len(my.fields))
	copy(cloned, my.fields)
	ext["errors"] = cloned
	if msg := my.Message(); msg != "" {
		ext["message"] = msg
	}
	for _, field := range my.fields {
		if field.Field != "" {
			ext[field.Field] = field.Message
		}
	}
	return ext
}

// Fields 返回字段错误列表副本
func (my *ValidationError) Fields() []FieldError {
	if my == nil || len(my.fields) == 0 {
		return nil
	}
	cp := make([]FieldError, len(my.fields))
	copy(cp, my.fields)
	return cp
}

// Message 返回顶层错误描述
func (my *ValidationError) Message() string {
	if my == nil {
		return ""
	}
	return my.message
}

// Summary 与 Message 含义一致，便于兼容旧接口
func (my *ValidationError) Summary() string {
	return my.Message()
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
	enLocale := en.New()
	zhLocale := zh.New()

	my := &Validator{
		validate:      validator.New(),
		universal:     ut.New(enLocale, enLocale, zhLocale),
		defaultLocale: LocaleChinese,
		registered:    make(map[string]struct{}),
	}

	my.validate.RegisterTagNameFunc(func(field reflect.StructField) string {
		if label := strings.TrimSpace(field.Tag.Get("label")); label != "" {
			return label
		}
		return field.Name
	})

	if err := my.registerLocale(enLocale, enTranslations.RegisterDefaultTranslations); err != nil {
		return nil, err
	}
	if err := my.registerLocale(zhLocale, zhTranslations.RegisterDefaultTranslations); err != nil {
		return nil, err
	}

	return my, nil
}

// RegisterValidation 包装原生注册校验函数
func (my *Validator) RegisterValidation(tag string, fn validator.Func, callValidationEvenIfNull ...bool) error {
	return my.validate.RegisterValidation(tag, fn, callValidationEvenIfNull...)
}

// RegisterTranslation 注册指定语言翻译，必要时自动挂载 translator。
func (my *Validator) RegisterTranslation(locale string, register func(*validator.Validate, ut.Translator) error, trans locales.Translator) error {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return fmt.Errorf("validator: locale 不能为空")
	}
	if trans == nil {
		return fmt.Errorf("validator: translator 不能为空")
	}
	if register == nil {
		return fmt.Errorf("validator: register 函数不能为空")
	}
	if !strings.EqualFold(locale, trans.Locale()) {
		return fmt.Errorf("validator: locale 与 translator 不匹配")
	}
	return my.registerLocale(trans, register)
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

// Validate 实现 fiber.StructValidator 接口
func (my *Validator) Validate(out any) error {
	return my.Struct(out)
}

// Struct 校验结构体并返回国际化后的错误
func (my *Validator) Struct(target any) error {
	if err := my.validate.Struct(target); err != nil {
		if converted, ok := my.translateError(target, err); ok {
			return converted
		}
		return err
	}
	return nil
}

// Var 校验单个字段并返回国际化后的错误
func (my *Validator) Var(field any, tag string) error {
	if err := my.validate.Var(field, tag); err != nil {
		if converted, ok := my.translateError(nil, err); ok {
			return converted
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
		return &ValidationError{message: err.Error(), err: err}
	}
	return nil
}

// RegisterGeneralMessage 注册顶层错误提示的翻译，便于国际化默认错误描述。
func (my *Validator) RegisterGeneralMessage(locale, message string) error {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return fmt.Errorf("validator: 语言代码不能为空")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("validator: 翻译内容不能为空")
	}

	my.mutex.RLock()
	translator, found := my.universal.GetTranslator(locale)
	my.mutex.RUnlock()
	if !found {
		return fmt.Errorf("validator: 未注册语言代码 %q", locale)
	}
	return translator.Add(generalMessageKey, message, true)
}

func (my *Validator) registerLocale(trans locales.Translator, register func(*validator.Validate, ut.Translator) error) error {
	if trans == nil {
		return fmt.Errorf("validator: translator 不能为空")
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
	if msg, ok := defaultGeneralMessages[locale]; ok {
		if err := translator.Add(generalMessageKey, msg, true); err != nil {
			return err
		}
	}
	my.registered[locale] = struct{}{}
	return nil
}

func (my *Validator) translateError(payload any, raw error) (*ValidationError, bool) {
	var errs validator.ValidationErrors
	if !errors.As(raw, &errs) {
		return nil, false
	}

	my.mutex.RLock()
	locale := my.defaultLocale
	my.mutex.RUnlock()

	translator, found := my.universal.GetTranslator(locale)
	if !found {
		return nil, false
	}

	meta := buildFieldMetadata(payload)
	fields := make([]FieldError, 0, len(errs))
	for _, e := range errs {
		message := translateFieldError(e, translator)
		if override := findMessageOverride(meta, e); override != "" {
			message = override
		}

		fieldName := resolveFieldName(meta, e)
		fields = append(fields, FieldError{Field: fieldName, Message: message})
	}
	message, _ := translator.T(generalMessageKey)
	if message == "" {
		if fallback, ok := defaultGeneralMessages[locale]; ok {
			message = fallback
		} else {
			message = raw.Error()
		}
	}
	return &ValidationError{fields: fields, message: message, err: raw}, true
}

func translateFieldError(err validator.FieldError, translator ut.Translator) string {
	if translator == nil {
		return err.Error()
	}
	msg := err.Translate(translator)
	if msg == "" {
		return err.Error()
	}
	return msg
}

type fieldMeta struct {
	jsonKey string
	label   string
	message string
}

func resolveFieldName(meta map[string]fieldMeta, err validator.FieldError) string {
	if len(meta) == 0 {
		return err.Field()
	}
	metaKey := normalizeNamespace(err.StructNamespace())
	if info, ok := meta[metaKey]; ok {
		if info.jsonKey != "" {
			return info.jsonKey
		}
		if info.label != "" {
			return info.label
		}
	}
	if info, ok := meta[err.StructField()]; ok {
		if info.jsonKey != "" {
			return info.jsonKey
		}
		if info.label != "" {
			return info.label
		}
	}
	return lowerCamel(err.StructField())
}

func findMessageOverride(meta map[string]fieldMeta, err validator.FieldError) string {
	if len(meta) == 0 {
		return ""
	}
	metaKey := normalizeNamespace(err.StructNamespace())
	if info, ok := meta[metaKey]; ok && info.message != "" {
		return info.message
	}
	if info, ok := meta[err.StructField()]; ok && info.message != "" {
		return info.message
	}
	return ""
}

func buildFieldMetadata(payload any) map[string]fieldMeta {
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

	meta := make(map[string]fieldMeta)
	walkStructFields(meta, typ, "", make(map[reflect.Type]struct{}))
	return meta
}

func walkStructFields(meta map[string]fieldMeta, typ reflect.Type, prefix string, visited map[reflect.Type]struct{}) {
	if typ == nil || typ.Kind() != reflect.Struct {
		return
	}
	if visited == nil {
		visited = make(map[reflect.Type]struct{})
	}
	if _, seen := visited[typ]; seen {
		return
	}
	visited[typ] = struct{}{}
	defer delete(visited, typ)

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}

		name := field.Name
		if prefix != "" {
			name = prefix + "." + name
		}

		jsonKey := strings.TrimSpace(strings.Split(field.Tag.Get("json"), ",")[0])
		if jsonKey == "" || jsonKey == "-" {
			jsonKey = lowerCamel(field.Name)
		}
		label := strings.TrimSpace(field.Tag.Get("label"))
		message := strings.TrimSpace(field.Tag.Get("message"))
		meta[name] = fieldMeta{
			jsonKey: jsonKey,
			label:   label,
			message: message,
		}

		ft := field.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			walkStructFields(meta, ft, name, visited)
		}
	}
}

func lowerCamel(name string) string {
	if name == "" {
		return ""
	}
	runes := []rune(name)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

func normalizeNamespace(ns string) string {
	if ns == "" {
		return ""
	}
	builder := strings.Builder{}
	for i := 0; i < len(ns); i++ {
		ch := ns[i]
		if ch == '[' {
			for i < len(ns) && ns[i] != ']' {
				i++
			}
			continue
		}
		if ch == ']' {
			continue
		}
		if ch == '.' {
			builder.WriteByte(ch)
			continue
		}
		builder.WriteByte(ch)
	}
	return builder.String()
}
