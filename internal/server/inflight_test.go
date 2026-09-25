package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The rail shows the count and the list side by side, so they must agree while
// a request runs, carry what the metrics middleware attached, and both drop
// when it finishes.
func TestInflight_ListTracksRunningRequest(t *testing.T) {
	c := &inflightCounter{}
	var during []inflightSummary
	var detailBody []byte

	h := CreateInflightMiddleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := inflightEntryFrom(r.Context())
		require.NotNil(t, e)
		e.attach("qwen", map[string]string{"Content-Type": "application/json"}, []byte(`{"model":"qwen"}`))

		during = c.list()
		cap, _, ok := c.detail(e.ID)
		require.True(t, ok)
		detailBody = cap.ReqBody
		assert.EqualValues(t, 1, c.Current())
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	require.Len(t, during, 1)
	assert.Equal(t, "qwen", during[0].Model)
	assert.Equal(t, "/v1/chat/completions", during[0].Path)
	assert.True(t, during[0].HasBody)
	assert.Equal(t, `{"model":"qwen"}`, string(detailBody))

	assert.Empty(t, c.list())
	assert.EqualValues(t, 0, c.Current())
	_, _, ok := c.detail(during[0].ID)
	assert.False(t, ok, "a finished request must 404, not linger")
}

// A nil entry (a request that bypassed the inflight middleware) must not panic
// the metrics middleware.
func TestInflight_AttachNilEntry(t *testing.T) {
	var e *inflightEntry
	assert.NotPanics(t, func() { e.attach("m", nil, nil) })
}
