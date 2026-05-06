package app

import (
	"encoding/json"
	"testing"
)

func TestSlimRawBodyKeepsOnlyAllowedFields(t *testing.T) {
	raw := []byte(`{
		"action": {
			"type": "updateCard",
			"data": {
				"card": {"id": "69ae188a", "name": "[AVM Module Issue]: ...", "desc": "ignored"},
				"listBefore": {"name": "Backlog", "id": "x"},
				"listAfter": {"name": "Analyze", "id": "y"},
				"board": {"name": "Main Board"}
			},
			"memberCreator": {"fullName": "HeZijie", "username": "hzj"},
			"id": "action-id"
		},
		"model": "should-not-forward"
	}`)

	slim, err := slimRawBody(raw)
	if err != nil {
		t.Fatalf("slim: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(slim, &got); err != nil {
		t.Fatalf("unmarshal slim body: %v", err)
	}

	action, ok := got["action"].(map[string]any)
	if !ok {
		t.Fatalf("missing action: %v", got)
	}
	if action["type"] != "updateCard" {
		t.Fatalf("unexpected action.type: %v", action)
	}

	data, ok := action["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing action.data: %v", action)
	}
	card, ok := data["card"].(map[string]any)
	if !ok || card["id"] != "69ae188a" {
		t.Fatalf("unexpected card: %v", data["card"])
	}
	if _, ok := card["name"]; ok {
		t.Fatalf("card.name should be removed: %v", card)
	}
	if _, ok := card["desc"]; ok {
		t.Fatalf("card.desc should be removed: %v", card)
	}

	listBefore, ok := data["listBefore"].(map[string]any)
	if !ok || listBefore["name"] != "Backlog" || listBefore["id"] != "x" {
		t.Fatalf("unexpected listBefore: %v", data["listBefore"])
	}
	listAfter, ok := data["listAfter"].(map[string]any)
	if !ok || listAfter["name"] != "Analyze" || listAfter["id"] != "y" {
		t.Fatalf("unexpected listAfter: %v", data["listAfter"])
	}
	if _, ok := data["board"]; ok {
		t.Fatalf("action.data.board should be removed: %v", data)
	}
	if _, ok := action["memberCreator"]; ok {
		t.Fatalf("memberCreator should be removed: %v", action)
	}
	if _, ok := action["id"]; ok {
		t.Fatalf("action.id should be removed: %v", action)
	}
	if _, ok := got["model"]; ok {
		t.Fatalf("top-level model should be removed: %v", got)
	}
}

func TestSlimRawBodyRejectsInvalidJSON(t *testing.T) {
	if _, err := slimRawBody([]byte(`not-json`)); err == nil {
		t.Fatal("expected error on invalid json")
	}
}
