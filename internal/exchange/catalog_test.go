// SPDX-License-Identifier: GPL-3.0-or-later
package exchange

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestNativeCatalogRatesAndIsolation(t *testing.T) {
	d, ok := Document("PixelPassOffers")
	if !ok {
		t.Fatal("missing")
	}
	offers := d["of"].([]any)
	if len(offers) != 5 {
		t.Fatal(offers)
	}
	for n, raw := range offers {
		o := raw.(map[string]any)
		rate := Offers()[n]
		if o["i"] != rate.ID || o["si"] != Tab || o["sd"] != 1 || o["e"] != 1 || o["bl"] != 0 || o["np"] != 0 || o["im"] == "" || o["mpn"] != "MainStorePromo_PixelPass_Currency_Layout" || o["vv"] != "layout" {
			t.Fatal(o)
		}
		if o["it"].([]string)[0] != fmt.Sprintf("%d:%d", rate.RewardIndex, rate.Amount) || o["pr"].([]string)[0] != fmt.Sprintf("%d:%d", rate.PayIndex, rate.Cost) {
			t.Fatal(o)
		}
	}
	d["of"] = nil
	again, _ := Document("PixelPassOffers")
	if again["of"] == nil {
		t.Fatal("shared state")
	}
	main, _ := Document("MainStore")
	section := main["ss"].([]any)[0].(map[string]any)
	if section["p"].([]string)[0] != "PixelPass" || section["s"] != Tab || section["tlk"] != "Key_3728" || section["vv"] != "layout" {
		t.Fatal(section)
	}
	for _, name := range []string{"MainStore", "PixelPassOffers"} {
		d, _ := Document(name)
		if _, e := json.Marshal(d); e != nil {
			t.Fatal(e)
		}
	}
	if _, ok := Document("../Shop"); ok {
		t.Fatal("unknown")
	}
}
