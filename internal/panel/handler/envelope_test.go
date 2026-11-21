package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A list answered whole says so in meta without counting; a window counts.
func TestWholeListMeta(t *testing.T) {
	counted := 0
	count := func() (int64, error) { counted++; return 42, nil }

	meta, err := WholeListMeta(0, 0, 7, count)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"page": 1, "per_page": 7, "total": int64(7)}, meta)
	assert.Zero(t, counted, "a whole list is not counted again")

	meta, err = WholeListMeta(10, 20, 10, count)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"page": 3, "per_page": 10, "total": int64(42)}, meta)

	meta, err = WholeListMeta(0, 5, 37, count)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"page": 1, "per_page": 42, "total": int64(42)}, meta)

	_, err = WholeListMeta(10, 0, 10, func() (int64, error) { return 0, errors.New("down") })
	assert.Error(t, err)
}

// limit/offset win; else page and page_size make the window; nonsense is 0.
func TestListWindow(t *testing.T) {
	for _, tc := range []struct {
		query         string
		limit, offset int
	}{
		{"", 0, 0},
		{"limit=5&offset=10", 5, 10},
		{"page=3&page_size=20", 20, 40},
		{"page=0&page_size=20", 20, 0},
		{"limit=5&page=3&page_size=20", 5, 0},
		{"limit=-1&offset=-9", 0, 0},
	} {
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/x?"+tc.query, nil), httptest.NewRecorder())
		limit, offset := listWindow(c)
		assert.Equal(t, tc.limit, limit, tc.query)
		assert.Equal(t, tc.offset, offset, tc.query)
	}
}
