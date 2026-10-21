// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"bytes"
	"encoding/json"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/pixelpass"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPixelPassConfigPollPushAndImmutableCatalog(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = t.TempDir()
	hostDocument(t, cfg.ContentDir, "PixelPass", `{"c":{"i":999}}`)
	reply, changed := s.hostedConfigReply("PixelPass", "")
	if !changed {
		t.Fatal("not served")
	}
	body, ok := reply[sioConfigBodyKey].(string)
	if !ok || !bytes.Equal([]byte(body), pixelpass.JSON()) {
		t.Fatal("catalog/delivery diverged")
	}
	hash := reply[sioConfigHashKey].(string)
	if _, changed = s.hostedConfigReply("PixelPass", hash); changed {
		t.Fatal("hash unstable")
	}
	batches := s.configPushBatches(map[string]any{"PixelPass": map[string]any{sioConfigHashKey: ""}})
	if len(batches) != 1 || len(batches[0].documents) != 0 || batches[0].entries["PixelPass"].(map[string]any)[sioPushBodyKey] != string(pixelpass.JSON()) {
		t.Fatal("inline JSON push diverged")
	}
}
func TestPixelPassShippingExpGate(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	hosted, ok := s.hostedConfigDocument("ExpOpen")
	if !ok {
		t.Fatal("missing rank gate")
	}
	rows := hosted.document["eos"].([]any)
	found := false
	for _, v := range rows {
		r := v.(map[string]any)
		if r["t"] == "feature.pixelpass" {
			found = true
			if r["e"] != float64(0) {
				t.Fatal("rank one still locked")
			}
		}
	}
	if !found {
		t.Fatal("pixelpass gate missing")
	}
}

// The stock lobby displays zero when the row is null, but its entry
// predicate returns false for null (0x18EE3D4 and 0x20DF334). Therefore
// checking just the number in ExpOpen cannot establish that cohort B opens.
func TestPixelPassUnlockBothNativeExpOpenCohorts(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	canonical, ok := s.hostedConfigDocument("ExpOpen")
	if !ok {
		t.Fatal("missing ExpOpen")
	}
	names := map[string]any{}
	for _, name := range []string{"ExpOpen", "ExpOpenForB"} {
		t.Run(name, func(t *testing.T) {
			hosted, ok := s.hostedConfigDocument(name)
			if !ok {
				t.Fatal("native cohort has no gate document")
			}
			if !reflect.DeepEqual(hosted.document, canonical.document) || hosted.hash != canonical.hash {
				t.Fatal("cohort gates diverged")
			}
			rowCount := 0
			for _, raw := range hosted.document["eos"].([]any) {
				row := raw.(map[string]any)
				if row["t"] == "feature.pixelpass" {
					rowCount++
					threshold, ok := row["e"].(float64)
					if !ok || threshold != 0 {
						t.Fatal("rank-one/zero-match unlock not configured")
					}
					// Captured stock predicate: row != null && progress >= row.e.
					for _, progress := range []float64{0, 1, 20, 60} {
						if progress < threshold {
							t.Fatal("native entry remains locked")
						}
					}
				}
			}
			if rowCount != 1 {
				t.Fatal("missing/ambiguous pass row", rowCount)
			}
			reply, changed := s.hostedConfigReply(name, "")
			if !changed || reply[sioConfigBodyKey] != string(canonical.body) {
				t.Fatal("poll failed", name)
			}
			if _, changed = s.hostedConfigReply(name, hosted.hash); changed {
				t.Fatal("stable cache not accepted")
			}
			names[name] = map[string]any{sioConfigHashKey: ""}
		})
	}
	batches := s.configPushBatches(names)
	if len(batches) != 1 || len(batches[0].documents) != 0 || len(batches[0].entries) != 2 {
		t.Fatal("both cohorts must receive inline JSON")
	}
	for name := range names {
		entry := batches[0].entries[name].(map[string]any)
		if entry[sioPushBodyKey] != string(canonical.body) {
			t.Fatal("wrong native JSON branch", name)
		}
	}
}

func TestPixelPassStockUpdateProgressTransaction(t *testing.T) {
	// The contract itself has fixed lifecycle dates; store tests inject a clock
	// and cover the full season deterministically, including expiration.
	if !pixelpass.Active(time.Now()) {
		t.Skip("live API fixture is outside frozen season; deterministic reducer tests cover lifecycle")
	}
	s, _ := newTestServer(t)
	session := authorisedSession("pixelpass-wire", revisionTestPlayerID)
	if _, err := s.st.EnsureProfile(revisionTestPlayerID, ""); err != nil {
		t.Fatal(err)
	}
	standalone := sioHead{"id": float64(322), "i": "native-standalone-save", "p": map[string]any{"i": float64(1), "c": "stock-config-ref"}}
	saved := s.answerUpdateProgress(session, standalone, 80)
	if saved["err_code"] != sioErrNone || saved["i"] != "native-standalone-save" {
		t.Fatal("standalone without ci failed", saved)
	}
	if _, invented := saved["ci"]; invented {
		t.Fatal("standalone invented a wire child correlation ID")
	}
	head := sioHead{"id": float64(63), "i": "pixelpass-outer", "p": map[string]any{"c": []any{
		map[string]any{"id": float64(322), "ci": "season", "p": map[string]any{"i": float64(1), "c": "stock-config-ref"}},
		map[string]any{"id": float64(10), "ci": "echo", "p": map[string]any{"c": "Coins", "v": float64(55)}},
		map[string]any{"id": float64(323), "ci": "claim", "p": map[string]any{"l": float64(1), "r": []any{"1015:55"}}},
	}}}
	reply := s.answerUpdateProgress(session, head, 81)
	if reply["err_code"] != sioErrNone || reply["i"] != "pixelpass-outer" {
		t.Fatal(reply)
	}
	p, err := s.st.Profile(revisionTestPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	coins := p.Coins
	if coins != 55 || p.PixelPass == nil {
		t.Fatal("stock batch was merely acknowledged", p.Coins)
	}
	again := s.answerUpdateProgress(session, head, 82)
	if again["err_code"] != sioErrNone {
		t.Fatal(again)
	}
	p, _ = s.st.Profile(revisionTestPlayerID)
	if p.Coins != coins {
		t.Fatal("wire retry duplicated reward")
	}
	raw, _ := json.Marshal(p.ProgressSlots["74"])
	var pp map[string]any
	if err := json.Unmarshal(raw, &pp); err != nil || pp["pp"] == nil {
		t.Fatal("wrong stock slot shape")
	}
}
