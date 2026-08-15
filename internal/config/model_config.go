package config

import (
	"errors"
	"fmt"
	"runtime"
)

const (
	MODEL_CONFIG_DEFAULT_TTL = -1
)

var validModalities = map[string]struct{}{
	"text":  {},
	"audio": {},
	"image": {},
}

// ModelCapConfig defines what modalities and features a model supports.
// Used in /v1/models to inform clients. An empty block (all zero values) is
// treated as not configured.
type ModelCapConfig struct {
	In           []string `yaml:"in"`
	Out          []string `yaml:"out"`
	Tools        bool     `yaml:"tools"`
	Reranker     bool     `yaml:"reranker"`
	Embedding    bool     `yaml:"embedding"`
	Segmentation bool     `yaml:"segmentation"`
	// VoiceClone marks a speech model that can register NEW voices from a
	// reference clip (qwentts.cpp). It is not implied by out:[audio] — a
	// fixed-voice-pack engine like TTS.cpp/Kokoro serves speech but has no
	// endpoint to clone with, and offering the UI for it produces a 404 at best.
	VoiceClone bool `yaml:"voiceClone"`
	// ReasoningEffort lists the reasoning-effort levels this model's chat
	// template accepts (Qwen 3.8: xhigh/medium/low), lowest-cost value LAST as
	// the template declares them. Non-empty is the signal that an incoming
	// OpenAI `reasoning_effort` field may be translated into a
	// chat_template_kwargs entry — a template that doesn't understand the value
	// raises, which surfaces as a 500, so an unlisted model is never translated
	// for. Advertised in /v1/models so a client can discover the ladder.
	ReasoningEffort []string `yaml:"reasoningEffort"`
	Context         int      `yaml:"context"`
}

// Empty returns true when all fields are at their zero values.
func (c ModelCapConfig) Empty() bool {
	return len(c.In) == 0 && len(c.Out) == 0 && !c.Tools && !c.Reranker && !c.Embedding && !c.Segmentation && !c.VoiceClone && len(c.ReasoningEffort) == 0 && c.Context == 0
}

// Validate checks that all modality values are recognized and context is
// non-negative. Returns an error if any value is invalid.
func (c ModelCapConfig) Validate() error {
	for _, m := range c.In {
		if _, ok := validModalities[m]; !ok {
			return fmt.Errorf("capabilities.in: invalid modality %q, must be one of: text, audio, image", m)
		}
	}
	for _, m := range c.Out {
		if _, ok := validModalities[m]; !ok {
			return fmt.Errorf("capabilities.out: invalid modality %q, must be one of: text, audio, image", m)
		}
	}
	if c.Context < 0 {
		return errors.New("capabilities.context: must be >= 0")
	}
	return nil
}

// TimeoutsConfig holds timeout settings for proxy connections
// 0 = no timeout
type TimeoutsConfig struct {
	Connect        int `yaml:"connect"`
	KeepAlive      int `yaml:"keepalive"`
	ResponseHeader int `yaml:"responseHeader"`
	TLSHandshake   int `yaml:"tlsHandshake"`
	ExpectContinue int `yaml:"expectContinue"`
	IdleConn       int `yaml:"idleConn"`
}

