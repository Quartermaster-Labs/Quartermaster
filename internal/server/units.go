package server

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// The `convert_units` tool: the same failure class as convert_currency, one
// dimension over. A US spec sheet gives inches and pounds, the user thinks in
// cm and kg, and a model converting from memory drops a factor or rounds 2.54
// to 2.5 — small enough to look right, big enough to pick the wrong product.
//
// Purely local: a fixed factor table, no network, no cache. Temperature is the
// only affine case (offset as well as scale) and is handled separately.

const maxUnitConverts = 12

type unitDef struct {
	dim    string
	factor float64 // multiply by this to reach the dimension's base unit
	name   string  // canonical display name
}

// unitTable maps every accepted spelling to its definition. Aliases are listed
// rather than derived: a model writes "kph", "km/h" and "kmh" for the same
// thing, and guessing at plurals or punctuation is how a lookup silently
// resolves to the wrong dimension.
var unitTable = map[string]unitDef{
	// length — base: metre
	"m": {"length", 1, "m"}, "meter": {"length", 1, "m"}, "meters": {"length", 1, "m"},
	"metre": {"length", 1, "m"}, "metres": {"length", 1, "m"},
	"km": {"length", 1000, "km"}, "kilometer": {"length", 1000, "km"}, "kilometers": {"length", 1000, "km"},
	"kilometre": {"length", 1000, "km"}, "kilometres": {"length", 1000, "km"},
	"cm": {"length", 0.01, "cm"}, "centimeter": {"length", 0.01, "cm"}, "centimeters": {"length", 0.01, "cm"},
	"centimetre": {"length", 0.01, "cm"}, "centimetres": {"length", 0.01, "cm"},
	"mm": {"length", 0.001, "mm"}, "millimeter": {"length", 0.001, "mm"}, "millimeters": {"length", 0.001, "mm"},
	"millimetre": {"length", 0.001, "mm"}, "millimetres": {"length", 0.001, "mm"},
	"in": {"length", 0.0254, "in"}, "inch": {"length", 0.0254, "in"}, "inches": {"length", 0.0254, "in"}, "\"": {"length", 0.0254, "in"},
	"ft": {"length", 0.3048, "ft"}, "foot": {"length", 0.3048, "ft"}, "feet": {"length", 0.3048, "ft"}, "'": {"length", 0.3048, "ft"},
	"yd": {"length", 0.9144, "yd"}, "yard": {"length", 0.9144, "yd"}, "yards": {"length", 0.9144, "yd"},
	"mi": {"length", 1609.344, "mi"}, "mile": {"length", 1609.344, "mi"}, "miles": {"length", 1609.344, "mi"},
	"nmi": {"length", 1852, "nmi"}, "nauticalmile": {"length", 1852, "nmi"},

	// mass — base: kilogram
	"kg": {"mass", 1, "kg"}, "kilogram": {"mass", 1, "kg"}, "kilograms": {"mass", 1, "kg"}, "kilo": {"mass", 1, "kg"}, "kilos": {"mass", 1, "kg"},
	"g": {"mass", 0.001, "g"}, "gram": {"mass", 0.001, "g"}, "grams": {"mass", 0.001, "g"},
	"mg": {"mass", 1e-6, "mg"}, "milligram": {"mass", 1e-6, "mg"}, "milligrams": {"mass", 1e-6, "mg"},
	"t": {"mass", 1000, "t"}, "tonne": {"mass", 1000, "t"}, "tonnes": {"mass", 1000, "t"}, "metricton": {"mass", 1000, "t"},
	"lb": {"mass", 0.45359237, "lb"}, "lbs": {"mass", 0.45359237, "lb"}, "pound": {"mass", 0.45359237, "lb"}, "pounds": {"mass", 0.45359237, "lb"},
	"oz": {"mass", 0.028349523125, "oz"}, "ounce": {"mass", 0.028349523125, "oz"}, "ounces": {"mass", 0.028349523125, "oz"},
	"st": {"mass", 6.35029318, "st"}, "stone": {"mass", 6.35029318, "st"},

	// volume — base: litre
	"l": {"volume", 1, "L"}, "liter": {"volume", 1, "L"}, "liters": {"volume", 1, "L"}, "litre": {"volume", 1, "L"}, "litres": {"volume", 1, "L"},
	"ml": {"volume", 0.001, "mL"}, "milliliter": {"volume", 0.001, "mL"}, "milliliters": {"volume", 0.001, "mL"},
	"millilitre": {"volume", 0.001, "mL"}, "millilitres": {"volume", 0.001, "mL"}, "cc": {"volume", 0.001, "mL"},
	"m3": {"volume", 1000, "m³"}, "cubicmeter": {"volume", 1000, "m³"}, "cubicmetre": {"volume", 1000, "m³"},
	"gal": {"volume", 3.785411784, "US gal"}, "gallon": {"volume", 3.785411784, "US gal"}, "gallons": {"volume", 3.785411784, "US gal"},
	// The "US "/"UK " prefixed spellings are the canonical display names above:
	// a model that reads one out of a previous result must be able to pass it
	// straight back in.
	"usgal": {"volume", 3.785411784, "US gal"}, "usgallon": {"volume", 3.785411784, "US gal"},
	"ukgal": {"volume", 4.54609, "UK gal"}, "imperialgallon": {"volume", 4.54609, "UK gal"}, "ukgallon": {"volume", 4.54609, "UK gal"},
	"usqt": {"volume", 0.946352946, "US qt"}, "uspt": {"volume", 0.473176473, "US pt"},
	"uscup": {"volume", 0.2365882365, "US cup"}, "usfloz": {"volume", 0.0295735295625, "US fl oz"},
	"ukfloz": {"volume", 0.0284130625, "UK fl oz"}, "ukpt": {"volume", 0.56826125, "UK pt"},
	"qt": {"volume", 0.946352946, "US qt"}, "quart": {"volume", 0.946352946, "US qt"},
	"pt": {"volume", 0.473176473, "US pt"}, "pint": {"volume", 0.473176473, "US pt"},
	"cup": {"volume", 0.2365882365, "US cup"}, "cups": {"volume", 0.2365882365, "US cup"},
	"floz": {"volume", 0.0295735295625, "US fl oz"}, "fluidounce": {"volume", 0.0295735295625, "US fl oz"},
	"tbsp": {"volume", 0.01478676478, "tbsp"}, "tsp": {"volume", 0.004928921594, "tsp"},

	// area — base: square metre
	"m2": {"area", 1, "m²"}, "sqm": {"area", 1, "m²"}, "squaremeter": {"area", 1, "m²"},
	"cm2": {"area", 0.0001, "cm²"}, "sqcm": {"area", 0.0001, "cm²"},
	"km2": {"area", 1e6, "km²"}, "sqkm": {"area", 1e6, "km²"},
	"ft2": {"area", 0.09290304, "ft²"}, "sqft": {"area", 0.09290304, "ft²"}, "squarefoot": {"area", 0.09290304, "ft²"}, "squarefeet": {"area", 0.09290304, "ft²"},
	"in2": {"area", 0.00064516, "in²"}, "sqin": {"area", 0.00064516, "in²"},
	"ha": {"area", 10000, "ha"}, "hectare": {"area", 10000, "ha"},
	"acre": {"area", 4046.8564224, "acre"}, "acres": {"area", 4046.8564224, "acre"},

	// speed — base: metre/second
	"mps": {"speed", 1, "m/s"}, "m/s": {"speed", 1, "m/s"},
	"kmh": {"speed", 0.277777778, "km/h"}, "km/h": {"speed", 0.277777778, "km/h"}, "kph": {"speed", 0.277777778, "km/h"},
	"mph": {"speed", 0.44704, "mph"}, "mi/h": {"speed", 0.44704, "mph"},
	"kn": {"speed", 0.514444444, "kn"}, "knot": {"speed", 0.514444444, "kn"}, "knots": {"speed", 0.514444444, "kn"},

	// power — base: watt
	"w": {"power", 1, "W"}, "watt": {"power", 1, "W"}, "watts": {"power", 1, "W"},
	"kw": {"power", 1000, "kW"}, "kilowatt": {"power", 1000, "kW"}, "kilowatts": {"power", 1000, "kW"},
	"hp": {"power", 745.6998716, "hp"}, "horsepower": {"power", 745.6998716, "hp"},
	"ps":    {"power", 735.49875, "PS"},
	"btu/h": {"power", 0.29307107, "BTU/h"}, "btuh": {"power", 0.29307107, "BTU/h"},

	// energy — base: joule
	"j": {"energy", 1, "J"}, "joule": {"energy", 1, "J"}, "joules": {"energy", 1, "J"},
	"kj": {"energy", 1000, "kJ"}, "kilojoule": {"energy", 1000, "kJ"},
	"wh": {"energy", 3600, "Wh"}, "watthour": {"energy", 3600, "Wh"},
	"kwh": {"energy", 3.6e6, "kWh"}, "kilowatthour": {"energy", 3.6e6, "kWh"},
	"cal": {"energy", 4.184, "cal"}, "kcal": {"energy", 4184, "kcal"}, "calorie": {"energy", 4.184, "cal"}, "calories": {"energy", 4.184, "cal"},
	"btu": {"energy", 1055.05585, "BTU"},

	// data — base: byte. Decimal and binary kept distinct on purpose: a drive's
	// "1 TB" and an OS's "1 TiB" differ by 10%, which is the entire complaint.
	"b": {"data", 1, "B"}, "byte": {"data", 1, "B"}, "bytes": {"data", 1, "B"},
	"kb": {"data", 1e3, "kB"}, "mb": {"data", 1e6, "MB"}, "gb": {"data", 1e9, "GB"}, "tb": {"data", 1e12, "TB"},
	"kib": {"data", 1024, "KiB"}, "mib": {"data", 1 << 20, "MiB"}, "gib": {"data", 1 << 30, "GiB"}, "tib": {"data", 1 << 40, "TiB"},
	"bit": {"data", 0.125, "bit"}, "bits": {"data", 0.125, "bit"},
	"mbit": {"data", 125000, "Mbit"}, "gbit": {"data", 1.25e8, "Gbit"},

	// time — base: second
	"s": {"time", 1, "s"}, "sec": {"time", 1, "s"}, "second": {"time", 1, "s"}, "seconds": {"time", 1, "s"},
	"min": {"time", 60, "min"}, "minute": {"time", 60, "min"}, "minutes": {"time", 60, "min"},
	"h": {"time", 3600, "h"}, "hr": {"time", 3600, "h"}, "hour": {"time", 3600, "h"}, "hours": {"time", 3600, "h"},
	"day": {"time", 86400, "day"}, "days": {"time", 86400, "day"},
	"week": {"time", 604800, "week"}, "weeks": {"time", 604800, "week"},
	"year": {"time", 31557600, "year"}, "years": {"time", 31557600, "year"},

	// pressure — base: pascal
	"pa": {"pressure", 1, "Pa"}, "kpa": {"pressure", 1000, "kPa"},
	"bar": {"pressure", 100000, "bar"}, "mbar": {"pressure", 100, "mbar"},
	"psi": {"pressure", 6894.757293, "psi"}, "atm": {"pressure", 101325, "atm"},
}

