// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"strconv"
	"strings"
)

// The stock 23.1.3 MapPopularityController callback (RVA 0x33C433C)
// reads data[mode:int][group:int][map:string] as integer counts. The request
// has an extra matchmaking-version level which MUST NOT appear in the reply.
// Merely acknowledging the request, or returning "popularity", throws a
// KeyNotFoundException on "data" in the unmodified client.
type mapPopularityData map[string]map[string]map[string]int

const (
	mapPopularityMaxModes    = 64
	mapPopularityMaxVersions = 16
	mapPopularityMaxGroups   = 256
	mapPopularityMaxEntries  = 4096
	mapPopularityMaxKeyBytes = 128
)

func (s *Server) answerMapPopularity(_ *sioSession, head sioHead, reqID int) map[string]any {
	data, ok := emptyRequestedMapPopularity(head["params"])
	if !ok {
		return socketStateFailure(reqID, "invalid_params")
	}
	body := sioOK(reqID)
	body["data"] = data
	return body
}

// There is currently no authoritative Photon room-statistics feed in this
// backend. Return explicit zeroes, not fabricated activity, client-reported
// offline matches, or lifetime play totals. This also replaces stale client
// counts for the requested maps. The schema is ready for a real source later;
// this change repairs the wire contract, NOT live population measurement.
func emptyRequestedMapPopularity(raw any) (mapPopularityData, bool) {
	data := make(mapPopularityData)
	// Some clients poll before their map catalog is populated.
	if raw == nil {
		return data, true
	}
	modes, ok := raw.(map[string]any)
	if !ok || len(modes) > mapPopularityMaxModes {
		return nil, false
	}
	entries, groupsSeen := 0, 0
	for mode, rawVersions := range modes {
		if !mapPopularityIntegerKey(mode) {
			return nil, false
		}
		versions, ok := rawVersions.(map[string]any)
		if !ok || len(versions) > mapPopularityMaxVersions {
			return nil, false
		}
		groups := make(map[string]map[string]int)
		data[mode] = groups
		for version, rawGroups := range versions {
			if !mapPopularityTextKey(version) {
				return nil, false
			}
			requestedGroups, ok := rawGroups.(map[string]any)
			if !ok || len(requestedGroups) > mapPopularityMaxGroups {
				return nil, false
			}
			groupsSeen += len(requestedGroups)
			if groupsSeen > mapPopularityMaxEntries {
				return nil, false
			}
			for group, rawMaps := range requestedGroups {
				if !mapPopularityIntegerKey(group) {
					return nil, false
				}
				maps, ok := rawMaps.([]any)
				if !ok || len(maps) > mapPopularityMaxEntries-entries {
					return nil, false
				}
				entries += len(maps)
				if groups[group] == nil {
					groups[group] = make(map[string]int)
				}
				for _, rawMap := range maps {
					name, ok := rawMap.(string)
					if !ok || !mapPopularityTextKey(name) {
						return nil, false
					}
					groups[group][name] = 0
				}
			}
		}
	}
	return data, true
}

// Canonical, nonnegative Int32 keys avoid aliases such as "01" and "1"
// colliding when the client converts both strings to the same dictionary key.
func mapPopularityIntegerKey(key string) bool {
	value, err := strconv.ParseInt(key, 10, 32)
	return err == nil && value >= 0 && strconv.FormatInt(value, 10) == key
}

func mapPopularityTextKey(key string) bool {
	return key != "" && len(key) <= mapPopularityMaxKeyBytes &&
		strings.TrimSpace(key) == key && !strings.ContainsAny(key, "\x00\r\n\t")
}
