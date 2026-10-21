// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"bytes"
	"encoding/json"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/pixelpass"
	"path/filepath"
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
