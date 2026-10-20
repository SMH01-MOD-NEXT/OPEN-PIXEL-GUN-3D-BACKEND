// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"embed"
	"encoding/json"
	"strings"
)

//go:embed admin_assets/console.html admin_assets/console.css admin_assets/console.js admin_assets/console.ru.json
var inventoryAssets embed.FS

func inventoryConsoleHTML() string {
	read := func(name string) string {
		b, e := inventoryAssets.ReadFile("admin_assets/" + name)
		if e != nil {
			panic(e)
		}
		return string(b)
	}
	var labels map[string]any
	if e := json.Unmarshal([]byte(read("console.ru.json")), &labels); e != nil {
		panic(e)
	}
	// Escape script-closing text and HTML delimiters in translations.
	safe, e := json.Marshal(labels)
	if e != nil {
		panic(e)
	}
	return strings.NewReplacer("/* CONSOLE_CSS */", read("console.css"), "/* CONSOLE_LOCALE */", "const TEXT = "+string(safe)+";", "/* CONSOLE_JS */", read("console.js")).Replace(read("console.html"))
}
