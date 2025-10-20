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

func (my *Validator) RegisterTranslation(trans locales.Translator, register func(*validator.Validate, ut.Translator) error) error {
	if trans == nil || register == nil {
		return fmt.Errorf("validator: translator 与 register 不能为空")
	}
	return my.registerLocale(trans, register)
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

func (my *Validator) registerLocale(trans locales.Translator, register func(*validator.Validate, ut.Translator) error) error {
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

	general := strings.TrimSpace(raw.Error())
	if translator != nil {
		if msg, err := translator.T(generalMessageKey); err == nil && strings.TrimSpace(msg) != "" {
			general = strings.TrimSpace(msg)
		}
	} else {
		if locale == LocaleEnglish {
			general = "validation failed"
		} else if locale == LocaleChinese {
			general = "参数校验失败"
		}
	}

	return &translatedErrors{
		ValidationErrors: append(validator.ValidationErrors(nil), errs...),
		general:          general,
		translator:       translator,
	}, true
}

type translatedErrors struct {
	validator.ValidationErrors
	general    string
	translator ut.Translator
}

func (my *translatedErrors) Error() string {
	if my == nil {
		return ""
	}
	if strings.TrimSpace(my.general) != "" {
		return my.general
	}
	return "参数校验失败"
}

func (my *translatedErrors) Extensions() Extension {
	if my == nil || len(my.ValidationErrors) == 0 {
		return nil
	}

	ext := make(Extension, len(my.ValidationErrors)+1)
	items := make([]map[string]string, 0, len(my.ValidationErrors))
	for _, fe := range my.ValidationErrors {
		field := strcase.ToLowerCamel(fe.StructField())
		message := strings.TrimSpace(fe.Error())
		if my.translator != nil {
			if msg := strings.TrimSpace(fe.Translate(my.translator)); msg != "" {
				message = msg
			}
		}
		items = append(items, map[string]string{"field": field, "message": message})
		if field != "" {
			if _, ok := ext[field]; !ok {
				ext[field] = message
			}
		}
	}
	if len(items) == 0 {
		return nil
	}
	ext["errors"] = items
	return ext
}
