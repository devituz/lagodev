package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/config"
)

// ---------------------------------------------------------------------------
// String getter
// ---------------------------------------------------------------------------

// String treats an empty value identically to an unset key: both yield the
// fallback. This is documented framework behaviour — there is no empty-vs-unset
// distinction for String.
func TestString_EmptyEqualsUnset(t *testing.T) {
	// Unset.
	os.Unsetenv("LAGO_EDGE_STR")
	assert.Equal(t, "fb", config.String("LAGO_EDGE_STR", "fb"))

	// Explicitly empty -> same as unset.
	t.Setenv("LAGO_EDGE_STR", "")
	assert.Equal(t, "fb", config.String("LAGO_EDGE_STR", "fb"))

	// Whitespace is NOT trimmed by String: a space is a real value.
	t.Setenv("LAGO_EDGE_STR", " ")
	assert.Equal(t, " ", config.String("LAGO_EDGE_STR", "fb"))
}

func TestString_PreservesValueShape(t *testing.T) {
	cases := map[string]string{
		"with spaces":                          "  padded value  ",
		"with equals":                          "key=val=more",
		"with quotes":                          `"quoted"`,
		"unicode":                              "héllo-世界-🚀",
		"hash not a comment when set directly": "value # not a comment",
	}
	for name, val := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("LAGO_EDGE_STR", val)
			assert.Equal(t, val, config.String("LAGO_EDGE_STR", "fb"))
		})
	}
}

// ---------------------------------------------------------------------------
// Int getter
// ---------------------------------------------------------------------------

func TestInt_MalformedFallsBack(t *testing.T) {
	cases := []string{
		"bogus", "12.5", "1_000", "0x10", " 5 ", "5e3",
		"99999999999999999999999999", // overflows int64
		"-", "+", "",
	}
	for _, v := range cases {
		t.Run("val="+v, func(t *testing.T) {
			t.Setenv("LAGO_EDGE_INT", v)
			assert.NotPanics(t, func() {
				got := config.Int("LAGO_EDGE_INT", 7)
				// Malformed input must yield the fallback, never a partial parse.
				assert.Equal(t, 7, got)
			})
		})
	}
}

func TestInt_ValidVariants(t *testing.T) {
	cases := map[string]int{
		"0":     0,
		"-42":   -42,
		"+13":   13,
		"00123": 123,
	}
	for v, want := range cases {
		t.Setenv("LAGO_EDGE_INT", v)
		assert.Equal(t, want, config.Int("LAGO_EDGE_INT", -1), "v=%s", v)
	}
}

func TestInt_UnsetFallsBack(t *testing.T) {
	os.Unsetenv("LAGO_EDGE_INT")
	assert.Equal(t, 99, config.Int("LAGO_EDGE_INT", 99))
}

// ---------------------------------------------------------------------------
// Bool getter
// ---------------------------------------------------------------------------

func TestBool_WhitespaceAndCase(t *testing.T) {
	truthy := []string{"TRUE", "  true  ", "YeS", "On", "T", "Y", "1"}
	for _, v := range truthy {
		t.Setenv("LAGO_EDGE_BOOL", v)
		assert.True(t, config.Bool("LAGO_EDGE_BOOL", false), "v=%q", v)
	}
	falsy := []string{"FALSE", "  off ", "No", "F", "N", "0"}
	for _, v := range falsy {
		t.Setenv("LAGO_EDGE_BOOL", v)
		assert.False(t, config.Bool("LAGO_EDGE_BOOL", true), "v=%q", v)
	}
}

func TestBool_UnknownFallsBack(t *testing.T) {
	for _, v := range []string{"", "maybe", "2", "yesyes", "tru", "enabled"} {
		t.Setenv("LAGO_EDGE_BOOL", v)
		// Fallback is returned regardless of its value -> no panic, deterministic.
		assert.True(t, config.Bool("LAGO_EDGE_BOOL", true), "v=%q", v)
		assert.False(t, config.Bool("LAGO_EDGE_BOOL", false), "v=%q", v)
	}
	os.Unsetenv("LAGO_EDGE_BOOL")
	assert.True(t, config.Bool("LAGO_EDGE_BOOL", true))
}

// ---------------------------------------------------------------------------
// Duration getter
// ---------------------------------------------------------------------------

func TestDuration_MalformedFallsBack(t *testing.T) {
	cases := []string{"bogus", "10", "10sec", "1.5.5s", "", "  ", "-", "1h2x"}
	for _, v := range cases {
		t.Run("val="+v, func(t *testing.T) {
			t.Setenv("LAGO_EDGE_DUR", v)
			assert.NotPanics(t, func() {
				assert.Equal(t, 3*time.Second, config.Duration("LAGO_EDGE_DUR", 3*time.Second))
			})
		})
	}
}

