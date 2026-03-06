package models

import (
	"fmt"
	"reflect"
	"regexp"

	"github.com/go-playground/validator/v10"
)

var validate *validator.Validate

// ValidationError represents a validation error with detailed message
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error implements the error interface for ValidationError
func (ve *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", ve.Field, ve.Message)
}

// ValidationErrors represents a collection of validation errors
type ValidationErrors []ValidationError

func (ve ValidationErrors) Error() string {
	result := "validation failed:"
	for _, e := range ve {
		result += fmt.Sprintf("\n  - %s: %s", e.Field, e.Message)
	}
	return result
}

func init() {
	validate = validator.New()

	// Register custom validators
	_ = validate.RegisterValidation("port_range", portRangeValidator)
	_ = validate.RegisterValidation("uuid", uuidValidator)
	_ = validate.RegisterValidation("ip_or_cidr", ipOrCidrValidator)
	_ = validate.RegisterValidation("bandwidth", bandwidthValidator)
}

// portRangeValidator validates port range format (e.g., "80", "80-443", "80,443,8080")
func portRangeValidator(fl validator.FieldLevel) bool {
	portRange := fl.Field().String()
	if portRange == "" {
		return true // empty is allowed (optional)
	}

	// Match patterns like "80", "80-443", "80,443,8080"
	portPattern := regexp.MustCompile(`^(\d{1,5}(-\d{1,5})?(,\d{1,5}(-\d{1,5})?)*)$`)
	if !portPattern.MatchString(portRange) {
		return false
	}

	// Validate each port number is in range 1-65535
	parts := regexp.MustCompile(`[,\-]`).Split(portRange, -1)
	for _, part := range parts {
		var port int
		if _, err := fmt.Sscanf(part, "%d", &port); err != nil {
			return false
		}
		if port < 1 || port > 65535 {
			return false
		}
	}
	return true
}

// uuidValidator validates UUID format
func uuidValidator(fl validator.FieldLevel) bool {
	uuidStr := fl.Field().String()
	if uuidStr == "" {
		return true // empty is allowed for optional fields
	}

	uuidPattern := regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	return uuidPattern.MatchString(uuidStr)
}

// ipOrCidrValidator validates IP address or CIDR notation
func ipOrCidrValidator(fl validator.FieldLevel) bool {
	ipStr := fl.Field().String()
	if ipStr == "" {
		return true // empty is allowed
	}

	// Try to parse as IP
	ip := regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}$`)
	if ip.MatchString(ipStr) {
		return true
	}

	// Try to parse as CIDR
	cidr := regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}/\d{1,2}$`)
	return cidr.MatchString(ipStr)
}

// bandwidthValidator validates bandwidth value (0 or positive integer)
func bandwidthValidator(fl validator.FieldLevel) bool {
	bw := fl.Field().Int()
	return bw >= 0
}

// ValidateStruct validates a struct and returns detailed errors
func ValidateStruct(s interface{}) ValidationErrors {
	errs := validate.Struct(s)
	if errs == nil {
		return nil
	}

	var validationErrs ValidationErrors
	for _, err := range errs.(validator.ValidationErrors) {
		fieldName := err.Field()
		tag := err.Tag()

		var message string
		switch tag {
		case "required":
			message = "this field is required"
		case "email":
			message = "must be a valid email address"
		case "ip":
			message = "must be a valid IP address"
		case "port_range":
			message = "must be a valid port range (e.g., 80, 80-443, 80,443,8080)"
		case "uuid":
			message = "must be a valid UUID"
		case "ip_or_cidr":
			message = "must be a valid IP address or CIDR notation"
		case "oneof":
			message = fmt.Sprintf("must be one of: %s", err.Param())
		case "min":
			message = fmt.Sprintf("must be at least %s", err.Param())
		case "max":
			message = fmt.Sprintf("must be at most %s", err.Param())
		case "dive":
			message = "invalid value in collection"
		default:
			message = fmt.Sprintf("failed %s validation", tag)
		}

		validationErrs = append(validationErrs, ValidationError{
			Field:   fieldName,
			Message: message,
		})
	}

	return validationErrs
}

// GetValidator returns the global validator instance
func GetValidator() *validator.Validate {
	return validate
}

// GetStructFieldName gets the JSON field name from a struct field
func GetStructFieldName(s interface{}, fieldPath string) string {
	t := reflect.TypeOf(s)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	parts := reflect.ValueOf(s)
	if parts.Kind() == reflect.Ptr {
		parts = parts.Elem()
	}

	// Simple lookup - for nested fields this would need recursive lookup
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		jsonTag := field.Tag.Get("json")
		if jsonTag == fieldPath || field.Name == fieldPath {
			return jsonTag
		}
	}

	return fieldPath
}
