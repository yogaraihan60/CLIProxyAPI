package executor

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type responsesSessionState struct {
	lastRequest        []byte
	lastResponseOutput []byte
	responseID         string
}

var globalResponsesStateStore = &responsesStateStore{sessions: map[string]responsesSessionState{}, responses: map[string]responsesSessionState{}}

type responsesStateStore struct {
	mu        sync.Mutex
	sessions  map[string]responsesSessionState
	responses map[string]responsesSessionState
}

func (s *responsesStateStore) get(sessionID string) (responsesSessionState, bool) {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return responsesSessionState{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.sessions[sessionID]
	if !ok {
		return responsesSessionState{}, false
	}
	return cloneResponsesState(state), true
}

func (s *responsesStateStore) put(sessionID string, state responsesSessionState) {
	if s == nil {
		return
	}
	cloned := cloneResponsesState(state)
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(sessionID) != "" {
		s.sessions[sessionID] = cloned
	}
	if strings.TrimSpace(cloned.responseID) != "" {
		s.responses[cloned.responseID] = cloned
	}
}

func (s *responsesStateStore) delete(sessionID string) {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.sessions[sessionID]
	delete(s.sessions, sessionID)
	if ok && strings.TrimSpace(state.responseID) != "" {
		delete(s.responses, state.responseID)
	}
}

func cloneResponsesState(state responsesSessionState) responsesSessionState {
	cloned := responsesSessionState{responseID: state.responseID}
	if len(state.lastRequest) > 0 {
		cloned.lastRequest = append([]byte(nil), state.lastRequest...)
	}
	if len(state.lastResponseOutput) > 0 {
		cloned.lastResponseOutput = append([]byte(nil), state.lastResponseOutput...)
	}
	return cloned
}

func (s *responsesStateStore) getByResponseID(responseID string) (responsesSessionState, bool) {
	if s == nil || strings.TrimSpace(responseID) == "" {
		return responsesSessionState{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.responses[responseID]
	if !ok {
		return responsesSessionState{}, false
	}
	return cloneResponsesState(state), true
}

func rebuildResponsesRequestFromState(rawJSON []byte, state responsesSessionState) ([]byte, bool, error) {
	// For backends that reject previous_response_id, rebuild the full input transcript from cached state.
	if len(rawJSON) == 0 || len(state.lastRequest) == 0 {
		return rawJSON, false, nil
	}
	if strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()) == "" {
		return rawJSON, false, nil
	}
	if !gjson.GetBytes(rawJSON, "input").Exists() || !gjson.GetBytes(rawJSON, "input").IsArray() {
		return rawJSON, false, nil
	}

	existingInput := gjson.GetBytes(state.lastRequest, "input")
	mergedInput, err := mergeResponsesJSONArrayRaw(existingInput.Raw, normalizeResponsesJSONArrayRaw(state.lastResponseOutput))
	if err != nil {
		return nil, false, err
	}
	mergedInput, err = mergeResponsesJSONArrayRaw(mergedInput, gjson.GetBytes(rawJSON, "input").Raw)
	if err != nil {
		return nil, false, err
	}

	normalized := append([]byte(nil), rawJSON...)
	normalized, _ = sjson.DeleteBytes(normalized, "previous_response_id")
	normalized, err = sjson.SetRawBytes(normalized, "input", []byte(mergedInput))
	if err != nil {
		return nil, false, err
	}
	if !gjson.GetBytes(normalized, "model").Exists() {
		if modelName := strings.TrimSpace(gjson.GetBytes(state.lastRequest, "model").String()); modelName != "" {
			normalized, _ = sjson.SetBytes(normalized, "model", modelName)
		}
	}
	if !gjson.GetBytes(normalized, "instructions").Exists() {
		instructions := gjson.GetBytes(state.lastRequest, "instructions")
		if instructions.Exists() {
			normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
		}
	}
	return normalized, true, nil
}

func mergeResponsesJSONArrayRaw(existingRaw, appendRaw string) (string, error) {
	existingRaw = strings.TrimSpace(existingRaw)
	appendRaw = strings.TrimSpace(appendRaw)
	if existingRaw == "" {
		existingRaw = "[]"
	}
	if appendRaw == "" {
		appendRaw = "[]"
	}

	var existing []json.RawMessage
	if err := json.Unmarshal([]byte(existingRaw), &existing); err != nil {
		return "", err
	}
	var appendItems []json.RawMessage
	if err := json.Unmarshal([]byte(appendRaw), &appendItems); err != nil {
		return "", err
	}

	merged := append(existing, appendItems...)
	out, err := json.Marshal(merged)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func normalizeResponsesJSONArrayRaw(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "[]"
	}
	result := gjson.Parse(trimmed)
	if result.Type == gjson.JSON && result.IsArray() {
		return trimmed
	}
	return "[]"
}

func responseCompletedOutputFromPayloadForExecutor(payload []byte) []byte {
	if !gjson.ValidBytes(payload) {
		return []byte("[]")
	}
	output := gjson.GetBytes(payload, "response.output")
	if output.Exists() && output.IsArray() {
		return []byte(output.Raw)
	}
	return []byte("[]")
}

func rebuildResponsesRequestFromStore(rawJSON []byte, sessionID string) ([]byte, bool, error) {
	if len(rawJSON) == 0 {
		return rawJSON, false, nil
	}
	prev := strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String())
	if prev == "" {
		return rawJSON, false, nil
	}
	if state, ok := globalResponsesStateStore.get(sessionID); ok {
		if rebuilt, rebuiltOK, err := rebuildResponsesRequestFromState(rawJSON, state); rebuiltOK || err != nil {
			return rebuilt, rebuiltOK, err
		}
	}
	if state, ok := globalResponsesStateStore.getByResponseID(prev); ok {
		return rebuildResponsesRequestFromState(rawJSON, state)
	}
	return rawJSON, false, nil
}