type ModelConfig struct {
	Cmd           string   `yaml:"cmd"`
	CmdStop       string   `yaml:"cmdStop"`
	Proxy         string   `yaml:"proxy"`
	Aliases       []string `yaml:"aliases"`
	Env           []string `yaml:"env"`
	CheckEndpoint string   `yaml:"checkEndpoint"`
	UnloadAfter   int      `yaml:"ttl"`
	Unlisted      bool     `yaml:"unlisted"`
	UseModelName  string   `yaml:"useModelName"`

	// #179 for /v1/models
	Name        string `yaml:"name"`
	Description string `yaml:"description"`

	// Limit concurrency of HTTP requests to process
	ConcurrencyLimit int `yaml:"concurrencyLimit"`

	// Model filters see issue #174
	Filters ModelFilters `yaml:"filters"`

	// Macros: see #264
	// Model level macros take precedence over the global macros
	Macros MacroList `yaml:"macros"`

	// Metadata: see #264
	// Arbitrary metadata that can be exposed through the API
	Metadata map[string]any `yaml:"metadata"`

	// override global setting
	SendLoadingState *bool `yaml:"sendLoadingState"`

	// Timeout settings for proxy connections
	Timeouts TimeoutsConfig `yaml:"timeouts"`

	// Capabilities defines what modalities and features the model supports.
	Capabilities ModelCapConfig `yaml:"capabilities"`

	// EstVramGB is this model's predicted VRAM footprint when loaded (weights +
	// KV reserve + compute buffer + overhead), as computed by the autogen sizer
	// at generate time. It is the admission input for VRAM-budget-aware
	// multi-load (see Config.VramBudgetGB): the router sums it over the resident
	// set to decide whether a new model fits alongside them or something must be
	// evicted first. 0 = unknown (hand-written config, or a class the sizer does
	// not model), which the router treats conservatively — see
	// internal/router/group.go.
	EstVramGB float64 `yaml:"estVramGB"`

	// EstRamGB is the sizer's companion figure: the share of the weights (plus
	// KV, when it is kept in system memory) that does NOT fit on the GPU and is
	// served from RAM. 0 for a fully-offloaded model. Purely informational — the
	// router admits on EstVramGB — but the dashboard shows it per model so the
	// cost of a partial offload is visible before loading.
	EstRamGB float64 `yaml:"estRamGB"`

	// Copy of HealthCheckTimeout from global config
	HealthCheckTimeout int `yaml:"healthCheckTimeout"`
}

func (m *ModelConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawModelConfig ModelConfig
	defaults := rawModelConfig{
		Cmd:              "",
		CmdStop:          "",
		Proxy:            "http://localhost:${PORT}",
		Aliases:          []string{},
		Env:              []string{},
		CheckEndpoint:    "/health",
		UnloadAfter:      MODEL_CONFIG_DEFAULT_TTL, // use GlobalTTL
		Unlisted:         false,
		UseModelName:     "",
		ConcurrencyLimit: 0,
		Name:             "",
		Description:      "",

		// matches http.DefaultTransport
		Timeouts: TimeoutsConfig{
			Connect:        30,
			KeepAlive:      30,
			ResponseHeader: 0,
			TLSHandshake:   10,
			ExpectContinue: 1,
			IdleConn:       90,
		},
	}

	// the default cmdStop to taskkill /f /t /pid ${PID}
	if runtime.GOOS == "windows" {
		defaults.CmdStop = "taskkill /f /t /pid ${PID}"
	}

	if err := unmarshal(&defaults); err != nil {
		return err
	}

	*m = ModelConfig(defaults)
	return nil
}

func (m *ModelConfig) SanitizedCommand() ([]string, error) {
	return SanitizeCommand(m.Cmd)
}

// ModelFilters embeds Filters and adds legacy support for strip_params field
// See issue #174
type ModelFilters struct {
	Filters `yaml:",inline"`
}

func (m *ModelFilters) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawModelFilters ModelFilters
	defaults := rawModelFilters{}

	if err := unmarshal(&defaults); err != nil {
		return err
	}

	// Try to unmarshal with the old field name for backwards compatibility
	if defaults.StripParams == "" {
		var legacy struct {
			StripParams string `yaml:"strip_params"`
		}
		if legacyErr := unmarshal(&legacy); legacyErr != nil {
			return errors.New("failed to unmarshal legacy filters.strip_params: " + legacyErr.Error())
		}
		defaults.StripParams = legacy.StripParams
	}

	*m = ModelFilters(defaults)
	return nil
}

// SanitizedStripParams wraps Filters.SanitizedStripParams for backwards compatibility
// Returns ([]string, error) to match existing API
func (f ModelFilters) SanitizedStripParams() ([]string, error) {
	return f.Filters.SanitizedStripParams(), nil
}
