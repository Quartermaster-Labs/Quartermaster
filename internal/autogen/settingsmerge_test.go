package autogen

import (
	"reflect"
	"testing"
)

// TestSettingsPatch_AllFieldsArePointers is the guard behind MergeSettingsPatch.
// A value-typed field would look "set" at its zero value, so the merge would
// happily overwrite a stored setting with 0/""/false on every save from a
// section that doesn't model it. Adding such a field must fail here, loudly,
// rather than in a user's config a week later.
func TestSettingsPatch_AllFieldsArePointers(t *testing.T) {
	rt := reflect.TypeOf(SettingsPatch{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.Ptr {
			t.Errorf("SettingsPatch.%s is %s, must be a pointer (see MergeSettingsPatch)", f.Name, f.Type)
		}
	}
}

// TestMergeSettingsPatch_CarriesForwardUntouchedFields is the regression the
// merge exists for: the dashboard saves in sections, so a patch carrying only
// the memory form's fields must leave the guard/advanced fields alone.
func TestMergeSettingsPatch_CarriesForwardUntouchedFields(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }
	b := func(v bool) *bool { return &v }

	prev := SettingsPatch{
		TargetVramGB:     f(20),
		OomGuardEvict:    b(false),
		OomGuardGraceSec: i(90),
		ComputeBufFactor: f(1.3),
		DryDefault:       b(true),
		TtlSec:           i(300),
	}
	// What the memory form PUTs: its own four fields, nothing else.
	next := SettingsPatch{
		TargetVramGB:   f(22),
		VramOverheadGB: f(1.5),
		MaxRamGB:       f(32),
		AutoVram:       b(false),
	}

	got := MergeSettingsPatch(prev, next)

	if got.TargetVramGB == nil || *got.TargetVramGB != 22 {
		t.Errorf("TargetVramGB: the new value must win, got %v", got.TargetVramGB)
	}
	if got.OomGuardEvict == nil || *got.OomGuardEvict != false {
		t.Errorf("OomGuardEvict was wiped by a memory-form save: %v", got.OomGuardEvict)
	}
	if got.OomGuardGraceSec == nil || *got.OomGuardGraceSec != 90 {
		t.Errorf("OomGuardGraceSec was wiped by a memory-form save: %v", got.OomGuardGraceSec)
	}
	if got.ComputeBufFactor == nil || *got.ComputeBufFactor != 1.3 {
		t.Errorf("ComputeBufFactor was wiped by a memory-form save: %v", got.ComputeBufFactor)
	}
	// The two fields that used to need bespoke carry-forward lines.
	if got.DryDefault == nil || *got.DryDefault != true {
		t.Errorf("DryDefault was wiped: %v", got.DryDefault)
	}
	if got.TtlSec == nil || *got.TtlSec != 300 {
		t.Errorf("TtlSec was wiped: %v", got.TtlSec)
	}
}

// TestMergeSettingsPatch_ClearedFieldStaysCleared: nil in prev and nil in next
// must stay nil, i.e. the merge never invents a value. That is what makes a
// per-section "restore defaults" (which nils the section's fields) work.
func TestMergeSettingsPatch_ClearedFieldStaysCleared(t *testing.T) {
	got := MergeSettingsPatch(SettingsPatch{}, SettingsPatch{})
	rv := reflect.ValueOf(got)
	for i := 0; i < rv.NumField(); i++ {
		if !rv.Field(i).IsNil() {
			t.Errorf("field %s became non-nil from two empty patches", rv.Type().Field(i).Name)
		}
	}
}

// TestSettingsPatch_ApplyCoversEveryField keeps (*SettingsPatch).apply in step
// with the struct: every field must actually reach Settings. apply is written
// explicitly (Settings is flat values, not pointers), so a new patch field can
// be added and silently never applied — the setting would save to disk, survive
// a reload, and do nothing.
func TestSettingsPatch_ApplyCoversEveryField(t *testing.T) {
	// Fill every pointer field with a non-zero value, apply, and assert Settings
	// changed for each one in turn.
	rt := reflect.TypeOf(SettingsPatch{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		p := SettingsPatch{}
		pv := reflect.ValueOf(&p).Elem().Field(i)
		elem := reflect.New(f.Type.Elem())
		switch elem.Elem().Kind() {
		case reflect.Float64:
			elem.Elem().SetFloat(3.5)
		case reflect.Int:
			elem.Elem().SetInt(4242)
		case reflect.Bool:
			elem.Elem().SetBool(true)
		case reflect.String:
			elem.Elem().SetString("q8_0")
		case reflect.Slice:
			elem.Elem().Set(reflect.ValueOf([]int{4096}))
		default:
			t.Fatalf("field %s has unhandled kind %s — extend this test", f.Name, elem.Elem().Kind())
		}
		pv.Set(elem)

		var s Settings
		p.apply(&s)
		if reflect.DeepEqual(s, Settings{}) {
			t.Errorf("(*SettingsPatch).apply ignores field %s — it would save but never take effect", f.Name)
		}
	}
}
