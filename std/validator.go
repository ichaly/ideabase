package std

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

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

// ValidationError 校验错误信息集合
type ValidationError struct {
	Messages []string
	Raw      validator.ValidationErrors
}

// Error 返回错误描述
func (my ValidationError) Error() string {
	return strings.Join(my.Messages, "; ")
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

// ValidateStructFunc 校验结构体方法签名
type ValidateStructFunc func(locale string, target any) error

// ValidateVarFunc 校验字段方法签名
type ValidateVarFunc func(locale string, field any, tag string) error

// TranslatorFunc 翻译器获取方法签名
type TranslatorFunc func(locale string) (ut.Translator, error)

// NewValidator 创建带默认语言（简体中文）的校验器实例
func NewValidator() (*Validator, error) {
	v := &Validator{
		validate:      validator.New(),
		universal:     ut.New(en.New(), zh.New()),
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

// Struct 校验结构体并返回国际化后的错误
func (my *Validator) Struct(target any) error {
	if err := my.validate.Struct(target); err != nil {
		return my.translateError(my.defaultLocale, err)
	}
	return nil
}

// Var 校验单个字段并返回国际化后的错误
func (my *Validator) Var(field any, tag string) error {
	if err := my.validate.Var(field, tag); err != nil {
		return my.translateError(my.defaultLocale, err)
	}
	return nil
}

// Translator 获取指定语言的翻译器
func (my *Validator) Translator(locale string) (ut.Translator, error) {
	return my.ensureTranslator(locale)
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

func (my *Validator) translateError(locale string, err error) error {
	validationErrors, ok := err.(validator.ValidationErrors)
	if !ok {
		return err
	}

	translator, transErr := my.ensureTranslator(locale)
	if transErr != nil {
		return transErr
	}

	messages := make([]string, 0, len(validationErrors))
	for _, fieldErr := range validationErrors {
		messages = append(messages, fieldErr.Translate(translator))
	}
	return ValidationError{
		Messages: messages,
		Raw:      validationErrors,
	}
}

func normalizeLocale(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return ""
	}
	locale = strings.ReplaceAll(locale, "-", "_")
	return strings.ToLower(locale)
}
