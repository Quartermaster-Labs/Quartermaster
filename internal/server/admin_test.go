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

	t.Run("playground listener is not exempt", func(t *testing.T) {
		// Every listener shares one mux, so arriving on the playground address
		// must not open the config editor / backend installer to the LAN. What
		// the playground app itself needs goes through requirePlaygroundOrAdmin.
		s := &Server{}
		s.SetAdminAccess(true, nil)
		assert.False(t, s.adminAllowed(req("10.0.0.5:4444", true)))
		assert.True(t, s.adminAllowed(req("127.0.0.1:4444", true)), "loopback still admins")
	})
}

func TestServer_RequirePlaygroundOrAdmin(t *testing.T) {
	dir := t.TempDir()
	s := newTestServer(&stubRouter{}, &stubRouter{})
	s.SetAdminAccess(true, nil)
	pg := &Playground{Addr: ":8081", DataDir: dir}
	s.SetPlayground(pg)

	call := func(mw func(http.Handler) http.Handler, remote string, playground, loggedIn bool) int {
		r := httptest.NewRequest("GET", "/api/events", nil)
		r.RemoteAddr = remote
		if playground {
			r = r.WithContext(context.WithValue(r.Context(), playgroundCtxKey{}, true))
		}
		if loggedIn {
			r.AddCookie(&http.Cookie{Name: pgCookie, Value: pg.cookieValue("alice")})
		}
		w := httptest.NewRecorder()
		mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(w, r)
		return w.Code
	}

	gated := s.requirePlaygroundOrAdmin(true)
	asset := s.requirePlaygroundOrAdmin(false)

	assert.Equal(t, http.StatusOK, call(gated, "127.0.0.1:1", false, false), "loopback admin")
	assert.Equal(t, http.StatusOK, call(gated, "10.0.0.5:1", true, true), "remote playground, logged in")
	assert.Equal(t, http.StatusUnauthorized, call(gated, "10.0.0.5:1", true, false), "remote playground, logged out")
	assert.Equal(t, http.StatusForbidden, call(gated, "10.0.0.5:1", false, false), "remote non-playground")
	assert.Equal(t, http.StatusOK, call(asset, "10.0.0.5:1", true, false), "SPA bundle needs no login")
	assert.Equal(t, http.StatusForbidden, call(asset, "10.0.0.5:1", false, false), "…but only on the playground port")
}

func TestParseAdminAllow(t *testing.T) {
	nets, err := ParseAdminAllow(" 10.0.0.0/8 ,, fd00::/8 , 1.2.3.4 ")
	require.NoError(t, err)
	require.Len(t, nets, 3)

	_, err = ParseAdminAllow("not-an-ip")
	assert.Error(t, err)
}
