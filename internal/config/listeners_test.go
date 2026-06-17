package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const listenersBaseModels = `
models:
  assistant1:
    cmd: path/to/cmd
    proxy: "http://localhost:8080"
  assistant2:
    cmd: path/to/cmd
    proxy: "http://localhost:8081"
  game1:
    cmd: path/to/cmd
    proxy: "http://localhost:8082"
  judge1:
    cmd: path/to/cmd
    proxy: "http://localhost:8083"
groups:
  assistant:
    members: ["assistant1", "assistant2"]
  game:
    members: ["game1"]
  judge:
    members: ["judge1"]
`

func TestConfig_ListenerModelSets(t *testing.T) {
	content := listenersBaseModels + `
listeners:
  ":1250":
    groups: ["assistant"]
  ":1251":
    groups: ["game", "judge"]
`
	cfg, err := LoadConfigFromReader(strings.NewReader(content))
	require.NoError(t, err)

	sets := cfg.ListenerModelSets()
	assert.Equal(t, map[string]bool{"assistant1": true, "assistant2": true}, sets[":1250"])
	assert.Equal(t, map[string]bool{"game1": true, "judge1": true}, sets[":1251"])

	assert.Equal(t, []string{":1250", ":1251"}, cfg.ListenerAddrs())
}

func TestConfig_ListenerModelSets_EmptyWhenUnset(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader(listenersBaseModels))
	require.NoError(t, err)
	assert.Empty(t, cfg.ListenerModelSets())
	assert.Empty(t, cfg.ListenerAddrs())
}

func TestConfig_ListenersUnknownGroup(t *testing.T) {
	content := listenersBaseModels + `
listeners:
  ":1250":
    groups: ["nope"]
`
	_, err := LoadConfigFromReader(strings.NewReader(content))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "references unknown group")
}

func TestConfig_ListenersEmptyGroups(t *testing.T) {
	content := listenersBaseModels + `
listeners:
  ":1250":
    groups: []
`
	_, err := LoadConfigFromReader(strings.NewReader(content))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exposes no groups")
}

func TestConfig_ListenersDuplicateGroup(t *testing.T) {
	content := listenersBaseModels + `
listeners:
  ":1250":
    groups: ["assistant", "assistant"]
`
	_, err := LoadConfigFromReader(strings.NewReader(content))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than once")
}

func TestConfig_ListenersRejectedWithMatrix(t *testing.T) {
	content := `
models:
  gemma:
    cmd: echo gemma
    proxy: http://localhost:8080
matrix:
  vars:
    g: gemma
  sets:
    combo: "g"
listeners:
  ":1250":
    groups: ["assistant"]
`
	_, err := LoadConfigFromReader(strings.NewReader(content))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "group router")
}
