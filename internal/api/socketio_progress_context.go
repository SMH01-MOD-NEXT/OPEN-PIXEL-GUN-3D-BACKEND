// SPDX-License-Identifier: GPL-3.0-or-later
package api

import "github.com/SMH01/open-pixel-gun-3d-backend/internal/store"

func (s *sioSession) setProgressWriteRevision(revision uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progressWriteRevision = revision
}

func (s *sioSession) progressWriteContext() store.ProgressWriteContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return store.ProgressWriteContext{Device: s.device, Revision: s.progressWriteRevision}
}
