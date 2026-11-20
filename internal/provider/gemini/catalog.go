package gemini

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
)

type normalizedModel struct {
	ID                 string         `json:"id"`
	DisplayName        string         `json:"display_name,omitempty"`
	MaxInputTokens     int            `json:"max_input_tokens,omitempty"`
	MaxOutputTokens    int            `json:"max_output_tokens,omitempty"`
	SupportsTools      bool           `json:"supports_tools"`
	SupportsReasoning  *bool          `json:"supports_reasoning,omitempty"`
	SupportedEndpoints []string       `json:"supported_endpoints,omitempty"`
	Capabilities       map[string]any `json:"capabilities,omitempty"`
}

func NormalizeCatalogJSON(raw []byte, endpoint string) ([]byte, error) {
	var probe struct {
		Data          json.RawMessage `json:"data"`
		Models        json.RawMessage `json:"models"`
		NextPageToken string          `json:"nextPageToken"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	if d := strings.TrimSpace(string(probe.Data)); strings.HasPrefix(d, "[") {
		return raw, nil
	}
	m := strings.TrimSpace(string(probe.Models))
	if strings.HasPrefix(m, "{") {
		var grouped struct {
			GroupedModels map[string]effortGroup `json:"groupedModels"`
		}
		_ = json.Unmarshal(raw, &grouped)
		return normalizeCodeAssistModels(probe.Models, grouped.GroupedModels, endpoint)
	}
	if strings.HasPrefix(m, "[") {
		return normalizeGenerativeModels(probe.Models, probe.NextPageToken, endpoint)
	}
	return raw, nil
}

func normalizeCodeAssistModels(raw json.RawMessage, groups map[string]effortGroup, endpoint string) ([]byte, error) {
	var byID map[string]struct {
		DisplayName     string `json:"displayName"`
		SupportsImages  *bool  `json:"supportsImages"`
		SupportsVideo   *bool  `json:"supportsVideo"`
		ThinkingLevel   int    `json:"thinkingLevel"`
		MaxTokens       int    `json:"maxTokens"`
		MaxInputTokens  int    `json:"maxInputTokens"`
		MaxOutputTokens int    `json:"maxOutputTokens"`
	}
	if err := json.Unmarshal(raw, &byID); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		if id == "" || strings.HasSuffix(id, "-disabled") {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	data := make([]normalizedModel, 0, len(ids))
	for _, id := range ids {
		entry := byID[id]
		maxIn := entry.MaxInputTokens
		maxOut := entry.MaxOutputTokens
		if maxIn > 0 {
			if maxOut == 0 {
				maxOut = entry.MaxTokens
			}
		} else {
			maxIn = entry.MaxTokens
		}
		nm := normalizedModel{
			ID:                 id,
			DisplayName:        entry.DisplayName,
			MaxInputTokens:     maxIn,
			MaxOutputTokens:    maxOut,
			SupportsTools:      true,
			SupportedEndpoints: []string{endpoint},
		}
		if entry.SupportsImages != nil || entry.SupportsVideo != nil {
			images := false
			if entry.SupportsImages != nil {
				images = *entry.SupportsImages
			} else if entry.SupportsVideo != nil {
				images = *entry.SupportsVideo
			}
			nm.Capabilities = map[string]any{
				"image_input": map[string]any{"supported": images},
			}
		}
		if entry.ThinkingLevel > 0 || strings.HasSuffix(id, "-thinking") {
			yes := true
			nm.SupportsReasoning = &yes
		}
		data = append(data, nm)
	}
	members := effortMembers(groups, data)
	for i := range data {
		if rung, ok := members[data[i].ID]; ok {
			if data[i].Capabilities == nil {
				data[i].Capabilities = map[string]any{}
			}
			data[i].Capabilities["reasoning_effort"] = []string{rung}
		}
	}
	return json.Marshal(map[string]any{"data": data})
}

type effortGroup struct {
	DisplayName string `json:"displayName"`
	Levels      []struct {
		Suffix string `json:"thinkingSuffix"`
	} `json:"supportedThinkingLevels"`
}

var effortWords = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

func effortMembers(groups map[string]effortGroup, data []normalizedModel) map[string]string {
	out := map[string]string{}
	for base, g := range groups {
		for _, l := range g.Levels {
			if s := strings.ToLower(strings.TrimSpace(l.Suffix)); s != "" {
				out[base+"-"+s] = s
			}
		}
	}
	for _, m := range data {
		if _, ok := out[m.ID]; ok {
			continue
		}
		cut := strings.LastIndex(m.ID, "-")
		if cut <= 0 {
			continue
		}
		rung := m.ID[cut+1:]
		if slices.Contains(effortWords, rung) && titleNamesRung(m.DisplayName, rung) {
			out[m.ID] = rung
		}
	}
	for id := range out {
		if !slices.ContainsFunc(data, func(m normalizedModel) bool { return m.ID == id }) {
			delete(out, id)
		}
	}
	return out
}

func titleNamesRung(name, rung string) bool {
	open := strings.LastIndex(name, "(")
	if open < 0 || !strings.HasSuffix(name, ")") {
		return false
	}
	label := strings.ToLower(strings.Join(strings.Fields(name[open+1:len(name)-1]), ""))
	return label == rung || (rung == "xhigh" && label == "extrahigh")
}

func normalizeGenerativeModels(raw json.RawMessage, nextToken, endpoint string) ([]byte, error) {
	var entries []struct {
		Name                       string   `json:"name"`
		DisplayName                string   `json:"displayName"`
		InputTokenLimit            int      `json:"inputTokenLimit"`
		OutputTokenLimit           int      `json:"outputTokenLimit"`
		Thinking                   *bool    `json:"thinking"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	data := make([]normalizedModel, 0, len(entries))
	for _, e := range entries {
		id := strings.TrimPrefix(strings.TrimSpace(e.Name), "models/")
		if id == "" || strings.HasSuffix(id, "-disabled") {
			continue
		}
		if len(e.SupportedGenerationMethods) > 0 && !slices.Contains(e.SupportedGenerationMethods, "generateContent") {
			continue
		}
		nm := normalizedModel{
			ID:                 id,
			DisplayName:        e.DisplayName,
			MaxInputTokens:     e.InputTokenLimit,
			MaxOutputTokens:    e.OutputTokenLimit,
			SupportsTools:      true,
			SupportsReasoning:  e.Thinking,
			SupportedEndpoints: []string{endpoint},
		}
		data = append(data, nm)
	}
	out := map[string]any{"data": data}
	if nextToken != "" {
		out["has_more"] = true
		out["nextPageToken"] = nextToken
	}
	return json.Marshal(out)
}
