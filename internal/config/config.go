// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads server configuration from the environment.
//
// Credentials (Photon AppIDs, the AppID encryption key, the admin token) are
// only ever read from the environment, never from a committed file, so the
// repository can be published under the GPL without redacting anything.
package config

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/secrets"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
)

// Update describes the client build the server currently hands out.
type Update struct {
	Version   string `json:"version"`
	APKURL    string `json:"apk_url"`
	SHA256    string `json:"sha256"`
	NotesURL  string `json:"notes_url"`
	Mandatory bool   `json:"mandatory"`
}

// Config is the fully resolved runtime configuration.
type Config struct {
	ServerName     string
	Addr           string
	DataDir        string
	LogDir         string
	ContentDir     string
	TraceBodies    bool
	MaxBodyLog     int
	DiscoveryPort  int
	AdminToken     string
	HubSecurityKey []byte
	HubDownloadURL string
	WebAuthOrigin  string // Exact HTTPS origin; empty disables website login.
	Maintenance    bool

	// BundleDir is the directory under ContentDir holding the asset bundles the
	// client downloads, laid out as <name>/<md5>/<name>. Those files are the
	// game's content, not this project's, so they are never committed:
	// tools/fetch-bundles.sh builds the directory from a client's own manifest.
	BundleDir string

	// BundleUpstream is the vendor CDN a bundle may be fetched from when it is
	// missing locally. Set it empty to keep the server strictly offline.
	BundleUpstream string

	// BundleFetchOnMiss lets the server fill a gap in its bundle tree on demand
	// rather than failing the download, which keeps a partially filled install
	// playable. Fetched bytes are only stored once they hash to the md5 the
	// request asked for.
	BundleFetchOnMiss bool

	// StableSessions derives each account's session token from its device id
	// instead of minting random bytes, and pins the socket.io session id to a
	// fixed value.
	//
	// SECURITY-SENSITIVE DIAGNOSTIC, off by default. A deterministic session
	// token and a predictable socket id are both guessable credentials, so this
	// belongs on a lab machine only. It exists to answer one question about the
	// /sio payload codec: the client's encryption is deterministic, yet the same
	// auth_v2 event is different in every run, so some key input varies per
	// session. The first attempt pinned only the token and left the socket id
	// random, which could not separate the two candidates. Pinning both freezes
	// every per-session value this server originates, so a ciphertext that still
	// changes proves the varying input is client-local.
	StableSessions bool

	// SioReply answers events on the encrypted /sio channel instead of only
	// tracing them. On by default, because an unanswered auth_v2 is exactly what
	// holds this client's loading bar at 78%: silence is not a safe fallback. It
	// is a switch because a wrong answer can be worse than none - a malformed
	// packet may read to the client as a protocol error - so the trace-only
	// behaviour can be restored without a rebuild.
	SioReply bool

	// SioKey, SioIV and SioMode pin the /sio payload codec to key material
	// captured from a traced client launch, skipping the key derivation.
	//
	// SECURITY-SENSITIVE DIAGNOSTIC, off by default. The client derives its
	// cipher password per connection from an input that includes the socket.io
	// session id, and that derivation is not reproduced yet, so the key cannot
	// be computed here. A captured key can still be pinned by hand, which is
	// only useful together with StableSessions: the same sid has to be issued
	// again for the captured key to be the one the client actually uses.
	SioKey  []byte
	SioIV   []byte
	SioMode sio.Mode

	// PhotonAppIDs is keyed by mode: "pun", "realtime", "chat", "voice".
	PhotonAppIDs map[string]string
	PhotonRegion string

	// PhotonPUNFromRealtime records that "pun" is answered with the Realtime
	// AppID because no dedicated PUN AppID was configured.
	PhotonPUNFromRealtime bool

	// AppIDKey is the AES-256 key used to seal AppIDs. Empty disables the
	// endpoint instead of falling back to clear text.
	AppIDKey []byte

	// ConfigAlwaysServe answers every poll for a hosted document with the whole
	// document, even when the client says it already holds that hash.
	//
	// DIAGNOSTIC, off by default. Runs 59 and 60 measured the push path storing
	// all six documents (cfgsys store id=... ok=1) while the read path answered
	// get-text with nothing and the client's apply entry point never ran once.
	// Serving a body the client did not ask for is the one variable that puts
	// the poll answer back in play as the path that applies a document.
	ConfigAlwaysServe bool

	// ConfigPush hands the hosted documents to the config system on the
	// config_info_versions event. On by default: that push is what run 57
	// measured writing every document into the client's own storage. It is a
	// switch so a run can take the push away and see what the poll answer
	// alone does, which is the only way to tell the two delivery paths apart.
	ConfigPush bool

	// ConfigHashNonce salts the fingerprint this server states for every hosted
	// document with a value that changes each time the server starts.
	//
	// DIAGNOSTIC, off by default. Run 64 measured the client's own reason for
	// ignoring a delivered document: its config system looks the name up in the
	// dictionary its readers consult, compares the fingerprint it already holds
	// with the one being delivered, and skips the body when the two are equal.
	// The dictionary is rebuilt from storage at boot with fingerprints but
	// without texts, so a document this client already stored once can never be
	// delivered again: the fingerprints match and the text stays missing. A
	// fingerprint the client cannot already hold takes the other branch.
	ConfigHashNonce bool

	// ConfigPushAliases widens the vocabulary of one push record.
	//
	// A record states two keys the handler is known to read: val, the index of
	// the attachment that carries the body, and hash, the fingerprint of that
	// body. Runs 64 to 66 measured what happens next: the handler stores the
	// body, and for a name it has no entry for it even creates one, yet every
	// later read of that name returns an empty string. The reader takes the
	// text out of the entry under a key of its own, so a record that never
	// spells that key leaves the entry text-less no matter how the fingerprint
	// compares. This states the same body and fingerprint under the cache
	// vocabulary the client uses in its own poll as well.
	ConfigPushAliases bool

	// ConfigPushWrapper sends every pushed body as the cache record the
	// client's reader consults, rather than as the bare document.
	//
	// DIAGNOSTIC, off by default. Run 75 read the reader's own dictionary
	// while a pushed name was looked up. The entry the push handler leaves
	// behind holds two members and both are spelled in the cache vocabulary:
	// TopUp is {c: "", h: "5181351cf6887c00"} and Boosters is
	// {c: "", h: "c5078ace838a9a63"}. Those fingerprints are this server's
	// own, so the handler did read the record and did write an entry - and
	// the text is String.Empty, the same object for every pushed name. An
	// embedded name read through that very same code answers with its
	// document, so the handler does not put the attachment bytes under c: it
	// parses those bytes as the h_<16 hex>_c_<object> plaintext the client's
	// cache stores and copies the members across. A bare document has no c
	// member, which is the empty string every reader has returned since run
	// 57.
	ConfigPushWrapper bool
	// ConfigPushInline forces every hosted document into the pushed entry
	// as text instead of sending a binary attachment index.
	//
	// DIAGNOSTIC, off by default. Production selects inline delivery only for
	// the GameModeEvents family (runs 172-174) and Boosters (ConfigId 167),
	// whose stock readers consume JSON text;
	// globally inlining unrelated documents caused their parsers to consume
	// unverified JSON and must not be a production dependency.
	ConfigPushInline bool
	// ProgressSynchronizeMode selects progress_status code 2 for an ordinary
	// stored profile that has no unacknowledged operator revision. Code 2 starts
	// the stock state-13 upload path; code 1 starts state 8 and then the
	// correlated state-9 download once get_last_command supplies a non-null
	// empty local cursor.
	//
	// Pending operator revisions deliberately override this broad setting. The
	// first boot always uses code 1 to download authoritative slots. Reaching
	// Login stages a second boot, which always uses code 2 so replace_progress
	// can echo the raw slots. Only an exact echo is acknowledged. A mismatching
	// upload returns the device to code 1 on its next boot. Thus switching this
	// flag off changes ordinary stored-profile behavior, but cannot strand a
	// revision between its measured download and proof phases.
	//
	// Run 60 measured code 2 uploading the old local snapshot. Run 62 measured
	// code 1 reaching state 9, applying 87 server slots, and entering the lobby
	// with the edited currencies. Server authority protects the four editable
	// counters throughout both paths. On by default for ordinary compatibility.
	ProgressSynchronizeMode bool
	// ProgressBlankUploadGuard keeps a blank client snapshot from erasing the
	// four core counters this server already holds for an account.
	//
	// The 2026-09-01 device run measured the erasure directly. The client
	// finished the slot handshake and sent replace_progress carrying its own
	// local slot 3 and slot 4, both blank, 62 ms after this server answered
	// with the 87 stored slot ids. store.projectReplacementSlotState reads
	// exactly those two slots and zeroes the currencies and the level before
	// reading them back, which is correct for a genuine reset and wrong for a
	// client that simply has not been told what the server holds yet. The
	// operator edit went from 123123123 coins to zero in one write.
	//
	// With the guard on, a slot 3 that projects to no currency at all and a
	// slot 4 that projects to level one with no experience are refilled from
	// the stored profile before the snapshot is installed. Every other slot in
	// the upload is stored exactly as it arrived. The trade-off is deliberate
	// and narrow: the server wins the four counters an operator can edit, and
	// the client keeps ownership of everything else. A real reset to zero from
	// a non-blank profile is the case this refuses, which is why it is a flag.
	ProgressBlankUploadGuard bool
	// ProgressSlotsAlwaysCreate omits the slots member from every
	// get_progress_slots answer, including for an account that already has
	// a stored set.
	//
	// Run 75 reached 90% against an account with nothing stored: the answer
	// carried no slots member, the client uploaded its own 63 slots through
	// create_slots, and the boot went on to update_progress, refund_get and
	// unsubscribe. Runs 78 to 80 served those 63 slots back and stopped at
	// 87% with that entire branch missing - six subscribes instead of
	// twelve and no update_progress at all. Serving the set the client is
	// about to send anyway costs the boot the work that follows it.
	ProgressSlotsAlwaysCreate bool
	// TutorialSectionSeed publishes progress-document section 56 with tutorial
	// 1100 marked completed for accounts that carry no tutorials section of
	// their own.
	//
	// The client gates two boot steps on that one answer, and an account this
	// server created has never played the tutorial, so the section it uploads
	// is an empty list and the answer is always no. Runs 89 to 107 only got
	// past it because a temporary frontend hook lied about it; owning the
	// section here is what lets that hook be deleted. A stored section always
	// wins, so a player's real history is never rewritten.
	TutorialSectionSeed bool
	// TutorialSectionIDs are the completed tutorial ids the seed publishes.
	//
	// 1100 is the id both boot gates ask about. 1500 is there because the
	// client replaces its local tutorial history with whatever arrives: run 107
	// saw the device answer yes for 1500 out of its own history, and after a
	// section carrying only 1100 landed, run 113 saw that turn into a no and the
	// boot wall itself at 90%. A full set is measured to be wrong too - run 102
	// marked every id and the boot left the ladder for 75 seconds - so the set
	// is exactly the ids the boot is known to need, and it stays an operator
	// switch so a run can widen it without a rebuild.
	TutorialSectionIDs []int
	// TutorialSectionViaProgressDocument also publishes the section in the
	// init_progress document. Run 108 measured that this channel never reaches
	// the tutorial manager, so it is off unless a run asks for it again.
	TutorialSectionViaProgressDocument bool
	// TutorialSectionViaSlots publishes the same tutorials section through the
	// get_progress_slots answer instead of the init_progress document.
	//
	// Run 108 proved the two channels are not interchangeable: the section went
	// out with the init_progress document, the client applied that document,
	// and its tutorial manager still answered no 1200 times while the bar sat
	// at 75%. The client's own copy of section 56 arrives in create_slots, in
	// the 25..91 range, so the slot document is where it belongs.
	TutorialSectionViaSlots bool
	// TutorialSectionViaCreateSlots returns the tutorials section in the
	// acknowledgement of create_slots instead of in get_progress_slots.
	//
	// Run 110 measured why this channel exists. Serving the section in
	// get_progress_slots did answer the boot's tutorial question - the client
	// reported the tutorial completed for the first time without any frontend
	// hook - but a non-empty slots member also tells the client its slots are
	// already on the server, so it skipped its create_slots upload and the
	// ladder stopped at 87%, the same signature runs 58 and 78-80 produced.
	// Answering the upload itself keeps that branch intact.
	TutorialSectionViaCreateSlots bool
	// TutorialSectionViaSyncFull merges the same seed into the tutorials
	// section of the stored slot document that sync_full returns.
	//
	// The 2026-10-26 AWS logs measured the case it covers: a new account
	// stores its create_slots upload with section 56 {"t":[]}, because the
	// seed only travels on the create_slots acknowledgement. When a failed
	// update_progress made the client resync, sync_full returned that stale
	// section, the client replaced its history with it and replayed the
	// tutorial. The merge only adds the seed ids, only to a section the stored
	// document already holds, and only in the reply; stored state is untouched.
	TutorialSectionViaSyncFull bool

	Update Update
}

