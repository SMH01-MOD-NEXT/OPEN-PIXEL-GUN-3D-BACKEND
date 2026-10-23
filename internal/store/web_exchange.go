// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/exchange"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/pixelpass"
)

var ErrWebExchange = errors.New("store: exchange unavailable or invalid")
var ErrWebFunds = errors.New("store: insufficient funds")

type WebExchangeReceipt struct {
	OfferID int       `json:"offer_id"`
	At      time.Time `json:"at"`
}
type WebExchangeRequest struct {
	OfferID        int       `json:"offer_id"`
	Operation      string    `json:"operation"`
	CatalogVersion string    `json:"catalog_version"`
	Revision       uint64    `json:"revision"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func ExchangeCatalogVersion() string {
	b, _ := json.Marshal(exchange.Offers())
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *Store) WebExchange(playerID string, request WebExchangeRequest) (Profile, bool, error) {
	return s.webExchange(playerID, request, nil, "", time.Now().UTC())
}

// WebExchangeOwned rechecks the verified account incarnation and bans under the
// same store lock as the wallet write, closing an account-rebinding race.
func (s *Store) WebExchangeOwned(owner Account, ip string, request WebExchangeRequest) (Profile, bool, error) {
	return s.webExchange(owner.PlayerID, request, &owner, ip, time.Now().UTC())
}
func (s *Store) webExchange(playerID string, request WebExchangeRequest, owner *Account, ip string, now time.Time) (Profile, bool, error) {
	bytes, err := hex.DecodeString(request.Operation)
	offer, ok := exchange.Find(request.OfferID)
	if err != nil || len(bytes) != 16 || request.Operation != strings.ToLower(request.Operation) || !ok || request.CatalogVersion != ExchangeCatalogVersion() {
		return Profile{}, false, ErrWebExchange
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	if owner != nil {
		current := s.state.Accounts[id]
		if current == nil || current.PlayerID != owner.PlayerID || current.DeviceID != owner.DeviceID || current.SocketDevice != owner.SocketDevice || !current.CreatedAt.Equal(owner.CreatedAt) {
			return Profile{}, false, ErrWebExchange
		}
		for _, ban := range s.state.Bans {
			if ban.Active(now) && ((ban.Kind == BanPlayer && s.resolvePlayerIDLocked(ban.Subject) == id) || (ban.Kind == BanDevice && (ban.Subject == current.DeviceID || ban.Subject == current.SocketDevice)) || (ban.Kind == BanIP && ip != "" && ban.Subject == ip)) {
				return Profile{}, false, ErrWebExchange
			}
		}
	}
	p := s.state.Profiles[id]
	if p == nil {
		return Profile{}, false, ErrNotFound
	}
	if old, exists := p.WebExchangeReceipts[request.Operation]; exists {
		if old.OfferID != request.OfferID {
			return Profile{}, false, ErrWebExchange
		}
		return p.clone(), false, nil
	}
	if !pixelpass.Active(now) {
		return Profile{}, false, ErrWebExchange
	}
	if p.ProgressRevision != request.Revision || !p.UpdatedAt.Equal(request.UpdatedAt) {
		return Profile{}, false, ErrInventoryConflict
	}
	candidate := p.clone()
	if candidate.ProgressSlots == nil {
		candidate.ProgressSlots = map[string]any{}
	}
	wallet, err := ensureObject(candidate.ProgressSlots, "3")
	if err != nil {
		return Profile{}, false, ErrWebExchange
	}
	read := func(key string) (int64, bool) {
		if raw, ok := wallet[key]; ok {
			return counterInt64(raw)
		}
		return 0, true
	}
	cost, cok := read(offer.Pay)
	gain, gok := read(offer.Receive)
	if !cok || !gok || cost < 0 || gain < 0 || gain > math.MaxInt32-offer.Amount {
		return Profile{}, false, ErrWebExchange
	}
	if cost < offer.Cost {
		return Profile{}, false, ErrWebFunds
	}
	wallet[offer.Pay] = cost - offer.Cost
	wallet[offer.Receive] = gain + offer.Amount
	candidate.Coins, _ = counterInt64(wallet["Coins"])
	candidate.Gems, _ = counterInt64(wallet["GemsCurrency"])
	if candidate.WebExchangeReceipts == nil {
		candidate.WebExchangeReceipts = map[string]WebExchangeReceipt{}
	}
	for key, r := range candidate.WebExchangeReceipts {
		if now.Sub(r.At) > 24*time.Hour {
			delete(candidate.WebExchangeReceipts, key)
		}
	}
	if len(candidate.WebExchangeReceipts) >= 2048 {
		return Profile{}, false, ErrWebExchange
	}
	candidate.WebExchangeReceipts[request.Operation] = WebExchangeReceipt{OfferID: offer.ID, At: now}
	mirrorRawSlots(&candidate)
	// External wallet changes must fence stale game uploads, but MUST NOT erase
	// gameplay command receipts (replayed purchases must never charge twice).
	candidate.ProgressRevision = nextProgressRevision(candidate.ProgressRevision)
	candidate.AppliedProgressRevisions = nil
	candidate.UpdatedAt = now
	recordWalletEvent(*p, &candidate, "web_exchange", offer.ID, now)
	before := *p
	*p = candidate
	if err := s.persistLocked(); err != nil {
		*p = before
		return Profile{}, false, ErrInventoryPersistence
	}
	return p.clone(), true, nil
}
