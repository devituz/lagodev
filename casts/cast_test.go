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
	assert.Nil(t, casts.Get("does-not-exist"))
}