// tempUnits is separate because temperature is affine, not a pure scale.
var tempUnits = map[string]string{
	"c": "°C", "celsius": "°C", "centigrade": "°C", "°c": "°C",
	"f": "°F", "fahrenheit": "°F", "°f": "°F",
	"k": "K", "kelvin": "K",
}

// normUnit strips the punctuation and spacing models scatter through unit names
// without touching the characters that distinguish one unit from another —
// "/" survives (km/h), and so do the quote marks used for inch and foot.
func normUnit(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(
		"²", "2", "³", "3", "µ", "u", "”", "\"", "’", "'",
		" ", "", ".", "", "_", "", "-", "",
	).Replace(s)
	return s
}

func parseUnitArgs(raw string) (float64, string, string) {
	var a struct {
		Amount any    `json:"amount"`
		Value  any    `json:"value"`
		From   string `json:"from"`
		Unit   string `json:"unit"`
		To     string `json:"to"`
		Target string `json:"target"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return 0, "", ""
	}
	amount := 1.0
	for _, v := range []any{a.Amount, a.Value} {
		switch n := v.(type) {
		case float64:
			amount = n
		case string:
			if f, err := evalExpr(n); err == nil {
				amount = f
			}
		default:
			continue
		}
		break
	}
	from := a.From
	if strings.TrimSpace(from) == "" {
		from = a.Unit
	}
	to := a.To
	if strings.TrimSpace(to) == "" {
		to = a.Target
	}
	return amount, strings.TrimSpace(from), strings.TrimSpace(to)
}

// convertUnit returns the converted value plus the canonical names of both
// units. A cross-dimension request (kg → cm) is an error, not a number: it
// means the model misread the spec, and answering anyway hides that.
func convertUnit(amount float64, from, to string) (float64, string, string, error) {
	nf, nt := normUnit(from), normUnit(to)
	if nf == "" || nt == "" {
		return 0, "", "", fmt.Errorf("convert_units needs both `from` and `to`")
	}
	if cf, ok := tempUnits[nf]; ok {
		ct, ok2 := tempUnits[nt]
		if !ok2 {
			return 0, "", "", fmt.Errorf("cannot convert temperature (%s) to %q", cf, to)
		}
		return convertTemp(amount, cf, ct), cf, ct, nil
	}
	df, ok := unitTable[nf]
	if !ok {
		return 0, "", "", fmt.Errorf("unknown unit %q. Known: %s", from, knownUnitHint())
	}
	dt, ok := unitTable[nt]
	if !ok {
		if _, isTemp := tempUnits[nt]; isTemp {
			return 0, "", "", fmt.Errorf("cannot convert %s to a temperature", df.dim)
		}
		return 0, "", "", fmt.Errorf("unknown unit %q. Known: %s", to, knownUnitHint())
	}
	if df.dim != dt.dim {
		return 0, "", "", fmt.Errorf("%s is %s and %s is %s - those do not convert into each other", df.name, df.dim, dt.name, dt.dim)
	}
	return amount * df.factor / dt.factor, df.name, dt.name, nil
}

func convertTemp(v float64, from, to string) float64 {
	// via kelvin
	var k float64
	switch from {
	case "°C":
		k = v + 273.15
	case "°F":
		k = (v-32)*5/9 + 273.15
	default:
		k = v
	}
	switch to {
	case "°C":
		return k - 273.15
	case "°F":
		return (k-273.15)*9/5 + 32
	default:
		return k
	}
}

func knownUnitHint() string {
	dims := map[string]bool{}
	for _, d := range unitTable {
		dims[d.dim] = true
	}
	out := make([]string, 0, len(dims)+1)
	for d := range dims {
		out = append(out, d)
	}
	out = append(out, "temperature")
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func formatUnitConvert(amount float64, from string, v float64, to string) string {
	return fmt.Sprintf("%s %s = %s %s", fmtCalcNum(roundTo(amount, 6)), from, fmtCalcNum(roundTo(v, 6)), to)
}

// roundTo trims float noise (0.30000000000000004) at the display edge only.
// Large magnitudes are left alone: scaling them up to round would overflow the
// mantissa and corrupt the very digits that matter (bytes in a TB).
func roundTo(v float64, digits int) float64 {
	if math.Abs(v) >= 1e12 {
		return v
	}
	p := math.Pow(10, float64(digits))
	return math.Round(v*p) / p
}
