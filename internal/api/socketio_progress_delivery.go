// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "sync"

type progressDeliveryKey struct {
	playerID string
	device   string
}

// progressDeliveryStages is the process-local bridge between two stock boot
// flows. The first boot downloads authoritative slots through state 9; the next
// boot uses state 13 so replace_progress can echo the raw values. A process
// restart intentionally loses this bridge and repeats the safe download rather
// than risking an acknowledgement without proof.
type progressDeliveryStages struct {
	mu        sync.Mutex
	revisions map[progressDeliveryKey]uint64
}

func (d *progressDeliveryStages) has(playerID, device string, revision uint64) bool {
	if playerID == "" || device == "" || revision == 0 {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.revisions[progressDeliveryKey{playerID: playerID, device: device}] == revision
}

func (d *progressDeliveryStages) mark(playerID, device string, revision uint64) bool {
	if playerID == "" || device == "" || revision == 0 {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.revisions == nil {
		d.revisions = make(map[progressDeliveryKey]uint64)
	}
	key := progressDeliveryKey{playerID: playerID, device: device}
	d.revisions[key] = revision
	return true
}

func (d *progressDeliveryStages) clear(playerID, device string, revision uint64) bool {
	if playerID == "" || device == "" || revision == 0 {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	key := progressDeliveryKey{playerID: playerID, device: device}
	if d.revisions[key] != revision {
		return false
	}
	delete(d.revisions, key)
	return true
}

func (s *Server) progressRevisionWasDelivered(playerID, device string, revision uint64) bool {
	return s != nil && s.progressDeliveries.has(playerID, device, revision)
}

func (s *Server) stageProgressRevisionDelivery(playerID, device string, revision uint64) bool {
	if s == nil || s.st == nil {
		return false
	}
	profile, err := s.st.Profile(playerID)
	if err != nil || profile.ProgressRevision != revision {
		return false
	}
	return s.progressDeliveries.mark(playerID, device, revision)
}

func (s *Server) clearProgressRevisionDelivery(playerID, device string, revision uint64) bool {
	return s != nil && s.progressDeliveries.clear(playerID, device, revision)
}
