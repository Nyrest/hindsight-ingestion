package connectors

import (
	"path"
	"strings"
)

// File type groups shared by global and task-specific policies.
const (
	GroupPlainText = "plainText"
	GroupDocuments = "documents"
	GroupImages    = "images"
	GroupAudios    = "audios"
)

// FilePolicy toggles each file type group.
type FilePolicy struct {
	PlainText bool `json:"plainText"`
	Documents bool `json:"documents"`
	Images    bool `json:"images"`
	Audios    bool `json:"audios"`
}

// DefaultFilePolicy is the out-of-the-box global policy.
var DefaultFilePolicy = FilePolicy{PlainText: true, Documents: true}

// Allows reports whether a file type group is enabled.
func (p FilePolicy) Allows(group string) bool {
	switch group {
	case GroupPlainText:
		return p.PlainText
	case GroupDocuments:
		return p.Documents
	case GroupImages:
		return p.Images
	case GroupAudios:
		return p.Audios
	}
	return false
}

var extGroups = map[string]string{}

func init() {
	register := func(group string, exts ...string) {
		for _, e := range exts {
			extGroups[e] = group
		}
	}
	register(GroupPlainText,
		"txt", "md", "markdown", "mdx", "rst", "org", "adoc", "asciidoc", "tex", "log",
		"csv", "tsv", "json", "jsonl", "ndjson", "yaml", "yml", "toml", "ini", "cfg", "conf", "xml",
		"html", "htm", "rtf", "srt", "vtt",
		"go", "py", "js", "jsx", "ts", "tsx", "java", "kt", "c", "h", "cpp", "hpp", "cs", "rs",
		"rb", "php", "swift", "scala", "sh", "bash", "ps1", "sql", "css", "scss", "vue", "svelte", "lua", "r", "dart")
	register(GroupDocuments,
		"pdf", "doc", "docx", "odt", "ppt", "pptx", "odp", "xls", "xlsx", "ods", "epub", "pages", "numbers", "key")
	register(GroupImages,
		"png", "jpg", "jpeg", "gif", "webp", "bmp", "tif", "tiff", "heic", "heif", "svg", "avif")
	register(GroupAudios,
		"mp3", "wav", "m4a", "aac", "flac", "ogg", "oga", "opus", "wma", "aiff", "aif", "webm")
}

// FileGroup returns the file type group for a file name ("" when unknown).
// Determination is primarily by extension, falling back to MIME type.
func FileGroup(name, mimeType string) string {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
	if g, ok := extGroups[ext]; ok {
		return g
	}
	mt := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(mt, "text/"):
		return GroupPlainText
	case strings.HasPrefix(mt, "image/"):
		return GroupImages
	case strings.HasPrefix(mt, "audio/"):
		return GroupAudios
	case mt == "application/pdf", strings.Contains(mt, "officedocument"), strings.Contains(mt, "opendocument"),
		mt == "application/msword", mt == "application/vnd.ms-excel", mt == "application/vnd.ms-powerpoint":
		return GroupDocuments
	case mt == "application/json", mt == "application/xml":
		return GroupPlainText
	}
	return ""
}
