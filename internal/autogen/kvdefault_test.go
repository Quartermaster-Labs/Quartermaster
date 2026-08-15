package autogen

import "testing"

// A plain-attention model with room to spare keeps f16; the same model on a card
// that can't buy denseMinCtx at f16 steps down to q8_0.
func TestDefaultKvQuant_StepsDownOnlyUnderPressure(t *testing.T) {
	// 32 layers x 8 kv heads x 128 k/v dims => ~0.5 MB/token at f16.
	meta := Metadata{
		BlockCount: 32, HeadCountKv: 8, KeyLength: 128, ValueLength: 128,
		ContextLength: 131072, FileSizeGB: 8,
	}
	s := Settings{DenseMinCtx: 32768}

	f16 := GetKvCostModel(meta, "f16", "f16")
	roomy := 8 + 1 + f16.SlopeGB*32768 + 0.5 // weights + overhead + 32k of f16 KV
	if got := defaultKvQuant(s, meta, roomy, 1.0); got != "f16" {
		t.Errorf("roomy budget: got %q, want f16", got)
	}
	// Half the KV budget: f16 can't reach 32k, q8_0 (~0.53x) can.
	tight := 8 + 1 + f16.SlopeGB*16384
	if got := defaultKvQuant(s, meta, tight, 1.0); got != "q8_0" {
		t.Errorf("tight budget: got %q, want q8_0", got)
	}
}

// settings.kvQuant pins the type outright; an unknown value falls back to auto.
func TestDefaultKvQuant_SettingPin(t *testing.T) {
	meta := Metadata{BlockCount: 32, HeadCountKv: 8, KeyLength: 128, ValueLength: 128, FileSizeGB: 8}
	if got := defaultKvQuant(Settings{KvQuant: "q5_1", DenseMinCtx: 32768}, meta, 100, 1.0); got != "q5_1" {
		t.Errorf("pinned: got %q, want q5_1", got)
	}
	// Never emit a type llama would reject, even if a config asks for it.
	if got := defaultKvQuant(Settings{KvQuant: "iq4_nl", DenseMinCtx: 32768}, meta, 100, 1.0); got != "f16" {
		t.Errorf("iq4_nl pin: got %q, want the auto pick f16", got)
	}
}

// A one-sided pin mirrors rather than collapsing to the default. Dropping it
// used to size the fleet default f16 for a model pinned to q8_0, which on a
// tight budget costs a whole layer of offload in the editor's estimate.
func TestResolveKvPair(t *testing.T) {
	cases := []struct {
		name         string
		k, v         string
		wantK, wantV string
	}{
		{"both blank => default", "", "", "f16", "f16"},
		{"both set", "q8_0", "q8_0", "q8_0", "q8_0"},
		{"K only mirrors to V", "q8_0", "", "q8_0", "q8_0"},
		{"V only mirrors to K", "", "q8_0", "q8_0", "q8_0"},
		{"genuine mismatch => default", "q8_0", "q5_0", "f16", "f16"},
		{"unsupported type => default", "iq4_nl", "", "f16", "f16"},
	}
	for _, c := range cases {
		gotK, gotV := resolveKvPair(c.k, c.v, "f16", "f16")
		if gotK != c.wantK || gotV != c.wantV {
			t.Errorf("%s: resolveKvPair(%q,%q) = %q,%q; want %q,%q",
				c.name, c.k, c.v, gotK, gotV, c.wantK, c.wantV)
		}
	}
}

func TestValidKvPair(t *testing.T) {
	for _, q := range []string{"f32", "f16", "bf16", "q8_0", "q5_1", "q5_0", "q4_1", "q4_0"} {
		if !ValidKvPair(q, q) {
			t.Errorf("%s/%s should be valid", q, q)
		}
	}
	// Mismatched K/V breaks flash-attention; iq4_nl has no FA kernel at all.
	if ValidKvPair("q8_0", "f16") {
		t.Error("mismatched K/V should be rejected")
	}
	if ValidKvPair("iq4_nl", "iq4_nl") {
		t.Error("iq4_nl should be rejected")
	}
	if ValidKvPair("q3_k", "q3_k") {
		t.Error("unknown quant should be rejected")
	}
}