// Load resolves configuration from the environment.
func Load() (*Config, error) {
	cfg := &Config{
		WebAuthOrigin:                      strings.TrimSpace(os.Getenv("OPG3D_WEB_AUTH_ORIGIN")),
		ServerName:                         env("OPG3D_SERVER_NAME", "open-pixel-gun-3d-backend"),
		Addr:                               env("OPG3D_ADDR", ":8080"),
		DataDir:                            env("OPG3D_DATA_DIR", "data"),
		LogDir:                             env("OPG3D_LOG_DIR", "logs"),
		ContentDir:                         env("OPG3D_CONTENT_DIR", "content"),
		BundleDir:                          env("OPG3D_BUNDLE_DIR", "bundles"),
		BundleUpstream:                     env("OPG3D_BUNDLE_UPSTREAM", "https://pixelgun3d.akamaized.net"),
		BundleFetchOnMiss:                  envBool("OPG3D_BUNDLE_FETCH_ON_MISS", true),
		TraceBodies:                        envBool("OPG3D_TRACE_BODIES", true),
		MaxBodyLog:                         envInt("OPG3D_MAX_BODY_LOG", 8192),
		DiscoveryPort:                      envInt("OPG3D_DISCOVERY_PORT", 47318),
		AdminToken:                         env("OPG3D_ADMIN_TOKEN", ""),
		Maintenance:                        envBool("OPG3D_MAINTENANCE", false),
		StableSessions:                     envBool("OPG3D_STABLE_SESSIONS", false),
		SioReply:                           envBool("OPG3D_SIO_REPLY", true),
		ConfigAlwaysServe:                  envBool("OPG3D_CONFIG_ALWAYS_SERVE", false),
		ConfigPush:                         envBool("OPG3D_CONFIG_PUSH", true),
		ConfigHashNonce:                    envBool("OPG3D_CONFIG_HASH_NONCE", false),
		ConfigPushAliases:                  envBool("OPG3D_CONFIG_PUSH_ALIASES", false),
		ConfigPushWrapper:                  envBool("OPG3D_CONFIG_PUSH_WRAPPER", false),
		ConfigPushInline:                   envBool("OPG3D_CONFIG_PUSH_INLINE", false),
		ProgressSynchronizeMode:            envBool("OPG3D_PROGRESS_SYNCHRONIZE_MODE", true),
		ProgressBlankUploadGuard:           envBool("OPG3D_PROGRESS_BLANK_UPLOAD_GUARD", true),
		ProgressSlotsAlwaysCreate:          envBool("OPG3D_PROGRESS_SLOTS_ALWAYS_CREATE", false),
		TutorialSectionSeed:                envBool("OPG3D_TUTORIAL_SECTION_SEED", true),
		TutorialSectionIDs:                 envInts("OPG3D_TUTORIAL_SECTION_IDS", []int{1100, 1500}),
		TutorialSectionViaProgressDocument: envBool("OPG3D_TUTORIAL_SECTION_VIA_PROGRESS_DOCUMENT", false),
		TutorialSectionViaSlots:            envBool("OPG3D_TUTORIAL_SECTION_VIA_SLOTS", false),
		TutorialSectionViaCreateSlots:      envBool("OPG3D_TUTORIAL_SECTION_VIA_CREATE_SLOTS", true),
		TutorialSectionViaSyncFull:         envBool("OPG3D_TUTORIAL_SECTION_VIA_SYNC_FULL", true),
		PhotonAppIDs:                       map[string]string{},
		PhotonRegion:                       env("OPG3D_PHOTON_REGION", "eu"),
		Update: Update{
			Version:   env("OPG3D_UPDATE_VERSION", ""),
			APKURL:    env("OPG3D_UPDATE_APK_URL", ""),
			SHA256:    env("OPG3D_UPDATE_SHA256", ""),
			NotesURL:  env("OPG3D_UPDATE_NOTES_URL", ""),
			Mandatory: envBool("OPG3D_UPDATE_MANDATORY", false),
		},
	}

	for mode, key := range map[string]string{
		"pun":      "OPG3D_PHOTON_APPID_PUN",
		"realtime": "OPG3D_PHOTON_APPID_REALTIME",
		"chat":     "OPG3D_PHOTON_APPID_CHAT",
		"voice":    "OPG3D_PHOTON_APPID_VOICE",
	} {
		if value := env(key, ""); value != "" {
			cfg.PhotonAppIDs[mode] = value
		}
	}

	// PUN is a client library, not a Photon product: a PUN client connects with
	// the AppID of a Realtime application. Serving the Realtime AppID for "pun"
	// keeps the client from blocking on its AppID wait when only one Photon
	// application exists, which is the normal case for a private server.
	if cfg.PhotonAppIDs["pun"] == "" && cfg.PhotonAppIDs["realtime"] != "" {
		cfg.PhotonAppIDs["pun"] = cfg.PhotonAppIDs["realtime"]
		cfg.PhotonPUNFromRealtime = true
	}

	if material := env("OPG3D_APPID_KEY", ""); material != "" {
		key, err := secrets.DeriveKey(material)
		if err != nil {
			return nil, fmt.Errorf("config: OPG3D_APPID_KEY: %w", err)
		}
		cfg.AppIDKey = key
	}

	if err := loadSioCodec(cfg); err != nil {
		return nil, err
	}

	if cfg.DiscoveryPort < 0 || cfg.DiscoveryPort > 65535 {
		return nil, fmt.Errorf("config: OPG3D_DISCOVERY_PORT out of range: %d", cfg.DiscoveryPort)
	}
	cfg.HubDownloadURL = strings.TrimSpace(os.Getenv("OPG3D_HUB_DOWNLOAD_URL"))
	if cfg.HubDownloadURL != "" {
		u, e := url.Parse(cfg.HubDownloadURL)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return nil, fmt.Errorf("config: HUB download URL must be HTTPS")
		}
	}
	if raw := strings.TrimSpace(os.Getenv("OPG3D_HUB_SECURITY_KEY")); raw != "" {
		key, e := hex.DecodeString(raw)
		if e != nil || len(key) != 32 {
			return nil, fmt.Errorf("config: HUB security key must contain 32 random bytes encoded as hex")
		}
		cfg.HubSecurityKey = key
	}
	if cfg.WebAuthOrigin != "" {
		u, err := url.Parse(cfg.WebAuthOrigin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || cfg.StableSessions {
			return nil, fmt.Errorf("config: OPG3D_WEB_AUTH_ORIGIN must be an exact HTTPS origin and requires random game sessions")
		}
	}
	return cfg, nil
}

