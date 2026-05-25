package app

import (
	"encoding/json"
	"fmt"
)

// BuildLogSummary renders a human-friendly one-line description of a
// Trello webhook event for log lines and the operator REPL/TUI.
//
// SECURITY NOTE — DISPLAY ONLY:
// The fallback branch for unrecognised action shapes embeds the
// compactified raw payload (`raw=<json>`). That payload is fully
// user-controlled (a Trello user can put anything they want into card
// names, descriptions, and comments). DO NOT use this string as input
// to a model prompt, system prompt, tool call argument, or any other
// LLM-bound surface — that would defeat the prompt-injection
// mitigations in slim.go by re-introducing untrusted free-form text.
//
// For prompt-assembly use BuildPromptSummary instead.
func BuildLogSummary(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Sprintf("Trello: invalid payload %q", string(raw))
	}

	action := asMap(payload["action"])
	actionType := asString(action["type"])
	data := asMap(action["data"])
	card := asMap(data["card"])
	board := asMap(data["board"])
	member := asMap(action["memberCreator"])

	cardName := fallback(asString(card["name"]), "unknown-card")
	boardName := fallback(asString(board["name"]), "unknown-board")
	fullName := fallback(asString(member["fullName"]), "unknown-user")

	listBefore := asMap(data["listBefore"])
	listAfter := asMap(data["listAfter"])
	listBeforeName := asString(listBefore["name"])
	listAfterName := asString(listAfter["name"])

	if listBeforeName != "" && listAfterName != "" {
		return fmt.Sprintf("Trello: card %q moved from %q to %q (by %s)", cardName, listBeforeName, listAfterName, fullName)
	}

	if actionType == "commentCard" {
		text := fallback(asString(data["text"]), "")
		return fmt.Sprintf("Trello: %s commented on card %q: %s", fullName, cardName, text)
	}

	return fmt.Sprintf("Trello: %s on card %q in board %q by %s raw=%s", fallback(actionType, "unknown-action"), cardName, boardName, fullName, compactJSON(raw))
}

// BuildPromptSummary renders a one-line description of a Trello webhook
// event safe for inclusion in a worker's prompt. It is a strict subset
// of BuildLogSummary: the unrecognised-action fallback omits the raw
// JSON payload that would otherwise smuggle untrusted card content
// past the slim.go prompt-injection mitigations.
//
// The recognised shapes (list move, commentCard) embed cardName /
// listBefore / listAfter / commenter / comment text — those fields are
// inherently user-controlled and prompt-assembly callers must already
// account for that, but at least the surface is bounded to known fields.
func BuildPromptSummary(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "Trello: invalid payload (omitted)"
	}

	action := asMap(payload["action"])
	actionType := asString(action["type"])
	data := asMap(action["data"])
	card := asMap(data["card"])
	board := asMap(data["board"])
	member := asMap(action["memberCreator"])

	cardName := fallback(asString(card["name"]), "unknown-card")
	boardName := fallback(asString(board["name"]), "unknown-board")
	fullName := fallback(asString(member["fullName"]), "unknown-user")

	listBefore := asMap(data["listBefore"])
	listAfter := asMap(data["listAfter"])
	listBeforeName := asString(listBefore["name"])
	listAfterName := asString(listAfter["name"])

	if listBeforeName != "" && listAfterName != "" {
		return fmt.Sprintf("Trello: card %q moved from %q to %q (by %s)", cardName, listBeforeName, listAfterName, fullName)
	}

	if actionType == "commentCard" {
		text := fallback(asString(data["text"]), "")
		return fmt.Sprintf("Trello: %s commented on card %q: %s", fullName, cardName, text)
	}

	// Unrecognised action: report only the action type plus the named
	// fields — DO NOT splice raw JSON. The slimmed payload that
	// prompt-assembly callers append separately already gives the worker
	// a structured view of the event without untrusted text.
	return fmt.Sprintf("Trello: %s on card %q in board %q by %s",
		fallback(actionType, "unknown-action"), cardName, boardName, fullName)
}

// BuildMessage is the legacy entry point kept for backwards compatibility.
// New code should call BuildLogSummary (display) or BuildPromptSummary
// (prompt assembly) explicitly. Removing it requires updating every
// caller in this package and is intentionally deferred to keep this
// security patch small.
//
// Deprecated: use BuildLogSummary or BuildPromptSummary.
func BuildMessage(raw []byte) string { return BuildLogSummary(raw) }

func compactJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func fallback(v, fb string) string {
	if v == "" {
		return fb
	}
	return v
}
