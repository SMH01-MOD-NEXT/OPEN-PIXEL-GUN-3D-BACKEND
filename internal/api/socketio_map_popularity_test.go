// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func popularityParams(t *testing.T, raw string) sioHead {
	t.Helper()
	var head sioHead
	if err := json.Unmarshal([]byte(raw), &head); err != nil {
		t.Fatal(err)
	}
	return head
}

func TestMapPopularityStockContractEncryptedRoundTrip(t *testing.T) {
	s, _ := newTestServer(t)
	head := popularityParams(t, `{"params":{"0":{"16.6.0mm_v2":{"0":["1001","1035"]}},"2":{"16.6.0mm_v2":{"0":["10038","1043"],"5":[]}}}}`)
	body := sioAnswerFor("get_map_popularity")(s, nil, head, 103)
	if _, exists := body["err_code"]; exists {
		t.Fatalf("stock callback takes the error branch whenever err_code exists: %#v", body)
	}
	codec := codecForPinnedSid(t)
	frames, err := encodeEncryptedEvent(codec, "/sio", "get_map_popularity", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("frames: %d", len(frames))
	}
	ciphertext, _ := stripBinaryPrefix(frames[2].data)
	plain, err := codec.Decrypt(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ReqID int               `json:"req_id"`
		Data  mapPopularityData `json:"data"`
	}
	if err := json.Unmarshal(plain, &decoded); err != nil {
		t.Fatalf("reply does not match the client's 3-level integer dictionary: %s: %v", plain, err)
	}
	want := mapPopularityData{"0": {"0": {"1001": 0, "1035": 0}}, "2": {"0": {"10038": 0, "1043": 0}, "5": {}}}
	if decoded.ReqID != 103 || !reflect.DeepEqual(decoded.Data, want) {
		t.Fatalf("decoded reply = %#v; want %#v and req_id 103", decoded, want)
	}
}

func TestMapPopularityMergesVersionsWithoutAliasingRequest(t *testing.T) {
	head := popularityParams(t, `{"params":{"2":{"v1":{"0":["10038","10038"]},"v2":{"0":["1043"],"100":["4024"]}}}}`)
	before, _ := json.Marshal(head)
	data, ok := emptyRequestedMapPopularity(head["params"])
	want := mapPopularityData{"2": {"0": {"10038": 0, "1043": 0}, "100": {"4024": 0}}}
	if !ok || !reflect.DeepEqual(data, want) {
		t.Fatalf("data = %#v, valid = %v", data, ok)
	}
	data["2"]["0"]["10038"] = 42
	after, _ := json.Marshal(head)
	if string(before) != string(after) {
		t.Fatal("mutated input params")
	}
	again, _ := emptyRequestedMapPopularity(head["params"])
	if again["2"]["0"]["10038"] != 0 {
		t.Fatal("invented counts survived between requests")
	}
}

func TestMapPopularityEmptyCatalogIsAnObject(t *testing.T) {
	s, _ := newTestServer(t)
	for _, raw := range []string{`{}`, `{"params":null}`, `{"params":{}}`} {
		body := s.answerMapPopularity(nil, popularityParams(t, raw), 8)
		encoded, err := json.Marshal(body["data"])
		if err != nil || string(encoded) != "{}" {
			t.Fatalf("%s: data = %s, err = %v", raw, encoded, err)
		}
	}
}

func TestMapPopularityRejectsMalformedOrUnboundedParams(t *testing.T) {
	s, _ := newTestServer(t)
	cases := []string{
		`{"params":[]}`, `{"params":{"bad":{}}}`, `{"params":{"01":{}}}`,
		`{"params":{"2147483648":{}}}`, `{"params":{"-1":{}}}`,
		`{"params":{"2":[]}}`, `{"params":{"2":{"":{}}}}`,
		`{"params":{"2":{"v":[]}}}`, `{"params":{"2":{"v":{"bad":[]}}}}`,
		`{"params":{"2":{"v":{"0":null}}}}`, `{"params":{"2":{"v":{"0":[10038]}}}}`,
		`{"params":{"2":{"v":{"0":[""]}}}}`, `{"params":{"2":{"v":{"0":["a\n"]}}}}`,
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			body := s.answerMapPopularity(nil, popularityParams(t, raw), 19)
			if body["err_code"] != 1 || body[sioRequestIDKey] != 19 || body["data"] != nil {
				t.Fatalf("malformed request got success/partial data: %#v", body)
			}
		})
	}
	for _, dimension := range []string{"modes", "versions", "groups", "maps", "total_maps", "total_groups", "key_length"} {
		t.Run(dimension, func(t *testing.T) {
			maps := []any{"10038"}
			groups := map[string]any{"0": maps}
			versions := map[string]any{"v": groups}
			modes := map[string]any{"2": versions}
			switch dimension {
			case "modes":
				for i := 0; i <= mapPopularityMaxModes; i++ {
					modes[fmt.Sprint(i)] = versions
				}
			case "versions":
				for i := 0; i <= mapPopularityMaxVersions; i++ {
					versions[fmt.Sprint(i)] = groups
				}
			case "groups":
				for i := 0; i <= mapPopularityMaxGroups; i++ {
					groups[fmt.Sprint(i)] = maps
				}
			case "maps":
				groups["0"] = make([]any, mapPopularityMaxEntries+1)
			case "total_maps":
				many := make([]any, mapPopularityMaxEntries/2+1)
				for i := range many {
					many[i] = "10038"
				}
				groups["0"] = many
				groups["1"] = many
			case "total_groups":
				for i := 0; i < mapPopularityMaxGroups; i++ {
					groups[fmt.Sprint(i)] = []any{}
				}
				for i := 0; i < mapPopularityMaxVersions; i++ {
					versions[fmt.Sprint(i)] = groups
				}
			case "key_length":
				groups["0"] = []any{string(make([]byte, mapPopularityMaxKeyBytes+1))}
			}
			if data, ok := emptyRequestedMapPopularity(modes); ok || data != nil {
				t.Fatal("unbounded request accepted")
			}
		})
	}
}
