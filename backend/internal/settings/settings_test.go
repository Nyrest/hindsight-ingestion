package settings

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/crypto"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestTimezoneDefaultsAndPersistence(t *testing.T) {
	db, err := database.Open("sqlite", filepath.Join(t.TempDir(), "settings.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); sqlDB.Close() })
	cipher, err := crypto.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, cipher)
	for _, tc := range []struct{ env, want string }{{"", "UTC"}, {"Asia/Tokyo", "Asia/Tokyo"}} {
		t.Setenv("TZ", tc.env)
		v, err := store.Get(t.Context())
		if err != nil || v.Timezone != tc.want {
			t.Fatalf("TZ=%q: timezone=%q, err=%v", tc.env, v.Timezone, err)
		}
	}
	// Existing settings without a timezone inherit TZ when upgraded.
	if err := db.Create(&models.Setting{Key: key, Value: `{"maxFileSizeMB":20}`}).Error; err != nil {
		t.Fatal(err)
	}
	v, err := store.Get(t.Context())
	if err != nil || v.Timezone != "Asia/Tokyo" || v.MaxFileSizeMB != 20 {
		t.Fatal(v, err)
	}
	v.Timezone = "Europe/Berlin"
	if err := store.Put(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TZ", "America/New_York")
	v, err = store.Get(t.Context())
	if err != nil || v.Timezone != "Europe/Berlin" {
		t.Fatal(v, err)
	}
}

func TestTimezoneValidation(t *testing.T) {
	for _, timezone := range []string{"", "Mars/Unknown"} {
		v := Defaults
		v.Timezone = timezone
		if !errors.Is(v.Validate(), ErrInvalidTimezone) {
			t.Fatalf("accepted %q", timezone)
		}
	}
	for _, timezone := range []string{"UTC", "Asia/Shanghai", "America/New_York"} {
		v := Defaults
		v.Timezone = timezone
		if err := v.Validate(); err != nil {
			t.Fatal(timezone, err)
		}
	}
}
