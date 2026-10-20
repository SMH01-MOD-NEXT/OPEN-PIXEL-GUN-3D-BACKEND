// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"embed"
	"encoding/json"
	"strings"
)

//go:embed admin_assets/inventory.js admin_assets/inventory.ru.json
var inventoryAssets embed.FS

func inventoryConsoleHTML() string {
	js, err := inventoryAssets.ReadFile("admin_assets/inventory.js")
	if err != nil {
		panic(err)
	}
	text, err := inventoryAssets.ReadFile("admin_assets/inventory.ru.json")
	if err != nil {
		panic(err)
	}
	var labels map[string]any
	if err = json.Unmarshal(text, &labels); err != nil {
		panic(err)
	}
	// json.Marshal escapes < > & in localizations, preventing script termination.
	safe, err := json.Marshal(labels)
	if err != nil {
		panic(err)
	}
	return strings.Replace(adminConsoleHTML, "/* INVENTORY_PANEL */", "var INVENTORY_TEXT = "+string(safe)+";\n"+string(js), 1)
}
