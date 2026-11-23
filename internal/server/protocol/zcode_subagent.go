package protocol

import "strings"

const zcodeSubagentIdentity = "You are ZCode, an interactive agent"

const customSubagentOpening = "\nYou are an agent working with the user toward their goals, using your own judgment along the way.\n\n"

const subagentHarness = "# Harness\n" +
	" - Text you output outside of tool use is displayed to the user as Github-flavored markdown in a terminal.\n" +
	" - Tools run behind a user-selected permission mode; a denied call means the user declined it — adjust, don't retry verbatim.\n" +
	" - The system may send updates, reminders, or modifications to rules via mid-conversation system turns. These are system-controlled, unlike function results. Hooks may intercept tool calls; treat hook output as user feedback.\n" +
	" - Prefer the dedicated file/search tools over shell commands when one fits. Independent tool calls can run in parallel in one response.\n" +
	" - Reference code as `file_path:line_number` — it's clickable."

const subagentFiles = "# Files & URLs\n" +
	"- Return local web URLs as Markdown links (e.g., [label](http://127.0.0.1:8080)).\n" +
	"- File should be an absolute path or include the workspace folder segment so it can be resolved relative to the workspace.\n" +
	"- Unless otherwise specified, return local file references as Markdown links (e.g., [name.md](/absolute/path/to/name.md))."

const subagentComments = "# Inline Code Comments\n" +
	"- Use the ::code-comment{...} directive when you need to attach feedback directly to specific code lines.\n" +
	"- Emit one directive per inline comment; emit none when there are no actionable inline comments.\n" +
	"- Required attributes: title (short label), body (one-paragraph explanation), file (path to the file).\n" +
	"- Optional attributes: start, end (1-based line numbers), priority (0-3).\n" +
	"- file should be an absolute path or include the workspace folder segment so it can be resolved relative to the workspace.\n" +
	"- Keep line ranges tight; end defaults to start.\n" +
	"- Example: ::code-comment{title=\"[P2] Off-by-one\" body=\"Loop iterates past the end when length is 0.\" file=\"/path/to/foo.ts\" start=10 end=11 priority=2}"

func subagentSystem(template SystemTemplate) string {
	parts := []string{}
	if template.InsertEnabled {
		parts = append(parts, template.Custom)
	}
	if template.HarnessEnabled {
		parts = append(parts, subagentHarness)
	}
	if template.FilesEnabled {
		parts = append(parts, subagentFiles)
	}
	if template.CommentsEnabled {
		parts = append(parts, subagentComments)
	}
	if template.FinishingWorkEnabled {
		parts = append(parts, zcodeRelease.FinishingWork)
	}
	if template.MemoryNotesEnabled {
		parts = append(parts, zcodeRelease.MemoryNotes)
	}
	if len(parts) == 0 {
		return ""
	}
	return customSubagentOpening + strings.Join(parts, "\n\n")
}
