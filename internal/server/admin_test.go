package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_AdminAllowed(t *testing.T) {
	allow, err := ParseAdminAllow("100.64.0.0/10, 192.168.1.7")
	require.NoError(t, err)

	req := func(remote string, playground bool) *http.Request {
		r := httptest.NewRequest("GET", "/api/settings", nil)
		r.RemoteAddr = remote
		if playground {
			r = r.WithContext(context.WithValue(r.Context(), playgroundCtxKey{}, true))
		}
		return r
	}

	t.Run("open serves every remote", func(t *testing.T) {
		s := &Server{}
		s.SetAdminAccess(false, nil)
		assert.True(t, s.adminAllowed(req("10.0.0.5:4444", false)))
	})

	t.Run("local only", func(t *testing.T) {
		s := &Server{}
		s.SetAdminAccess(true, allow)

		assert.True(t, s.adminAllowed(req("127.0.0.1:5555", false)), "loopback v4")
		assert.True(t, s.adminAllowed(req("[::1]:5555", false)), "loopback v6")
		assert.True(t, s.adminAllowed(req("100.101.102.103:5555", false)), "tailnet CIDR")
		assert.True(t, s.adminAllowed(req("192.168.1.7:5555", false)), "bare IP allow entry")

		assert.False(t, s.adminAllowed(req("192.168.1.8:5555", false)), "other LAN host")
		assert.False(t, s.adminAllowed(req("10.0.0.5:4444", false)), "other LAN subnet")
		assert.False(t, s.adminAllowed(req("garbage", false)), "unparseable remote")
	})

	t.Run("playground is exempt", func(t *testing.T) {
		s := &Server{}
		s.SetAdminAccess(true, nil)
		assert.True(t, s.adminAllowed(req("10.0.0.5:4444", true)))
	})
}

func TestParseAdminAllow(t *testing.T) {
	nets, err := ParseAdminAllow(" 10.0.0.0/8 ,, fd00::/8 , 1.2.3.4 ")
	require.NoError(t, err)
	require.Len(t, nets, 3)

	_, err = ParseAdminAllow("not-an-ip")
	assert.Error(t, err)
}
