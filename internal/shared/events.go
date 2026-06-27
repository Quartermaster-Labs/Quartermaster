package shared

const ProcessStateChangeEventID = 0x01
const ConfigFileChangedEventID = 0x03
const ActivityLogEventID = 0x05
const ModelPreloadedEventID = 0x06
const InFlightRequestsEventID = 0x07
const LiveTokensEventID = 0x08
const BackendMetricsEventID = 0x09

// ProcessStateChangeEvent is emitted whenever a process transitions between
// lifecycle states. States are carried as strings so this package stays a leaf
// (no import of internal/process).
type ProcessStateChangeEvent struct {
	ProcessName string
	OldState    string
	NewState    string
}

func (e ProcessStateChangeEvent) Type() uint32 {
	return ProcessStateChangeEventID
}

type ReloadingState int

const (
	ReloadingStateStart ReloadingState = iota
	ReloadingStateEnd
)

type ConfigFileChangedEvent struct {
	State ReloadingState
}

func (e ConfigFileChangedEvent) Type() uint32 {
	return ConfigFileChangedEventID
}

type ModelPreloadedEvent struct {
	ModelName string
	Success   bool
}

func (e ModelPreloadedEvent) Type() uint32 {
	return ModelPreloadedEventID
}

type InFlightRequestsEvent struct {
	Total int
}

func (e InFlightRequestsEvent) Type() uint32 {
	return InFlightRequestsEventID
}

// LiveTokensEvent reports in-progress generation for a streaming request: the
// number of completion tokens emitted so far and the elapsed time since the
// upstream started responding. Emitted (throttled) while the stream is open so
// the UI can show a live tokens/sec readout before the final metrics land.
type LiveTokensEvent struct {
	Model        string
	OutputTokens int
	ElapsedMs    int
	// FirstTokenMs is the measured time-to-first-token: ms from the upstream
	// request start to the first generated chunk. -1 until the first token
	// lands. Lets the UI show real TTFT and a decode-only tokens/sec (excluding
	// prompt-processing time) instead of a prompt-diluted lifetime average.
	FirstTokenMs int
}

func (e LiveTokensEvent) Type() uint32 {
	return LiveTokensEventID
}
