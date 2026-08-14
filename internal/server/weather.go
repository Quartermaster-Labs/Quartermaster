package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The `get_weather` tool: Open-Meteo, keyless.
//
// Two calls, both public and free with no signup: a geocoding lookup that turns
// "Cluj" into coordinates, then the forecast itself. No API key means no secret
// to store and nothing to leak out of a config file — the reason this source was
// picked over the better-known ones.
//
// The place name is model text, so it goes through url.Values encoding, never
// string concatenation into a path.

const (
	wxTimeout  = 15 * time.Second
	wxCacheTTL = 30 * time.Minute
	wxCacheMax = 64
	// maxWeather caps calls per turn: a trip question covers two or three
	// cities, not twenty.
	maxWeather  = 4
	wxMaxDays   = 7
	wxGeoBase   = "https://geocoding-api.open-meteo.com/v1/search"
	wxFcastBase = "https://api.open-meteo.com/v1/forecast"
)

type wxCacheEntry struct {
	text string
	at   time.Time
}

var (
	wxCacheMu sync.Mutex
	wxCache   = map[string]wxCacheEntry{}
)

func parseWeatherArgs(raw string) (string, int, bool) {
	var a struct {
		Location string  `json:"location"`
		Place    string  `json:"place"`
		City     string  `json:"city"`
		Query    string  `json:"query"`
		Days     float64 `json:"days"`
		Units    string  `json:"units"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return "", 0, false
	}
	loc := ""
	for _, s := range []string{a.Location, a.Place, a.City, a.Query} {
		if strings.TrimSpace(s) != "" {
			loc = strings.TrimSpace(s)
			break
		}
	}
	days := int(a.Days)
	if days <= 0 {
		days = 3
	}
	if days > wxMaxDays {
		days = wxMaxDays
	}
	imperial := strings.EqualFold(strings.TrimSpace(a.Units), "imperial") || strings.EqualFold(strings.TrimSpace(a.Units), "f")
	return loc, days, imperial
}

type wxPlace struct {
	Name, Admin, Country, TZ string
	Lat, Lon                 float64
}

func wxGeocode(ctx context.Context, place string) (*wxPlace, error) {
	q := url.Values{"name": {place}, "count": {"1"}, "format": {"json"}}
	var r struct {
		Results []struct {
			Name      string  `json:"name"`
			Admin1    string  `json:"admin1"`
			Country   string  `json:"country"`
			Timezone  string  `json:"timezone"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"results"`
	}
	if err := fxGetJSON(ctx, wxGeoBase+"?"+q.Encode(), &r); err != nil {
		return nil, err
	}
	if len(r.Results) == 0 {
		return nil, fmt.Errorf("no place called %q was found", place)
	}
	g := r.Results[0]
	return &wxPlace{Name: g.Name, Admin: g.Admin1, Country: g.Country, TZ: g.Timezone, Lat: g.Latitude, Lon: g.Longitude}, nil
}

// wmoText maps WMO weather codes to words. Open-Meteo returns the code only,
// and "code 63" in the tool result is something the model would have to
// hallucinate a meaning for.
var wmoText = map[int]string{
	0: "clear", 1: "mostly clear", 2: "partly cloudy", 3: "overcast",
	45: "fog", 48: "freezing fog",
	51: "light drizzle", 53: "drizzle", 55: "heavy drizzle",
	56: "freezing drizzle", 57: "heavy freezing drizzle",
	61: "light rain", 63: "rain", 65: "heavy rain",
	66: "freezing rain", 67: "heavy freezing rain",
	71: "light snow", 73: "snow", 75: "heavy snow", 77: "snow grains",
	80: "light showers", 81: "showers", 82: "violent showers",
	85: "snow showers", 86: "heavy snow showers",
	95: "thunderstorm", 96: "thunderstorm with hail", 99: "thunderstorm with heavy hail",
}

func wmo(code int) string {
	if s, ok := wmoText[code]; ok {
		return s
	}
	return fmt.Sprintf("weather code %d", code)
}

func fetchWeather(ctx context.Context, place string, days int, imperial bool) (string, error) {
	key := fmt.Sprintf("%s|%d|%v", strings.ToLower(place), days, imperial)
	wxCacheMu.Lock()
	if e, ok := wxCache[key]; ok && time.Since(e.at) < wxCacheTTL {
		wxCacheMu.Unlock()
		return e.text, nil
	}
	wxCacheMu.Unlock()

	g, err := wxGeocode(ctx, place)
	if err != nil {
		return "", err
	}

	q := url.Values{
		"latitude":  {fmt.Sprintf("%.4f", g.Lat)},
		"longitude": {fmt.Sprintf("%.4f", g.Lon)},
		"current":   {"temperature_2m,apparent_temperature,precipitation,weather_code,wind_speed_10m,relative_humidity_2m"},
		"daily":     {"weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum,precipitation_probability_max,wind_speed_10m_max"},
		"timezone":  {"auto"},
		// The API counts today as day 1, so "3 days" reads as today plus two.
		"forecast_days": {fmt.Sprint(days)},
	}
	tUnit, wUnit, pUnit := "°C", "km/h", "mm"
	if imperial {
		q.Set("temperature_unit", "fahrenheit")
		q.Set("wind_speed_unit", "mph")
		q.Set("precipitation_unit", "inch")
		tUnit, wUnit, pUnit = "°F", "mph", "in"
	}
	var r struct {
		Current struct {
			Time     string  `json:"time"`
			Temp     float64 `json:"temperature_2m"`
			Feels    float64 `json:"apparent_temperature"`
			Precip   float64 `json:"precipitation"`
			Code     int     `json:"weather_code"`
			Wind     float64 `json:"wind_speed_10m"`
			Humidity float64 `json:"relative_humidity_2m"`
		} `json:"current"`
		Daily struct {
			Time    []string  `json:"time"`
			Code    []int     `json:"weather_code"`
			Max     []float64 `json:"temperature_2m_max"`
			Min     []float64 `json:"temperature_2m_min"`
			Precip  []float64 `json:"precipitation_sum"`
			PrecipP []float64 `json:"precipitation_probability_max"`
			Wind    []float64 `json:"wind_speed_10m_max"`
		} `json:"daily"`
	}
	if err := fxGetJSON(ctx, wxFcastBase+"?"+q.Encode(), &r); err != nil {
		return "", err
	}

	where := g.Name
	if g.Admin != "" && g.Admin != g.Name {
		where += ", " + g.Admin
	}
	if g.Country != "" {
		where += ", " + g.Country
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Weather for %s (local time %s, %s).\n", where, r.Current.Time, g.TZ)
	fmt.Fprintf(&b, "Now: %s, %.0f%s (feels %.0f%s), wind %.0f %s, humidity %.0f%%, precipitation %.1f %s.\n",
		wmo(r.Current.Code), r.Current.Temp, tUnit, r.Current.Feels, tUnit, r.Current.Wind, wUnit, r.Current.Humidity, r.Current.Precip, pUnit)
	if len(r.Daily.Time) > 0 {
		b.WriteString("Forecast:\n")
		for i := range r.Daily.Time {
			day := r.Daily.Time[i]
			if t, err := time.Parse("2006-01-02", day); err == nil {
				day = t.Format("Mon 2 Jan")
			}
			fmt.Fprintf(&b, "- %s: %s, %.0f to %.0f%s", day, wmo(at(r.Daily.Code, i)), atf(r.Daily.Min, i), atf(r.Daily.Max, i), tUnit)
			if p := atf(r.Daily.PrecipP, i); p > 0 {
				fmt.Fprintf(&b, ", %.0f%% chance of precipitation (%.1f %s)", p, atf(r.Daily.Precip, i), pUnit)
			}
			fmt.Fprintf(&b, ", wind up to %.0f %s\n", atf(r.Daily.Wind, i), wUnit)
		}
	}
	b.WriteString("Source: Open-Meteo, read just now. A forecast beyond about three days is a trend, not a fact - say so if the user is planning on one.")
	out := b.String()

	wxCacheMu.Lock()
	if len(wxCache) >= wxCacheMax {
		wxCache = map[string]wxCacheEntry{}
	}
	wxCache[key] = wxCacheEntry{text: out, at: time.Now()}
	wxCacheMu.Unlock()
	return out, nil
}

// at/atf index the daily arrays defensively: Open-Meteo returns one array per
// field and a short one would otherwise panic mid-render.
func at(v []int, i int) int {
	if i < len(v) {
		return v[i]
	}
	return -1
}

func atf(v []float64, i int) float64 {
	if i < len(v) {
		return v[i]
	}
	return 0
}
