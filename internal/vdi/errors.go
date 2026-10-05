package vdi

import (
	"context"
	"errors"
)

type Context = context.Context
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}
type APIError struct {
	Status  int          `json:"-"`
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Details []FieldError `json:"details"`
}

func (e *APIError) Error() string { return e.Code }
func Err(status int, code, message string) *APIError {
	return &APIError{status, code, message, []FieldError{}}
}
func Invalid(field, message string) *APIError {
	e := Err(400, "VALIDATION_ERROR", "입력값을 확인해 주세요.")
	e.Details = append(e.Details, FieldError{field, message})
	return e
}

var ErrCreateRejected = errors.New("Nova rejected VM creation")
var ErrVMNotFound = errors.New("VM not found")
var ErrSpecUnavailable = Err(409, "SPEC_UNAVAILABLE", "m1.micro 사양을 정수 GB로 제공할 수 없습니다.")
var ErrOSUnavailable = Err(409, "OS_UNAVAILABLE", "이미지를 사용할 수 없습니다.")
var ErrCloudUnavailable = Err(503, "OPENSTACK_UNAVAILABLE", "OpenStack에 연결할 수 없습니다.")