func TestDuration_ValidVariants(t *testing.T) {
	cases := map[string]time.Duration{
		"0":       0,
		"500ms":   500 * time.Millisecond,
		"1h30m":   90 * time.Minute,
		"-2s":     -2 * time.Second,
		"1us":     time.Microsecond,
		"2h45m0s": 2*time.Hour + 45*time.Minute,
	}
	for v, want := range cases {
		t.Setenv("LAGO_EDGE_DUR", v)
		assert.Equal(t, want, config.Duration("LAGO_EDGE_DUR", -1), "v=%s", v)
	}
}

// ---------------------------------------------------------------------------
// LoadEnv: .env file edge cases
// ---------------------------------------------------------------------------

func TestLoadEnv_MissingFileNoError(t *testing.T) {
	err := config.LoadEnv(filepath.Join(t.TempDir(), "nope.env"))
	assert.NoError(t, err)
}

func TestLoadEnv_DefaultMissingNoError(t *testing.T) {
	// With no args and no .env in the (temp) cwd, LoadEnv must be a no-op.
	dir := t.TempDir()
	prev, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })

	assert.NoError(t, config.LoadEnv())
}

func TestLoadEnv_LoadsValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.env")
	content := strings.Join([]string{
		"# a comment",
		"",
		"LAGO_EDGE_LOADED=hello",
		`LAGO_EDGE_QUOTED="quoted value"`,
		"LAGO_EDGE_EQUALS=a=b=c",
		"   LAGO_EDGE_SPACED   =   trimmed   ", // godotenv trims surrounding space
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	require.NoError(t, config.LoadEnv(path))
	t.Cleanup(func() {
		for _, k := range []string{"LAGO_EDGE_LOADED", "LAGO_EDGE_QUOTED", "LAGO_EDGE_EQUALS", "LAGO_EDGE_SPACED"} {
			os.Unsetenv(k)
		}
	})

	assert.Equal(t, "hello", os.Getenv("LAGO_EDGE_LOADED"))
	assert.Equal(t, "quoted value", os.Getenv("LAGO_EDGE_QUOTED"))
	assert.Equal(t, "a=b=c", os.Getenv("LAGO_EDGE_EQUALS"))
	assert.Equal(t, "trimmed", os.Getenv("LAGO_EDGE_SPACED"))
}

func TestLoadEnv_MalformedFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.env")
	// A line with no '=' and not a comment is malformed for godotenv.
	require.NoError(t, os.WriteFile(path, []byte("this is not valid env syntax at all\n"), 0o600))

	err := config.LoadEnv(path)
	// Malformed content must surface as a clean error, never a panic.
	assert.Error(t, err)
}

func TestLoadEnv_VeryLongValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "long.env")
	long := strings.Repeat("A", 1<<16) // 64 KiB
	require.NoError(t, os.WriteFile(path, []byte("LAGO_EDGE_LONG="+long+"\n"), 0o600))

	require.NoError(t, config.LoadEnv(path))
	t.Cleanup(func() { os.Unsetenv("LAGO_EDGE_LONG") })
	assert.Equal(t, long, os.Getenv("LAGO_EDGE_LONG"))
}

func TestLoadEnv_UTF8Value(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "utf8.env")
	require.NoError(t, os.WriteFile(path, []byte("LAGO_EDGE_UTF8=héllo-世界-🚀\n"), 0o600))

	require.NoError(t, config.LoadEnv(path))
	t.Cleanup(func() { os.Unsetenv("LAGO_EDGE_UTF8") })
	assert.Equal(t, "héllo-世界-🚀", os.Getenv("LAGO_EDGE_UTF8"))
}

// Documented behaviour: godotenv does NOT strip a leading UTF-8 BOM. The BOM
// bytes leak into the first variable name, which godotenv rejects as an
// "unexpected character", and it aborts the ENTIRE file (it does not skip the
// bad line). Therefore LoadEnv returns a clean error and sets nothing — not
// even the well-formed lines after the BOM-tainted first one. The important
// guarantees we assert: (1) no panic, (2) a clean error is surfaced, (3) the
// process env is left untouched.
func TestLoadEnv_BOMReturnsErrorAndSetsNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bom.env")
	bom := []byte{0xEF, 0xBB, 0xBF}
	content := append(bom, []byte("LAGO_EDGE_FIRST=one\nLAGO_EDGE_SECOND=two\n")...)
	require.NoError(t, os.WriteFile(path, content, 0o600))

	t.Cleanup(func() {
		os.Unsetenv("LAGO_EDGE_FIRST")
		os.Unsetenv("LAGO_EDGE_SECOND")
	})

	var err error
	assert.NotPanics(t, func() {
		err = config.LoadEnv(path)
	})
	assert.Error(t, err, "BOM-prefixed file must surface a clean parse error")
	// Whole-file abort: nothing is loaded.
	assert.Empty(t, os.Getenv("LAGO_EDGE_FIRST"))
	assert.Empty(t, os.Getenv("LAGO_EDGE_SECOND"))
}

