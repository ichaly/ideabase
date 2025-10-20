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
	"github.com/iancoleman/strcase"
)

const (
	LocaleEnglish = "en"
	LocaleChinese = "zh"

	generalMessageKey = "__validation_error"
)

type Validator struct {
	validate      *validator.Validate
	universal     *ut.UniversalTranslator
	defaultLocale string
	registered    map[string]struct{}
	mutex         sync.RWMutex
}

func NewValidator() (*Validator, error) {
	enLocale, zhLocale := en.New(), zh.New()

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

	if err := my.registerLocale(enLocale, enTranslations.RegisterDefaultTranslations, WithGeneralMessage("validation failed")); err != nil {
		return nil, err
	}
	if err := my.registerLocale(zhLocale, zhTranslations.RegisterDefaultTranslations, WithGeneralMessage("参数校验失败")); err != nil {
		return nil, err
	}

	return my, nil
}

type TranslationOption func(*Validator, string, ut.Translator) error

func WithGeneralMessage(message string) TranslationOption {
	msg := strings.TrimSpace(message)
	return func(v *Validator, locale string, translator ut.Translator) error {
		if locale == "" || msg == "" {
			return nil
		}
		if translator != nil {
			return translator.Add(generalMessageKey, msg, true)
		}
		return nil
	}
}

func (my *Validator) RegisterTranslation(trans locales.Translator, register func(*validator.Validate, ut.Translator) error, opts ...TranslationOption) error {
	if trans == nil || register == nil {
		return fmt.Errorf("validator: translator 与 register 不能为空")
	}
	return my.registerLocale(trans, register, opts...)
}

func (my *Validator) Validate(out any) error {
	return my.Struct(out)
}

func (my *Validator) Struct(payload any) error {
	return my.run(func() error { return my.validate.Struct(payload) })
}

func (my *Validator) Var(field any, tag string) error {
	return my.run(func() error { return my.validate.Var(field, tag) })
}

func (my *Validator) run(fn func() error) error {
	if err := fn(); err != nil {
		if translated, ok := my.wrapValidationError(err); ok {
			return translated
		}
		return err
	}
	return nil
}

func (my *Validator) registerLocale(trans locales.Translator, register func(*validator.Validate, ut.Translator) error, opts ...TranslationOption) error {
	locale := strings.TrimSpace(trans.Locale())
	if locale == "" {
		return fmt.Errorf("validator: translator locale 不能为空")
	}

	my.mutex.Lock()
	defer my.mutex.Unlock()

	if _, exists := my.registered[locale]; exists {
		return nil
	}
	if err := my.universal.AddTranslator(trans, true); err != nil {
		return err
	}
	translator, ok := my.universal.GetTranslator(locale)
	if !ok {
		return fmt.Errorf("validator: 未支持的语言代码 %q", locale)
	}
	if err := register(my.validate, translator); err != nil {
		return err
	}

	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(my, locale, translator); err != nil {
			return err
		}
	}

	my.registered[locale] = struct{}{}
	return nil
}

func (my *Validator) wrapValidationError(raw error) (error, bool) {
	var errs validator.ValidationErrors
	if !errors.As(raw, &errs) {
		return nil, false
	}

	my.mutex.RLock()
	locale := my.defaultLocale
	my.mutex.RUnlock()
	translator, ok := my.universal.GetTranslator(locale)
	if !ok {
		return nil, false
	}

	message := strings.TrimSpace(raw.Error())
	if translator != nil {
		if msg, err := translator.T(generalMessageKey); err == nil && strings.TrimSpace(msg) != "" {
			message = strings.TrimSpace(msg)
		}
	}

	return &translatedErrors{
		ValidationErrors: append(validator.ValidationErrors(nil), errs...),
		message:          message,
		translator:       translator,
	}, true
}

type translatedErrors struct {
	validator.ValidationErrors
	message    string
	translator ut.Translator
}

func (my *translatedErrors) Error() string {
	return my.message
}

func (my *translatedErrors) Extensions() Extension {
	if my == nil || len(my.ValidationErrors) == 0 {
		return nil
	}

	var ext Extension
	for _, e := range my.ValidationErrors {
		field := strings.TrimSpace(strcase.ToLowerCamel(e.StructField()))
		if field == "" {
			continue
		}
		msg := strings.TrimSpace(e.Error())
		if my.translator != nil {
			if translated := strings.TrimSpace(e.Translate(my.translator)); translated != "" {
				msg = translated
			}
		}
		if msg == "" {
			continue
		}
		if ext == nil {
			ext = make(Extension)
		}
		ext[field] = msg
	}
	return ext
}
