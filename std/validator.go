package std

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

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
	LocaleEnglish     = "en"
	LocaleChinese     = "zh"
	generalMessageKey = "__validation_error"
)

type Validator struct {
	validate      *validator.Validate
	universal     *ut.UniversalTranslator
	defaultLocale string
}

func NewValidator() (*Validator, error) {
	enLocale, zhLocale := en.New(), zh.New()

	my := &Validator{
		validate:      validator.New(),
		universal:     ut.New(enLocale, enLocale, zhLocale),
		defaultLocale: LocaleChinese,
	}

	my.validate.RegisterTagNameFunc(func(field reflect.StructField) string {
		if label := strings.TrimSpace(field.Tag.Get("label")); label != "" {
			return label
		}
		return field.Name
	})

	if err := my.registerLocale(enLocale, enTranslations.RegisterDefaultTranslations, "validation failed"); err != nil {
		return nil, err
	}
	if err := my.registerLocale(zhLocale, zhTranslations.RegisterDefaultTranslations, "参数校验失败"); err != nil {
		return nil, err
	}

	return my, nil
}

func (my *Validator) RegisterTranslation(trans locales.Translator, register func(*validator.Validate, ut.Translator) error, generalMessage ...string) error {
	if trans == nil || register == nil {
		return fmt.Errorf("validator: translator 与 register 不能为空")
	}
	return my.registerLocale(trans, register, generalMessage...)
}

func (my *Validator) Validate(out any) error { return my.Struct(out) }

func (my *Validator) Struct(payload any) error {
	if err := my.validate.Struct(payload); err != nil {
		if translated, ok := my.wrapValidationError(err); ok {
			return translated
		}
		return err
	}
	return nil
}

func (my *Validator) Var(field any, tag string) error {
	if err := my.validate.Var(field, tag); err != nil {
		if translated, ok := my.wrapValidationError(err); ok {
			return translated
		}
		return err
	}
	return nil
}

func (my *Validator) registerLocale(trans locales.Translator, register func(*validator.Validate, ut.Translator) error, generalMessage ...string) error {
	locale := strings.TrimSpace(trans.Locale())

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

	if len(generalMessage) > 0 {
		if msg := strings.TrimSpace(generalMessage[0]); msg != "" {
			if err := translator.Add(generalMessageKey, msg, true); err != nil {
				return err
			}
		}
	}

	return nil
}

func (my *Validator) wrapValidationError(raw error) (error, bool) {
	var errs validator.ValidationErrors
	if !errors.As(raw, &errs) {
		return nil, false
	}

	translator, ok := my.universal.GetTranslator(my.defaultLocale)
	if !ok {
		return nil, false
	}

	message := strings.TrimSpace(raw.Error())
	if msg, err := translator.T(generalMessageKey); err == nil {
		if translated := strings.TrimSpace(msg); translated != "" {
			message = translated
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

func (my *translatedErrors) Error() string { return my.message }

func (my *translatedErrors) Extensions() Extension {
	if my == nil || len(my.ValidationErrors) == 0 {
		return nil
	}

	ext := make(Extension)
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
		ext[field] = msg
	}
	return ext
}