func TestLoadEnv_DoesNotOverrideExistingProcessEnv(t *testing.T) {
	// godotenv.Load (non-overload) must NOT clobber an already-set var.
	t.Setenv("LAGO_EDGE_PRESET", "from-process")
	dir := t.TempDir()
	path := filepath.Join(dir, "preset.env")
	require.NoError(t, os.WriteFile(path, []byte("LAGO_EDGE_PRESET=from-file\n"), 0o600))

	require.NoError(t, config.LoadEnv(path))
	assert.Equal(t, "from-process", os.Getenv("LAGO_EDGE_PRESET"),
		"existing process env must win over .env file (godotenv.Load semantics)")
}

func TestLoadEnv_DoesNotMutateCallerSliceWhenSomeExist(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.env")
	require.NoError(t, os.WriteFile(real, []byte("LAGO_EDGE_MX=1\n"), 0o600))
	t.Cleanup(func() { os.Unsetenv("LAGO_EDGE_MX") })

	files := []string{filepath.Join(dir, "missing.env"), real}
	orig := append([]string(nil), files...)
	require.NoError(t, config.LoadEnv(files...))
	assert.Equal(t, orig, files, "LoadEnv must not mutate the caller's slice")
}

// ---------------------------------------------------------------------------
// Precedence: process env vs .env file
// ---------------------------------------------------------------------------

// Documented precedence: process environment (already set) wins over .env file
// contents, because LoadEnv uses godotenv.Load (not Overload). And the getters
// read directly from os.Getenv, so whatever ends up in the process env is what
// FromEnv sees.
func TestPrecedence_ProcessEnvWinsOverFile(t *testing.T) {
	t.Setenv("DB_HOST", "process-host")
	dir := t.TempDir()
	path := filepath.Join(dir, "db.env")
	require.NoError(t, os.WriteFile(path, []byte("DB_HOST=file-host\nDB_PORT=6000\n"), 0o600))
	t.Cleanup(func() { os.Unsetenv("DB_PORT") })

	require.NoError(t, config.LoadEnv(path))

	cfg := config.FromEnv()
	assert.Equal(t, "process-host", cfg.Host, "process env must win")
	assert.Equal(t, 6000, cfg.Port, "file value used when process env is unset")
}

// ---------------------------------------------------------------------------
// FromEnv: hostile / malformed values must never panic
// ---------------------------------------------------------------------------

func TestFromEnv_MalformedNumericFallsBackNoPanic(t *testing.T) {
	t.Setenv("DB_PORT", "not-a-port")
	t.Setenv("DB_MAX_OPEN", "∞")
	t.Setenv("DB_MAX_IDLE", "-")
	t.Setenv("DB_CONN_MAX_LIFETIME", "forever")
	t.Setenv("DB_SLOW_QUERY", "10") // missing unit -> invalid duration
	t.Setenv("DB_LOG_QUERIES", "perhaps")

	assert.NotPanics(t, func() {
		cfg := config.FromEnv()
		assert.Equal(t, 0, cfg.Port)
		assert.Equal(t, 0, cfg.MaxOpenConns)
		assert.Equal(t, 0, cfg.MaxIdleConns)
		assert.Equal(t, time.Duration(0), cfg.ConnMaxLifetime)
		assert.Equal(t, time.Duration(0), cfg.SlowQuery)
		assert.False(t, cfg.LogQueries) // unknown bool -> fallback false
	})
}

func TestFromEnv_DefaultsWhenAllUnset(t *testing.T) {
	for _, k := range []string{
		"DB_CONNECTION", "DB_DSN", "DB_HOST", "DB_PORT", "DB_USERNAME",
		"DB_PASSWORD", "DB_DATABASE", "DB_SCHEMA", "DB_SSL_MODE", "DB_TIMEZONE",
		"DB_MAX_OPEN", "DB_MAX_IDLE", "DB_CONN_MAX_LIFETIME", "DB_CONN_MAX_IDLE_TIME",
		"DB_LOG_QUERIES", "DB_SLOW_QUERY",
	} {
		os.Unsetenv(k)
	}
	cfg := config.FromEnv()
	assert.Equal(t, "sqlite", cfg.Driver) // documented default driver
	assert.Equal(t, 0, cfg.Port)
	assert.False(t, cfg.LogQueries)
}

// ---------------------------------------------------------------------------
// Concurrency: getters are read-only over os.Getenv and must be race-free.
// Run the whole package with -race to exercise this.
// ---------------------------------------------------------------------------

func TestGetters_ConcurrentReadsAreRaceFree(t *testing.T) {
	t.Setenv("LAGO_CONC_STR", "v")
	t.Setenv("LAGO_CONC_INT", "123")
	t.Setenv("LAGO_CONC_BOOL", "true")
	t.Setenv("LAGO_CONC_DUR", "250ms")

	const goroutines = 32
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_ = config.String("LAGO_CONC_STR", "fb")
				_ = config.Int("LAGO_CONC_INT", 0)
				_ = config.Bool("LAGO_CONC_BOOL", false)
				_ = config.Duration("LAGO_CONC_DUR", 0)
				_ = config.FromEnv()
			}
		}()
	}
	wg.Wait()
}
