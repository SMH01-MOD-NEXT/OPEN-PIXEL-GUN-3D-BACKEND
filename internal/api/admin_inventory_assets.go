// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"embed"
	"encoding/json"
	"strings"
)

//go:embed admin_assets/console.html admin_assets/console.css admin_assets/console.js admin_assets/console.ru.json admin_assets/console.en.json
var inventoryAssets embed.FS

func inventoryConsoleHTML() string {
	read := func(name string) string {
		b, e := inventoryAssets.ReadFile("admin_assets/" + name)
		if e != nil {
			panic(e)
		}
		return string(b)
	}
	locales := make(map[string]map[string]any, 2)
	for _, language := range []string{"ru", "en"} {
		var labels map[string]any
		if e := json.Unmarshal([]byte(read("console."+language+".json")), &labels); e != nil {
			panic(e)
		}
		locales[language] = labels
	}
	// encoding/json escapes script-closing text and HTML delimiters in translations.
	safe, e := json.Marshal(locales)
	if e != nil {
		panic(e)
	}
	return strings.NewReplacer("/* CONSOLE_CSS */", read("console.css"), "/* CONSOLE_LOCALE */", "const LOCALES = "+string(safe)+";", "/* CONSOLE_JS */", read("console.js")).Replace(read("console.html"))
}
