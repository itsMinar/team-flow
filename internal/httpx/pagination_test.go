package httpx

import (
	"errors"
	"net/url"
	"testing"
)

func TestParsePageRequestDefaults(t *testing.T) {
	req, err := ParsePageRequest(url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	if req.Page != 1 || req.PageSize != DefaultPageSize || req.Offset() != 0 {
		t.Fatalf("unexpected defaults: %+v", req)
	}
}

func TestParsePageRequestValues(t *testing.T) {
	req, err := ParsePageRequest(url.Values{"page": {"3"}, "page_size": {"25"}})
	if err != nil {
		t.Fatal(err)
	}
	if req.Limit() != 25 || req.Offset() != 50 {
		t.Fatalf("limit/offset = %d/%d", req.Limit(), req.Offset())
	}
}

func TestParsePageRequestRejectsInvalid(t *testing.T) {
	cases := []url.Values{
		{"page": {"0"}},
		{"page": {"-1"}},
		{"page": {"abc"}},
		{"page": {"99999999999"}},
		{"page_size": {"0"}},
		{"page_size": {"101"}},
		{"page_size": {"1; DROP TABLE projects"}},
	}
	for _, values := range cases {
		_, err := ParsePageRequest(values)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != 400 {
			t.Fatalf("%v: expected 400 validation error, got %v", values, err)
		}
	}
}

func TestParsePageRequestLargePageDoesNotOverflow(t *testing.T) {
	req, err := ParsePageRequest(url.Values{"page": {"2147483647"}, "page_size": {"100"}})
	if err != nil {
		t.Fatal(err)
	}
	if req.Offset() <= 0 {
		t.Fatalf("offset overflowed: %d", req.Offset())
	}
}

func TestNewPagination(t *testing.T) {
	cases := []struct {
		total, pages int64
	}{{0, 0}, {1, 1}, {20, 1}, {21, 2}, {150, 8}}
	for _, c := range cases {
		p := NewPagination(PageRequest{Page: 1, PageSize: 20}, c.total)
		if p.TotalPages != c.pages {
			t.Fatalf("total %d: pages = %d, want %d", c.total, p.TotalPages, c.pages)
		}
	}
	if p := NewPagination(PageRequest{}, 10); p.TotalPages != 0 {
		t.Fatalf("zero page size must not divide by zero: %+v", p)
	}
}
