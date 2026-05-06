package app

import (
	"encoding/json"
	"fmt"
)

// slimRawBody reduces a raw Trello webhook payload to the minimal set of
// routing-relevant fields, dropping all free-form text in order to limit
// prompt-injection surface from arbitrary user-supplied card content.
func slimRawBody(rawBody []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(rawBody, &raw); err != nil {
		return nil, fmt.Errorf("raw body must be valid json: %w", err)
	}

	action := map[string]any{}
	if v, ok := nestedString(raw, "action", "type"); ok {
		action["type"] = v
	}

	data := map[string]any{}
	card := map[string]any{}
	if v, ok := nestedString(raw, "action", "data", "card", "id"); ok {
		card["id"] = v
	}
	if len(card) > 0 {
		data["card"] = card
	}

	listBefore := map[string]any{}
	if v, ok := nestedString(raw, "action", "data", "listBefore", "name"); ok {
		listBefore["name"] = v
	}
	if v, ok := nestedString(raw, "action", "data", "listBefore", "id"); ok {
		listBefore["id"] = v
	}
	if len(listBefore) > 0 {
		data["listBefore"] = listBefore
	}

	listAfter := map[string]any{}
	if v, ok := nestedString(raw, "action", "data", "listAfter", "name"); ok {
		listAfter["name"] = v
	}
	if v, ok := nestedString(raw, "action", "data", "listAfter", "id"); ok {
		listAfter["id"] = v
	}
	if len(listAfter) > 0 {
		data["listAfter"] = listAfter
	}

	if len(data) > 0 {
		action["data"] = data
	}

	payload := map[string]any{}
	if len(action) > 0 {
		payload["action"] = action
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal slim payload: %w", err)
	}
	return b, nil
}

func nestedString(m map[string]any, path ...string) (string, bool) {
	if len(path) == 0 {
		return "", false
	}

	current := any(m)
	for i, key := range path {
		obj, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		value, ok := obj[key]
		if !ok {
			return "", false
		}
		if i == len(path)-1 {
			s, ok := value.(string)
			if !ok || s == "" {
				return "", false
			}
			return s, true
		}
		current = value
	}

	return "", false
}
