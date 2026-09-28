package httpx

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// PageRequest is a validated, 1-based page selection.
type PageRequest struct {
	Page     int32
	PageSize int32
}

func (p PageRequest) Limit() int64 { return int64(p.PageSize) }

func (p PageRequest) Offset() int64 { return int64(p.Page-1) * int64(p.PageSize) }

// Pagination is the collection metadata returned alongside paged data.
type Pagination struct {
	Page       int32 `json:"page"`
	PageSize   int32 `json:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int64 `json:"total_pages"`
}

// PageResponse is the envelope for paginated collection responses.
type PageResponse struct {
	Data       any        `json:"data"`
	Pagination Pagination `json:"pagination"`
}

// ParsePageRequest reads page and page_size, rejecting values outside
// 1..MaxPageSize rather than silently clamping them.
func ParsePageRequest(values url.Values) (PageRequest, error) {
	req := PageRequest{Page: 1, PageSize: DefaultPageSize}
	fields := map[string]string{}
	if raw := values.Get("page"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 1 {
			fields["page"] = "must be a positive integer"
		} else {
			req.Page = int32(n)
		}
	}
	if raw := values.Get("page_size"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 1 || n > MaxPageSize {
			fields["page_size"] = fmt.Sprintf("must be between 1 and %d", MaxPageSize)
		} else {
			req.PageSize = int32(n)
		}
	}
	if len(fields) > 0 {
		return PageRequest{}, NewValidationError(fields)
	}
	return req, nil
}

// NewPagination builds response metadata for a page of a collection of total items.
func NewPagination(req PageRequest, total int64) Pagination {
	p := Pagination{Page: req.Page, PageSize: req.PageSize, Total: total}
	if req.PageSize > 0 {
		p.TotalPages = (total + int64(req.PageSize) - 1) / int64(req.PageSize)
	}
	return p
}

// WritePage writes a 200 paginated collection response.
func WritePage(w http.ResponseWriter, data any, pagination Pagination) {
	WriteJSON(w, http.StatusOK, PageResponse{Data: data, Pagination: pagination})
}
