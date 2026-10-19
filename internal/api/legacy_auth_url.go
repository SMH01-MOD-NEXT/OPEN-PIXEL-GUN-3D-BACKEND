// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http"
	"net/url"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

// Use the origin the device actually reached, not the server's bind address
// (which can be :8080). Never trust forwarded headers from arbitrary peers.
func legacySocketURL(r *http.Request, account store.Account) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	query := url.Values{
		"opg3d_player":  {account.PlayerID},
		"opg3d_session": {account.Session},
	}
	return (&url.URL{Scheme: scheme, Host: r.Host, Path: "/socket.io/", RawQuery: query.Encode()}).String()
}
