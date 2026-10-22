// SPDX-License-Identifier: GPL-3.0-or-later
package api

import "testing"

func TestCurrencyExchangeNativeConfigsUseText(t *testing.T) {
	for _, name := range []string{"MainStore", "PixelPassOffers", "PixelPass"} {
		if !configPushUsesInlineBody(name) {
			t.Fatal("native JSON config cannot use binary val", name)
		}
	}
}
