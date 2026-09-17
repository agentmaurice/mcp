package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestConfigurationKeyFileAndInvalidValues(t *testing.T) {
	file := filepath.Join(t.TempDir(), "key")
	if e := os.WriteFile(file, []byte("test-secret\n"), 0600); e != nil {
		t.Fatal(e)
	}
	v := viper.New()
	v.Set("timeout", "5s")
	v.Set("max_state_bytes", 32768)
	v.Set("typesafe_api_key_file", file)
	c, e := configuration(v)
	if e != nil || c.APIKey != "test-secret" {
		t.Fatal("file credential not loaded")
	}
	v.Set("typesafe_api_key", "other")
	if _, e = configuration(v); e == nil {
		t.Fatal("ambiguous credential accepted")
	}
	v.Set("typesafe_api_key", "")
	v.Set("typesafe_api_key_file", file+"-absent")
	if _, e = configuration(v); e == nil {
		t.Fatal("missing file accepted")
	}
	v.Set("typesafe_api_key_file", file)
	v.Set("timeout", "invalid")
	if _, e = configuration(v); e == nil {
		t.Fatal("invalid timeout accepted")
	}
	v.Set("timeout", "5s")
	v.Set("max_state_bytes", "invalid")
	if _, e = configuration(v); e == nil {
		t.Fatal("invalid byte limit accepted")
	}
}

func TestConfigurationRejectsNonPositiveLimits(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"timeout", "0s"}, {"timeout", "-1s"},
		{"max_state_bytes", "0"}, {"max_state_bytes", "-1"}, {"max_state_bytes", "32769"},
	} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			v := viper.New()
			v.Set("timeout", "5s")
			v.Set("max_state_bytes", 32768)
			v.Set(tc.name, tc.value)
			if _, err := configuration(v); err == nil {
				t.Fatal("invalid configured limit accepted")
			}
		})
	}
}
