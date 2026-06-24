package casts_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/casts"
)

func TestJSONCastRoundTrip(t *testing.T) {
	c := casts.JSONCast{}
	out, err := c.ToDB(map[string]any{"name": "Ada", "age": 36})
	require.NoError(t, err)

	var dst map[string]any
	require.NoError(t, c.FromDB(out, &dst))
	assert.Equal(t, "Ada", dst["name"])
}

func TestJSONCast_NilHandling(t *testing.T) {
	c := casts.JSONCast{}
	var dst map[string]any
	require.NoError(t, c.FromDB(nil, &dst))
	assert.Nil(t, dst)
}

func TestBoolCast_VariousSources(t *testing.T) {
	c := casts.BoolCast{}
	cases := []struct {
		src  any
		want bool
	}{
		{nil, false},
		{true, true},
		{int64(0), false},
		{int64(1), true},
		{"true", true},
		{"false", false},
		{"t", true},
		{[]byte("1"), true},
	}
	for _, tc := range cases {
		var b bool
		require.NoError(t, c.FromDB(tc.src, &b))
		assert.Equal(t, tc.want, b, "src=%#v", tc.src)
	}
}

func TestIntCastParsesStrings(t *testing.T) {
	c := casts.IntCast{}
	var out int64
	require.NoError(t, c.FromDB("42", &out))
	assert.Equal(t, int64(42), out)
	require.NoError(t, c.FromDB([]byte("-7"), &out))
	assert.Equal(t, int64(-7), out)
}

func TestDateCastRoundTrip(t *testing.T) {
	c := casts.DateCast{}
	day := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	out, err := c.ToDB(day)
	require.NoError(t, err)
	assert.Equal(t, "2026-05-15", out)

	var parsed time.Time
	require.NoError(t, c.FromDB("2026-05-15", &parsed))
	assert.Equal(t, day.Year(), parsed.Year())
	assert.Equal(t, day.Month(), parsed.Month())
	assert.Equal(t, day.Day(), parsed.Day())
}

func TestRegistryLookup(t *testing.T) {
	assert.NotNil(t, casts.Get("json"))
	assert.NotNil(t, casts.Get("boolean"))
	assert.NotNil(t, casts.Get("datetime"))
	assert.NotNil(t, casts.Get("float"))
	assert.NotNil(t, casts.Get("double"))
	assert.Nil(t, casts.Get("does-not-exist"))
}

func TestBoolCast_StringAndFloatSources(t *testing.T) {
	c := casts.BoolCast{}
	cases := []struct {
		src  any
		want bool
	}{
		{"yes", true},
		{"TRUE", true},
		{" On ", true},
		{"n", false},
		{"off", false},
		{float64(1), true},
		{float64(0), false},
		{int32(1), true},
		{uint64(0), false},
	}
	for _, tc := range cases {
		var b bool
		require.NoError(t, c.FromDB(tc.src, &b), "src=%#v", tc.src)
		assert.Equal(t, tc.want, b, "src=%#v", tc.src)
	}
}

func TestIntCast_DriverTypes(t *testing.T) {
	c := casts.IntCast{}
	cases := []struct {
		src  any
		want int64
	}{
		{int32(42), 42},
		{float64(7), 7}, // SQLite i+0.0
		{uint64(100), 100},
		{int64(-3), -3},
		{int(9), 9},
	}
	for _, tc := range cases {
		var out int64
		require.NoError(t, c.FromDB(tc.src, &out), "src=%#v", tc.src)
		assert.Equal(t, tc.want, out, "src=%#v", tc.src)
	}
	// Non-integral float must error.
	var out int64
	assert.Error(t, c.FromDB(float64(1.5), &out))
}

func TestFloatCast_FromDB(t *testing.T) {
	c := casts.FloatCast{}
	cases := []struct {
		src  any
		want float64
	}{
		{float64(1.5), 1.5},
		{float32(2.5), 2.5},
		{int64(3), 3},
		{"4.25", 4.25},
		{[]byte("5.5"), 5.5},
	}
	for _, tc := range cases {
		var out float64
		require.NoError(t, c.FromDB(tc.src, &out), "src=%#v", tc.src)
		assert.InDelta(t, tc.want, out, 1e-6, "src=%#v", tc.src)
	}
}

func TestDateCast_ToDBNullHandling(t *testing.T) {
	c := casts.DateCast{}

	out, err := c.ToDB(nil)
	require.NoError(t, err)
	assert.Nil(t, out)

	out, err = c.ToDB(time.Time{})
	require.NoError(t, err)
	assert.Nil(t, out, "zero time renders as NULL")

	var nilPtr *time.Time
	out, err = c.ToDB(nilPtr)
	require.NoError(t, err)
	assert.Nil(t, out)

	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	out, err = c.ToDB(&day)
	require.NoError(t, err)
	assert.Equal(t, "2026-01-02", out)
}

func TestDateTimeCast_RoundTrip(t *testing.T) {
	c := casts.DateTimeCast{}
	ts := time.Date(2026, 6, 24, 15, 4, 5, 0, time.UTC)
	out, err := c.ToDB(ts)
	require.NoError(t, err)
	assert.Equal(t, ts.Format(time.RFC3339), out)

	var parsed time.Time
	require.NoError(t, c.FromDB("2026-06-24 15:04:05", &parsed))
	assert.Equal(t, 2026, parsed.Year())
	assert.Equal(t, 4, parsed.Minute())

	out, err = c.ToDB(nil)
	require.NoError(t, err)
	assert.Nil(t, out)
}
