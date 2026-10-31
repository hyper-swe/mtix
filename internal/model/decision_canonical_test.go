// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestCanonicalJSON_RFC8785Examples_MatchPublishedOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "section 3.2.2 sample",
			in: `{"numbers":[333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001],` +
				`"string":"\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/","literals":[null, true, false]}`,
			want: `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],` +
				"\"string\":\"€$\\u000f\\nA'B\\\"\\\\\\\\\\\"/\"}",
		},
		{
			name: "section 3.2.3 key order by UTF-16 code units",
			in: `{"\u20ac":"Euro Sign","\r":"Carriage Return","\ufb33":"Hebrew Letter Dalet With Dagesh",` +
				`"1":"One","\ud83d\ude00":"Emoji: Grinning Face","\u0080":"Control","\u00f6":"Latin Small Letter O With Diaeresis"}`,
			want: "{\"\\r\":\"Carriage Return\",\"1\":\"One\",\"\u0080\":\"Control\",\"ö\":\"Latin Small Letter O With Diaeresis\"," +
				"\"€\":\"Euro Sign\",\"😀\":\"Emoji: Grinning Face\",\"\ufb33\":\"Hebrew Letter Dalet With Dagesh\"}",
		},
		{name: "whitespace removed", in: " { \"b\" : [ 1 , 2 ] , \"a\" : { } } ", want: `{"a":{},"b":[1,2]}`},
		{name: "negative zero", in: `[-0, 0.0, -0.0e5]`, want: `[0,0,0]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := model.CanonicalJSON([]byte(tt.in))
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestCanonicalJSON_NumberTable_MatchesRFC8785AppendixB(t *testing.T) {
	tests := []struct {
		bits uint64
		want string
	}{
		{0x0000000000000000, "0"},
		{0x8000000000000000, "0"},
		{0x0000000000000001, "5e-324"},
		{0x8000000000000001, "-5e-324"},
		{0x7fefffffffffffff, "1.7976931348623157e+308"},
		{0xffefffffffffffff, "-1.7976931348623157e+308"},
		{0x4340000000000000, "9007199254740992"},
		{0xc340000000000000, "-9007199254740992"},
		{0x4430000000000000, "295147905179352830000"},
		{0x44b52d02c7e14af5, "9.999999999999997e+22"},
		{0x44b52d02c7e14af6, "1e+23"},
		{0x44b52d02c7e14af7, "1.0000000000000001e+23"},
		{0x444b1ae4d6e2ef4e, "999999999999999700000"},
		{0x444b1ae4d6e2ef4f, "999999999999999900000"},
		{0x444b1ae4d6e2ef50, "1e+21"},
		{0x3eb0c6f7a0b5ed8c, "9.999999999999997e-7"},
		{0x3eb0c6f7a0b5ed8d, "0.000001"},
		{0x41b3de4355555553, "333333333.3333332"},
		{0x41b3de4355555554, "333333333.33333325"},
		{0x41b3de4355555555, "333333333.3333333"},
		{0x41b3de4355555556, "333333333.3333334"},
		{0x41b3de4355555557, "333333333.33333343"},
		{0xbecbf647612f3696, "-0.0000033333333333333333"},
		{0x43143ff3c1cb0959, "1424953923781206.2"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			f := math.Float64frombits(tt.bits)
			// Feed the number as the shortest round-trip text, as any producer would.
			in := strconv.FormatFloat(f, 'g', -1, 64)
			got, err := model.CanonicalJSON([]byte(in))
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestCanonicalJSON_HostileInput_ReturnsInvalidInput(t *testing.T) {
	deep := strings.Repeat("[", 10) + strings.Repeat("]", 10)
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ``},
		{"trailing data", `{} 1`},
		{"number overflow", `[1e999]`},
		{"negative overflow", `[-1e999]`},
		{"leading zero", `[01]`},
		{"plus sign", `[+1]`},
		{"bare dot", `[1.]`},
		{"nan literal", `[NaN]`},
		{"lone high surrogate", `["\ud800"]`},
		{"lone low surrogate", `["\udc00"]`},
		{"high surrogate then letter", `["\ud800a"]`},
		{"invalid utf-8", "[\"\xff\"]"},
		{"raw control character", "[\"\x01\"]"},
		{"bad escape", `["\q"]`},
		{"short unicode escape", `["\u12"]`},
		{"unterminated string", `["abc`},
		{"unterminated object", `{"a":1`},
		{"missing colon", `{"a" 1}`},
		{"unquoted name", `{a:1}`},
		{"trailing comma", `[1,]`},
		{"too deep", deep},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := model.CanonicalJSON([]byte(tt.in))
			require.Error(t, err)
			assert.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

func TestCanonicalJSON_DepthAtLimit_Accepted(t *testing.T) {
	ok := strings.Repeat("[", model.MaxDecisionCanonicalDepth) + strings.Repeat("]", model.MaxDecisionCanonicalDepth)
	_, err := model.CanonicalJSON([]byte(ok))
	require.NoError(t, err)
	over := "[" + ok + "]"
	_, err = model.CanonicalJSON([]byte(over))
	require.Error(t, err)
}

func TestCanonicalJSON_DuplicateNames_LastWinsAtEveryLevel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		dup  bool
	}{
		{"top level", `{"a":1,"a":2}`, `{"a":2}`, true},
		{"nested", `{"o":{"x":1,"x":{"y":1,"y":2}}}`, `{"o":{"x":{"y":2}}}`, true},
		{"inside array", `[{"k":1,"k":2},{"k":3}]`, `[{"k":2},{"k":3}]`, true},
		{"escaped spelling is the same name", `{"a":1,"\u0061":2}`, `{"a":2}`, true},
		{"no duplicates", `{"a":1,"b":2}`, `{"a":1,"b":2}`, false},
		{"same name in sibling objects", `{"p":{"a":1},"q":{"a":2}}`, `{"p":{"a":1},"q":{"a":2}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := model.CanonicalJSON([]byte(tt.in))
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
			dup, err := model.JSONHasDuplicateNames([]byte(tt.in))
			require.NoError(t, err)
			assert.Equal(t, tt.dup, dup)
		})
	}
}

func TestCanonicalJSON_MemberOrder_DoesNotChangeOutput(t *testing.T) {
	const seed = 20261031
	rng := rand.New(rand.NewPCG(seed, seed))
	names := []string{"alpha", "beta", "gamma", "delta", "eps", "zeta", "eta", "theta"}
	var want []byte
	for i := range 50 {
		perm := rng.Perm(len(names))
		var parts []string
		for _, p := range perm {
			parts = append(parts, strconv.Quote(names[p])+`:{"z":1,"a":[true,null]}`)
		}
		got, err := model.CanonicalJSON([]byte("{" + strings.Join(parts, ",") + "}"))
		require.NoError(t, err)
		if want == nil {
			want = got
		}
		assert.Equal(t, string(want), string(got), "seed %d permutation %d", seed, i)
	}
}

func TestCanonicalJSON_StringEscapes_RoundTripToCanonicalForm(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"short escapes", `["\b\f\n\r\t\"\\\/"]`, `["\b\f\n\r\t\"\\/"]`},
		{"control escape lower hex", `["\u001F\u0000"]`, `["\u001f\u0000"]`},
		{"surrogate pair", `["😀"]`, "[\"😀\"]"},
		{"del and line separator stay literal", "[\"\\u007f\\u2028\"]", "[\"\u007f \"]"},
		{"literals", `[true,false,null]`, `[true,false,null]`},
		{"exponent forms", `[1E2,1e+2,1e-2,-1.5E3]`, `[100,100,0.01,-1500]`},
		{"large integer", `[1e21,1e20,123456789012345678901]`, `[1e+21,100000000000000000000,123456789012345680000]`},
		{"small fraction", `[0.00001,0.000001,0.0000001]`, `[0.00001,0.000001,1e-7]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := model.CanonicalJSON([]byte(tt.in))
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestCanonicalJSON_MoreHostileInput_ReturnsInvalidInput(t *testing.T) {
	for _, in := range []string{`-`, `-a`, `1e`, `1e+`, `tru`, `nul`, `{"a"`, `{"a":}`, `[1 2]`, `["\u+123"]`, `["\ud800A"]`, `["\`, `{"a":1,}`, `{1:2}`} {
		_, err := model.CanonicalJSON([]byte(in))
		assert.ErrorIs(t, err, model.ErrInvalidInput, in)
	}
}

func TestIngestDecision_LongUnknownValues_AreClippedInReasons(t *testing.T) {
	res := ingest(t, withField(baseRequest(), "answerable_by", rep("x", 500)))
	assert.Equal(t, model.DecisionUnsupported, res.Class)
	assert.Less(t, len(res.Reason), 120)
}