// loadSioCodec resolves the pinned /sio codec material.
//
// The key and the IV have to arrive together: half a codec cannot decrypt
// anything, so it is reported as a configuration error instead of being
// silently dropped. The mode defaults to the variant measured against real
// traffic and only has to be given when re-testing that conclusion.
func loadSioCodec(cfg *Config) error {
	keyHex, ivHex := env("OPG3D_SIO_KEY", ""), env("OPG3D_SIO_IV", "")
	if keyHex == "" && ivHex == "" {
		return nil
	}
	if keyHex == "" || ivHex == "" {
		return fmt.Errorf("config: OPG3D_SIO_KEY and OPG3D_SIO_IV must be set together")
	}

	key, err := hexBlob("OPG3D_SIO_KEY", keyHex, sio.KeyLen)
	if err != nil {
		return err
	}
	iv, err := hexBlob("OPG3D_SIO_IV", ivHex, sio.IVLen)
	if err != nil {
		return err
	}

	mode := sio.DefaultMode
	if name := env("OPG3D_SIO_MODE", ""); name != "" {
		if mode, err = sio.ParseMode(name); err != nil {
			return fmt.Errorf("config: OPG3D_SIO_MODE: %w", err)
		}
	}

	cfg.SioKey, cfg.SioIV, cfg.SioMode = key, iv, mode
	return nil
}

