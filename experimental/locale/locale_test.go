package locale

import (
	"context"
	"testing"
)

func TestSupportedLocales(t *testing.T) {
	if len(localeRegistry) != 2 {
		t.Fatalf("unexpected registered locales: %v", localeRegistry)
	}
	for _, name := range []string{"en", "zh-Hans"} {
		if selected := localeRegistry[name]; selected == nil || selected.Locale != name {
			t.Fatalf("missing locale %s", name)
		}
	}
}

func TestLocaleSelection(t *testing.T) {
	cases := map[string]string{
		"en": "en", "en_GB.UTF-8": "en", "C": "en", "POSIX": "en",
		"zh": "zh-Hans", "zh-CN": "zh-Hans", "zh-TW": "zh-Hans", "zh-HK": "zh-Hans", "zh-MO": "zh-Hans",
		"zh-Hant": "zh-Hans", "zh_Hant_TW.UTF-8": "zh-Hans", "zh-CHS": "zh-Hans", "zh-CHT": "zh-Hans",
		"cmn": "zh-Hans", "ru": "en", "fa-IR": "en", "de-DE": "en",
		"ru-RU,zh-HK;q=0.8,en;q=0.5": "zh-Hans",
		"zh-CN;q=0.5,en-US;q=0.9":    "en",
		"en;q=0,zh-TW;q=0.5":         "zh-Hans",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			ctx, ok := ContextWithLocale(context.Background(), input)
			if !ok || FromContext(ctx).Locale != want {
				t.Fatalf("locale for %q = %s (%v), want %s", input, FromContext(ctx).Locale, ok, want)
			}
		})
	}
	for _, input := range []string{"", "not a language", "en;q=broken"} {
		if _, ok := ContextWithLocale(context.Background(), input); ok {
			t.Errorf("invalid locale accepted: %q", input)
		}
	}
}
