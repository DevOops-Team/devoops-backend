package vdi

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func assertFieldError(t *testing.T, err error, field string) {
	t.Helper()
	var api *APIError
	if !errors.As(err, &api) || api.Status != 400 || api.Code != "VALIDATION_ERROR" || len(api.Details) != 1 || api.Details[0].Field != field {
		t.Fatalf("expected validation error for %s, got %#v", field, err)
	}
}

func TestValidateCreateBoundaries(t *testing.T) {
	valid := CreateRequest{Name: "desktop", OSID: 1, CPUCores: 1, MemoryGB: 1, StorageGB: 1}
	for _, osID := range []int64{1, MaxID} {
		q := valid
		q.OSID = osID
		if err := validateCreate(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, field string
		change      func(*CreateRequest)
	}{
		{"empty name", "name", func(q *CreateRequest) { q.Name = "" }},
		{"whitespace name", "name", func(q *CreateRequest) { q.Name = " \t\n" }},
		{"zero OS", "osId", func(q *CreateRequest) { q.OSID = 0 }},
		{"negative OS", "osId", func(q *CreateRequest) { q.OSID = -1 }},
		{"unsafe OS", "osId", func(q *CreateRequest) { q.OSID = MaxID + 1 }},
		{"zero CPU", "cpuCores", func(q *CreateRequest) { q.CPUCores = 0 }},
		{"negative CPU", "cpuCores", func(q *CreateRequest) { q.CPUCores = -1 }},
		{"zero memory", "memoryGb", func(q *CreateRequest) { q.MemoryGB = 0 }},
		{"negative memory", "memoryGb", func(q *CreateRequest) { q.MemoryGB = -1 }},
		{"zero disk", "storageGb", func(q *CreateRequest) { q.StorageGB = 0 }},
		{"negative disk", "storageGb", func(q *CreateRequest) { q.StorageGB = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) { q := valid; tc.change(&q); assertFieldError(t, validateCreate(q), tc.field) })
	}
}

func TestDecodeRequestContract(t *testing.T) {
	for _, tc := range []struct{ name, body, contentType, query, field string }{
		{"valid", `{"name":"vm","osId":1}`, "application/json", "", ""},
		{"charset", `{"name":"vm"}`, "application/json; charset=utf-8", "", ""},
		{"unknown", `{"unexpected":true}`, "application/json", "", "body"},
		{"wrong type", `{"cpuCores":"one"}`, "application/json", "", "body"},
		{"fractional integer", `{"cpuCores":1.5}`, "application/json", "", "body"},
		{"malformed", `{"name":`, "application/json", "", "body"},
		{"empty", "", "application/json", "", "body"},
		{"multiple objects", `{} {}`, "application/json", "", "body"},
		{"trailing garbage", `{} x`, "application/json", "", "body"},
		{"wrong content type", `{}`, "text/plain", "", "body"},
		{"missing content type", `{}`, "", "", "body"},
		{"query", `{}`, "application/json", "?extra=1", "query"},
		{"oversize", `{"name":"` + strings.Repeat("a", 65536) + `"}`, "application/json", "", "body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/desktops"+tc.query, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			var q CreateRequest
			err := decode(httptest.NewRecorder(), req, &q)
			if tc.field != "" {
				assertFieldError(t, err, tc.field)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIDAndBodyValidation(t *testing.T) {
	for _, value := range []string{"0", "-1", "9007199254740992", "9223372036854775808", "1.5", "abc", ""} {
		_, err := id(value, "desktopId")
		assertFieldError(t, err, "desktopId")
	}
	for _, value := range []string{"1", "9007199254740991"} {
		if _, err := id(value, "desktopId"); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ body, query, field string }{{"", "", ""}, {" ", "", "body"}, {"{}", "", "body"}, {"", "?x=1", "query"}} {
		req := httptest.NewRequest("DELETE", "/"+tc.query, strings.NewReader(tc.body))
		err := noBody(req)
		if tc.field != "" {
			assertFieldError(t, err, tc.field)
		} else if err != nil {
			t.Fatal(err)
		}
	}
}
