package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/devituz/lagodev/config"
)

func TestString_FallbackWhenUnset(t *testing.T) {
	t.Setenv("LAGO_TEST_STR", "")
	assert.Equal(t, "fallback", config.String("LAGO_TEST_STR", "fallback"))

	t.Setenv("LAGO_TEST_STR", "hello")
	assert.Equal(t, "hello", config.String("LAGO_TEST_STR", "fallback"))
}

func TestInt_ParsesValidNumbers(t *testing.T) {
	t.Setenv("LAGO_TEST_INT", "42")
	assert.Equal(t, 42, config.Int("LAGO_TEST_INT", 0))

	t.Setenv("LAGO_TEST_INT", "bogus")
	assert.Equal(t, 7, config.Int("LAGO_TEST_INT", 7))
}

func TestBool_ParsesCommonTruthy(t *testing.T) {
	cases := map[string]bool{
		"1": true, "true": true, "yes": true, "on": true,
		"0": false, "false": false, "no": false, "off": false,
	}
	for v, expected := range cases {
		t.Setenv("LAGO_TEST_BOOL", v)
		assert.Equal(t, expected, config.Bool("LAGO_TEST_BOOL", !expected), "v=%s", v)
	}
}

func TestDuration_ParsesGoFormat(t *testing.T) {
	t.Setenv("LAGO_TEST_DUR", "750ms")
	assert.Equal(t, 750*time.Millisecond, config.Duration("LAGO_TEST_DUR", 0))
}

func TestFromEnv_BuildsDatabaseConfig(t *testing.T) {
	t.Setenv("DB_CONNECTION", "postgres")
	t.Setenv("DB_HOST", "db.example.com")
	t.Setenv("DB_PORT", "5433")
	t.Setenv("DB_USERNAME", "admin")
	t.Setenv("DB_DATABASE", "app")
	t.Setenv("DB_LOG_QUERIES", "true")

	cfg := config.FromEnv()
	assert.Equal(t, "postgres", cfg.Driver)
	assert.Equal(t, "db.example.com", cfg.Host)
	assert.Equal(t, 5433, cfg.Port)
	assert.Equal(t, "admin", cfg.Username)
	assert.Equal(t, "app", cfg.Database)
	assert.True(t, cfg.LogQueries)
}