func hexBlob(name, value string, want int) ([]byte, error) {
	blob, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", name, err)
	}
	if len(blob) != want {
		return nil, fmt.Errorf("config: %s must be %d bytes of hex, got %d", name, want, len(blob))
	}
	return blob, nil
}

// SealsAppIDs reports whether the AppID endpoint can serve anything.
func (c *Config) SealsAppIDs() bool {
	return len(c.AppIDKey) > 0 && len(c.PhotonAppIDs) > 0
}

// DecodesSio reports whether a /sio payload codec is pinned.
func (c *Config) DecodesSio() bool {
	return len(c.SioKey) == sio.KeyLen && len(c.SioIV) == sio.IVLen
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := env(key, "")
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// envInts reads a comma-separated list of integers. A malformed entry is
// skipped rather than fatal, and an entirely unusable value falls back, so a
// typo in a run's environment cannot silently empty a measured default.
func envInts(key string, fallback []int) []int {
	value := env(key, "")
	if value == "" {
		return fallback
	}
	parsedAll := make([]int, 0, len(fallback))
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		parsed, err := strconv.Atoi(part)
		if err != nil {
			continue
		}
		parsedAll = append(parsedAll, parsed)
	}
	if len(parsedAll) == 0 {
		return fallback
	}
	return parsedAll
}

func envInt(key string, fallback int) int {
	value := env(key, "")
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
