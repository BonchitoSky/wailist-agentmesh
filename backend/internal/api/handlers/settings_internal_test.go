package handlers

import (
	"strings"
	"testing"

	"github.com/agentmesh/backend/internal/models"
)

// The validation branches are the whole point of parseSettingsPatch: they are
// what stops an out-of-range ceiling or an unknown key mode from reaching a
// table whose CHECK constraints would reject it as a 500 instead of a 400.
func TestParseSettingsPatchRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"malformed JSON", `{`, "invalid JSON body"},
		{"negative threshold", `{"lowBalanceUsdMicros":-1}`, "cannot be negative"},
		{"non-numeric threshold", `{"lowBalanceUsdMicros":"5"}`, "whole number of USD micros"},
		{"zero ceiling", `{"maxCallSpendUsdMicros":0}`, "greater than zero"},
		{"negative ceiling", `{"maxCallSpendUsdMicros":-500}`, "greater than zero"},
		{"ceiling above the platform cap", `{"maxCallSpendUsdMicros":1000000001}`, "cannot exceed the platform ceiling"},
		{"unknown key mode", `{"defaultKeyMode":"free"}`, "must be byok or platform"},
		{"non-string key mode", `{"defaultKeyMode":7}`, "must be a string"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, msg := parseSettingsPatch(strings.NewReader(tc.body))
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("want message containing %q, got %q", tc.want, msg)
			}
		})
	}
}

// The global cap itself must remain settable — the boundary is inclusive, so a
// user pinning their ceiling to exactly the platform maximum is legal.
func TestParseSettingsPatchAcceptsTheGlobalCapExactly(t *testing.T) {
	body := `{"maxCallSpendUsdMicros":1000000000}`
	patch, msg := parseSettingsPatch(strings.NewReader(body))
	if msg != "" {
		t.Fatalf("want no error, got %q", msg)
	}
	if patch.maxCallSpend == nil || *patch.maxCallSpend != models.MaxSingleX402QuoteUSDMicros {
		t.Fatalf("want ceiling %d, got %v", models.MaxSingleX402QuoteUSDMicros, patch.maxCallSpend)
	}
}

// An omitted key must leave the stored value alone. This is the regression that
// matters most: a settings page that PATCHes one field must not blank the rest.
func TestParseSettingsPatchLeavesOmittedFieldsUntouched(t *testing.T) {
	existing := int64(250_000)
	settings := models.UserSettings{
		LowBalanceUSDMicros:   9_000_000,
		MaxCallSpendUSDMicros: &existing,
		DefaultKeyMode:        models.KeyModePlatform,
	}

	patch, msg := parseSettingsPatch(strings.NewReader(`{"lowBalanceUsdMicros":1000000}`))
	if msg != "" {
		t.Fatalf("want no error, got %q", msg)
	}
	patch.applyTo(&settings)

	if settings.LowBalanceUSDMicros != 1_000_000 {
		t.Errorf("threshold: want 1000000, got %d", settings.LowBalanceUSDMicros)
	}
	if settings.MaxCallSpendUSDMicros == nil || *settings.MaxCallSpendUSDMicros != existing {
		t.Errorf("ceiling should survive an unrelated patch, got %v", settings.MaxCallSpendUSDMicros)
	}
	if settings.DefaultKeyMode != models.KeyModePlatform {
		t.Errorf("key mode should survive an unrelated patch, got %q", settings.DefaultKeyMode)
	}
}

// An explicit null is the only way to remove a ceiling, and it has to be
// distinguishable from omitting the field — otherwise every partial save would
// silently drop the user's spend limit.
func TestParseSettingsPatchClearsCeilingOnExplicitNull(t *testing.T) {
	existing := int64(250_000)
	settings := models.UserSettings{MaxCallSpendUSDMicros: &existing}

	patch, msg := parseSettingsPatch(strings.NewReader(`{"maxCallSpendUsdMicros":null}`))
	if msg != "" {
		t.Fatalf("want no error, got %q", msg)
	}
	patch.applyTo(&settings)

	if settings.MaxCallSpendUSDMicros != nil {
		t.Fatalf("want ceiling cleared, got %d", *settings.MaxCallSpendUSDMicros)
	}
}

// Defaults are what every account has before it opens the settings page, so
// they have to match the column DEFAULTs in migration 000020.
func TestDefaultUserSettingsMatchTheMigration(t *testing.T) {
	d := models.DefaultUserSettings()
	if d.LowBalanceUSDMicros != 5_000_000 {
		t.Errorf("low balance default: want 5000000, got %d", d.LowBalanceUSDMicros)
	}
	if d.DefaultKeyMode != models.KeyModeBYOK {
		t.Errorf("key mode default: want %q, got %q", models.KeyModeBYOK, d.DefaultKeyMode)
	}
	if d.MaxCallSpendUSDMicros != nil {
		t.Errorf("ceiling should default to unset, got %d", *d.MaxCallSpendUSDMicros)
	}
}
