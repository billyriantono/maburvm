package handler

import (
	"strconv"

	"github.com/labstack/echo/v4"
)

// PageMeta is the pagination block every paginated list carries beside its
// data: {"page", "per_page", "total"}. Older lists keep their own fields
// (total, page_size, limit, offset...) too, for the clients that read them.
func PageMeta(page, perPage int, total int64) map[string]any {
	if page < 1 {
		page = 1
	}
	return map[string]any{"page": page, "per_page": perPage, "total": total}
}

// OffsetPageMeta is PageMeta for a list read by limit and offset.
func OffsetPageMeta(limit, offset int, total int64) map[string]any {
	page := 1
	if limit > 0 {
		page = offset/limit + 1
	}
	return PageMeta(page, limit, total)
}

// listWindow reads limit and offset, or else page and page_size (per_page is
// an alias the error envelope middleware maps to page_size). 0 means not given.
func listWindow(c echo.Context) (limit, offset int) {
	limit, _ = strconv.Atoi(c.QueryParam("limit"))
	offset, _ = strconv.Atoi(c.QueryParam("offset"))
	if c.QueryParam("limit") == "" && c.QueryParam("offset") == "" {
		if size, _ := strconv.Atoi(c.QueryParam("page_size")); size > 0 {
			page, _ := strconv.Atoi(c.QueryParam("page"))
			if page < 1 {
				page = 1
			}
			limit, offset = size, (page-1)*size
		}
	}
	return max(limit, 0), max(offset, 0)
}

// WholeListMeta is the meta of a list answered whole unless a window was asked
// for: n items on one page, or count's total for a window. count is only
// called for a window.
func WholeListMeta(limit, offset, n int, count func() (int64, error)) (map[string]any, error) {
	if limit <= 0 && offset <= 0 {
		return PageMeta(1, n, int64(n)), nil
	}
	total, err := count()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return PageMeta(1, int(total), total), nil
	}
	return OffsetPageMeta(limit, offset, total), nil
}
