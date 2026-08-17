package server

import "github.com/quartermaster-labs/quartermaster/internal/tools"

// The web-search and YouTube tool executors live in internal/tools so the
// /v1/tools HTTP API (toolsapi.go) and the playground turn loop share one
// implementation. These aliases keep turns.go, websearch.go and friends
// reading unchanged; new code should import tools directly.

type (
	searchProviderCfg = tools.SearchProvider
	ytVideo           = tools.Video
)

const (
	webDefaultResults = tools.DefaultResults
	webMaxResults     = tools.MaxResults
)

// Per-turn tool budgets — turn-loop policy (one chat turn can only spend so
// much context on transcripts/searches), not part of the shared executors,
// so they live here rather than in internal/tools.
const (
	maxYouTube = 8
	// ytTurnTokens is the real limiter; a turn may read this much transcript
	// text (~tokens) across all its youtube_transcript calls.
	ytTurnTokens = 40000
	// ytMinTranscript is the floor below which a further fetch is refused
	// rather than served as a stub the model would narrate over.
	ytMinTranscript = 1500
	maxYtBrowse     = 4
	maxYtComments   = 6
)

var (
	searchChain             = tools.Search
	legacySearchChain       = tools.LegacyChain
	formatSearchResults     = tools.FormatSearchResults
	fetchYouTubeTranscript  = tools.GetTranscript
	parseYouTubeID          = tools.ParseVideoID
	formatYouTubeTranscript = tools.FormatTranscript
	ytSearch                = tools.SearchVideos
	ytChannelVideos         = tools.ChannelVideos
	fetchYouTubeComments    = tools.GetComments
	formatYouTubeVideos     = tools.FormatVideos
	formatYouTubeComments   = tools.FormatComments
	ytVideoID               = tools.VideoID
	cleanFeedText           = tools.CleanFeedText
	readLimited             = tools.ReadLimited
	orURL                   = tools.OrURL
)
