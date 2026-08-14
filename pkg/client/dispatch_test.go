package client

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShouldFallbackOnStatus(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		resp *http.Response
		want bool
	}{
		{name: "nil response never falls back"},
		{
			name: "too many requests falls back",
			resp: &http.Response{StatusCode: http.StatusTooManyRequests},
			want: true,
		},
		{
			name: "forbidden falls back",
			resp: &http.Response{StatusCode: http.StatusForbidden},
			want: true,
		},
		{
			name: "service unavailable falls back",
			resp: &http.Response{StatusCode: http.StatusServiceUnavailable},
			want: true,
		},
		{name: "ok passes through", resp: &http.Response{StatusCode: http.StatusOK}},
		{name: "not found passes through", resp: &http.Response{StatusCode: http.StatusNotFound}},
		{name: "bad request passes through", resp: &http.Response{StatusCode: http.StatusBadRequest}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, shouldFallbackOnStatus(tc.resp))
		})
	}
}

func TestIsProxyMiss(t *testing.T) {
	t.Parallel()

	assert.False(t, IsProxyMiss(nil))
	assert.False(t, IsProxyMiss(ErrPoolExhausted))
}
