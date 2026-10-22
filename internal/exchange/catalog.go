// SPDX-License-Identifier: GPL-3.0-or-later
// Package exchange owns both the native 23.1.3 catalog and its settlement rates.
package exchange

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

const Tab = "TabCurrencyExchange"

// Existing localized labels and prefabs are resolved by the stock client.
// No client files, real-money billing, or custom purchase endpoint are needed.
type Offer struct {
	ID                    int
	Pay                   string
	Cost                  int64
	Receive               string
	Amount                int64
	PayIndex, RewardIndex int
}

func Offers() []Offer {
	return []Offer{
		{231301, "GemsCurrency", 100, "PixelPassCurrency", 100, 2015, 26015},
		{231302, "Coins", 100, "BattlePassCurrency", 50, 1015, 3015},
		{231303, "Coins", 500, "PixelPassCurrency", 100, 1015, 26015},
		{231304, "Coins", 1000, "GemsCurrency", 100, 1015, 2015},
		{231305, "GemsCurrency", 100, "Coins", 1000, 2015, 1015},
	}
}
func Find(id int) (Offer, bool) {
	for _, o := range Offers() {
		if o.ID == id {
			return o, true
		}
	}
	return Offer{}, false
}

//go:embed stock_main_store.json
var stockMainStore []byte

// Native PixelPass offers support virtual-currency prices without real-money
// billing. sd=1 and lt=0 mean available from season day one with no daily expiry;
// bl=0 means repeatable. si must match the MainStore section's s exactly.
func NativeOffers() []any {
	out := make([]any, 0, len(Offers()))
	for n, o := range Offers() {
		out = append(out, map[string]any{"i": o.ID, "e": 1, "sd": 1, "np": 0, "lt": 0, "tr": 0,
			"it": []string{fmt.Sprintf("%d:%d", o.RewardIndex, o.Amount)}, "pr": []string{fmt.Sprintf("%d:%d", o.PayIndex, o.Cost)},
			"g": 0, "bl": 0, "p": 0, "mpn": "MainStorePromo_PixelPass_Currency_Layout", "vv": "layout", "im": offerImage(o.RewardIndex), "si": Tab, "sl": n + 1, "py": len(Offers()) - n})
	}
	return out
}

func Document(name string) (map[string]any, bool) {
	switch name {
	case "PixelPassOffers":
		return map[string]any{"of": NativeOffers()}, true
	case "MainStore":
		var d map[string]any
		if err := json.Unmarshal(stockMainStore, &d); err != nil {
			panic(err)
		}
		d["ss"] = append([]any{map[string]any{"s": Tab, "tlk": "Key_3728", "sn": "halloween", "vv": "layout", "p": []string{"PixelPass"}, "sh": []string{"Default", "PixelPass"}, "t": "navigation.offers"}}, d["ss"].([]any)...)
		return d, true
	default:
		return nil, false
	}
}

// ResourceManager paths are present in the stock 23.1.3 APK/OBB. Images are
// explicit because PixelPass currency cards do not infer their banner from it.
func offerImage(index int) string {
	switch index {
	case 26015:
		return "OfferIcons/pixelpasscurrency_1"
	case 3015:
		return "OfferIcons/bp_currency_5"
	case 2015:
		return "OfferIcons/CurrencyGem_BankTier_2"
	case 1015:
		return "OfferIcons/CurrencyCoin_BankTier_3"
	default:
		panic("unsupported exchange image")
	}
}
