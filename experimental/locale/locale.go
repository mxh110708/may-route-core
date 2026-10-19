package locale

import (
	"context"
	"strings"
	"sync/atomic"

	"golang.org/x/text/language"
)

var (
	localeRegistry = map[string]*Locale{
		"en": defaultLocale,
	}
	current atomic.Pointer[Locale]
)

type Locale struct {
	Locale                  string
	DeprecatedMessage       string
	DeprecatedMessageNoLink string
	InsecureFeatureMessage  string
	ExternalPathFeature     string
	TailscaleInitializing   string
	TailscaleInUse          string
	TailscaleNeedsLogin     string
	TailscaleNeedsApproval  string
	TailscaleStopped        string
	TailscaleStarting       string
	TailscaleRunning        string
	TaildropReceiving       string
	TaildropReceived        string
	TaildropSendCanceled    string
	VPNConnecting           string
	VPNAuthentication       string
	VPNConnected            string
	VPNError                string
	Unknown                 string
}

var defaultLocale = &Locale{
	Locale:                  "en",
	DeprecatedMessage:       "%s is deprecated in sing-box %s and will be removed in sing-box %s. Please check the documentation for migration.",
	DeprecatedMessageNoLink: "%s is deprecated in sing-box %s and will be removed in sing-box %s.",
	InsecureFeatureMessage:  "%s is considered insecure in the graphical client for sing-box on %s. Enable Insecure Mode in `Settings - Core - Insecure Mode` to use it.",
	ExternalPathFeature:     "Access to %s (outside of the working directory) is considered insecure in the graphical client for sing-box on %s. Enable Insecure Mode in `Settings - Core - Insecure Mode` to use it.",
	TailscaleInitializing:   "Initializing",
	TailscaleInUse:          "In use by another user",
	TailscaleNeedsLogin:     "Needs login",
	TailscaleNeedsApproval:  "Needs approval",
	TailscaleStopped:        "Stopped",
	TailscaleStarting:       "Starting",
	TailscaleRunning:        "Running",
	TaildropReceiving:       "Receiving %s from %s",
	TaildropReceived:        "%s received from %s",
	TaildropSendCanceled:    "Sending %s canceled by receiver",
	VPNConnecting:           "Connecting",
	VPNAuthentication:       "Authentication required",
	VPNConnected:            "Connected",
	VPNError:                "Error",
	Unknown:                 "Unknown",
}

type contextKey struct{}

func init() {
	current.Store(defaultLocale)
}

func Current() *Locale {
	return current.Load()
}

func ContextWithLocale(ctx context.Context, localeID string) (context.Context, bool) {
	selectedLocale, loaded := selectLocale(localeID)
	if !loaded {
		return ctx, false
	}
	return context.WithValue(ctx, contextKey{}, selectedLocale), true
}

func FromContext(ctx context.Context) *Locale {
	selectedLocale, loaded := ctx.Value(contextKey{}).(*Locale)
	if loaded {
		return selectedLocale
	}
	return Current()
}

func selectLocale(localeID string) (*Locale, bool) {
	localeName, loaded := match(localeID)
	if !loaded {
		return nil, false
	}
	selectedLocale, loaded := localeRegistry[localeName]
	return selectedLocale, loaded
}

func Set(localeID string) bool {
	selectedLocale, loaded := selectLocale(localeID)
	if !loaded {
		return false
	}
	current.Store(selectedLocale)
	return true
}

func match(localeID string) (string, bool) {
	localeEntries := strings.Split(localeID, ",")
	for i, localeEntry := range localeEntries {
		languageID, options, hasOptions := strings.Cut(localeEntry, ";")
		languageID, _, _ = strings.Cut(strings.TrimSpace(languageID), "@")
		languageID = strings.ReplaceAll(languageID, "_", "-")
		if !hasOptions {
			languageID, _, _ = strings.Cut(languageID, ".")
		}
		switch {
		case strings.EqualFold(languageID, "C"), strings.EqualFold(languageID, "POSIX"):
			languageID = "en"
		case strings.EqualFold(languageID, "zh-CHS"):
			languageID = "zh-Hans"
		case strings.EqualFold(languageID, "zh-CHT"):
			languageID = "zh-Hans"
		}
		localeEntries[i] = languageID
		if hasOptions {
			localeEntries[i] += ";" + options
		}
	}
	localeID = strings.Join(localeEntries, ",")
	tags, _, err := language.ParseAcceptLanguage(localeID)
	if err != nil || len(tags) == 0 {
		return "", false
	}
	for _, tag := range tags {
		base, _, _ := tag.Raw()
		switch base.String() {
		case "zh", "cmn":
			return "zh-Hans", true
		case "en":
			return "en", true
		}
	}
	return "en", true
}

func (l *Locale) TailscaleStateText(state string) string {
	switch state {
	case "NoState":
		return l.TailscaleInitializing
	case "InUseOtherUser":
		return l.TailscaleInUse
	case "NeedsLogin":
		return l.TailscaleNeedsLogin
	case "NeedsMachineAuth":
		return l.TailscaleNeedsApproval
	case "Stopped":
		return l.TailscaleStopped
	case "Starting":
		return l.TailscaleStarting
	case "Running":
		return l.TailscaleRunning
	default:
		return l.Unknown
	}
}

func (l *Locale) VPNStateText(state string) string {
	switch state {
	case "connecting":
		return l.VPNConnecting
	case "auth-pending":
		return l.VPNAuthentication
	case "connected":
		return l.VPNConnected
	case "error":
		return l.VPNError
	default:
		return l.Unknown
	}
}
