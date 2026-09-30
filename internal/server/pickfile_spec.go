package server

// filePickSpec describes what a native open-file dialog should offer. The
// platform pickFile implementations interpolate these strings into a shell /
// PowerShell command line, so specs are a SERVER-SIDE whitelist (pickSpecs) —
// never built from request data.
type filePickSpec struct {
	Title string
	// WinFilter is a Windows OpenFileDialog Filter string ("Label (*.ext)|*.ext|...").
	WinFilter string
	// ZenityPatterns are zenity --file-filter values ("Label | *.ext").
	ZenityPatterns []string
	// Exts is what the browser-rendered picker (pickbrowse.go) lists by default:
	// lower-case extensions with the dot. Empty lists every file, which is what
	// an executable needs off Windows, where binaries carry no extension.
	Exts []string
}

// pickSpecs maps the UI's file-picker kinds to their dialog config. The
// /api/pick-file handler rejects anything not in here.
var pickSpecs = map[string]filePickSpec{
	"backend": {
		Title:          "Select backend executable",
		WinFilter:      "Executables (*.exe)|*.exe|All files (*.*)|*.*",
		ZenityPatterns: []string{"Executables | *"},
	},
	"template": {
		Title:          "Select chat template file",
		WinFilter:      "Chat templates (*.jinja;*.j2;*.jinja2)|*.jinja;*.j2;*.jinja2|All files (*.*)|*.*",
		ZenityPatterns: []string{"Chat templates | *.jinja *.j2 *.jinja2", "All files | *"},
		Exts:           []string{".jinja", ".j2", ".jinja2"},
	},
	"mmproj": {
		Title:          "Select vision projector (mmproj gguf)",
		WinFilter:      "GGUF models (*.gguf)|*.gguf|All files (*.*)|*.*",
		ZenityPatterns: []string{"GGUF models | *.gguf", "All files | *"},
		Exts:           []string{".gguf"},
	},
}
