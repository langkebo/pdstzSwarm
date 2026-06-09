package recon

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/tools"
)

// ParseAttackSurface parses an LLM response into a structured AttackSurface.
// It uses a flexible intermediate representation to handle common LLM output
// mismatches (e.g., subdomains as strings, services as string maps).
func ParseAttackSurface(rawJSON string) (*pipeline.AttackSurface, error) {
	// Strip markdown code fences if present
	rawJSON = stripCodeFence(rawJSON)
	rawJSON = strings.TrimSpace(rawJSON)

	if rawJSON == "" {
		return nil, fmt.Errorf("empty response from LLM")
	}

	// First try direct unmarshal into the strict struct
	var surface pipeline.AttackSurface
	if err := json.Unmarshal([]byte(rawJSON), &surface); err == nil {
		return &surface, nil
	}

	// Try to repair common LLM JSON issues
	repaired := repairLLMJSON(rawJSON)
	if repaired != rawJSON {
		if err2 := json.Unmarshal([]byte(repaired), &surface); err2 == nil {
			return &surface, nil
		}
	}

	// Use flexible intermediate struct to handle LLM output mismatches
	var flex flexibleSurface
	if err := json.Unmarshal([]byte(repaired), &flex); err != nil {
		return nil, fmt.Errorf("parsing attack surface JSON: %w (raw: %.200s)", err, rawJSON)
	}

	result := flex.ToAttackSurface()
	return result, nil
}

// flexibleSurface is a lenient intermediate representation that can handle
// common LLM output format mismatches and convert them to the strict pipeline types.
type flexibleSurface struct {
	Target       string                       `json:"target"`
	Subdomains   []flexibleSubdomain          `json:"subdomains"`
	Hosts        []flexibleHost               `json:"hosts"`
	Endpoints    []flexibleEndpoint           `json:"endpoints"`
	Technologies map[string]string            `json:"technologies"`
	Anomalies    []flexibleAnomaly            `json:"anomalies"`
}

type flexibleSubdomain struct {
	Domain      string `json:"domain"`
	IP          string `json:"ip"`
	CNAME       string `json:"cname"`
	Source      string `json:"source"`
	TakeoverRisk bool  `json:"takeover_risk"`
}

type flexibleHost struct {
	IP         string            `json:"ip"`
	Hostnames  []string          `json:"hostnames"`
	OpenPorts  []int             `json:"open_ports"`
	Services   map[string]string `json:"services"` // LLM returns {"80": "http"} instead of map[int]ServiceRecord
	OS         string            `json:"os"`
	Flags      []string          `json:"flags"`
}

type flexibleEndpoint struct {
	URL              string   `json:"url"`
	Method           string   `json:"method"`
	StatusCode       int      `json:"status_code"`
	ContentType      string   `json:"content_type"`
	ContentLength    int      `json:"content_length"`
	Interesting      bool     `json:"interesting"`
	InterestingReason string  `json:"interesting_reason"`
	ReflectedParams  []string `json:"reflected_params"`
}

type flexibleAnomaly struct {
	Type        string `json:"type"`
	Target      string `json:"target"`
	Description string `json:"description"`
}

// ToAttackSurface converts the flexible intermediate representation
// to the strict pipeline.AttackSurface struct.
func (f *flexibleSurface) ToAttackSurface() *pipeline.AttackSurface {
	surface := &pipeline.AttackSurface{
		Target:       f.Target,
		Technologies: f.Technologies,
	}
	if surface.Technologies == nil {
		surface.Technologies = make(map[string]string)
	}

	// Convert subdomains
	for _, sub := range f.Subdomains {
		record := pipeline.SubdomainRecord{
			Domain: sub.Domain,
			IP:     sub.IP,
			CNAME:  sub.CNAME,
			Source: sub.Source,
		}
		// Handle takeover_risk by noting it in the domain field if present
		if sub.TakeoverRisk {
			record.Source = "takeover_candidate"
		}
		surface.Subdomains = append(surface.Subdomains, record)
	}

	// Convert hosts
	for _, h := range f.Hosts {
		hostRecord := pipeline.HostRecord{
			IP:        h.IP,
			Hostnames: h.Hostnames,
			OpenPorts: h.OpenPorts,
			Services:  make(map[int]pipeline.ServiceRecord),
		}
		// Convert string-keyed services to int-keyed ServiceRecord
		for portStr, svcName := range h.Services {
			var port int
			fmt.Sscanf(portStr, "%d", &port)
			if port > 0 {
				hostRecord.Services[port] = pipeline.ServiceRecord{
					Port: port,
					Name: svcName,
				}
			}
		}
		surface.Hosts = append(surface.Hosts, hostRecord)
	}

	// Convert endpoints
	for _, ep := range f.Endpoints {
		endpointRecord := pipeline.EndpointRecord{
			URL:        ep.URL,
			StatusCode: ep.StatusCode,
			Interesting: ep.Interesting,
			Notes:      ep.InterestingReason,
		}
		if ep.Method != "" {
			endpointRecord.Method = ep.Method
		}
		if len(ep.ReflectedParams) > 0 {
			endpointRecord.Parameters = ep.ReflectedParams
		}
		surface.Endpoints = append(surface.Endpoints, endpointRecord)
	}

	return surface
}

// repairLLMJSON attempts to fix common JSON formatting issues from LLM output.
// LLMs often produce JSON with:
//   - Trailing commas in arrays/objects
//   - Unquoted keys
//   - Single-quoted strings
//   - String values where arrays are expected (e.g. "hosts": "..." instead of "hosts": [...])
//   - Comments (// or /* */)
func repairLLMJSON(raw string) string {
	s := strings.TrimSpace(raw)

	// Remove single-line comments (// ...)
	lines := strings.Split(s, "\n")
	var cleanLines []string
	for _, line := range lines {
		// Skip lines that are pure comments
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		// Remove trailing comment (but be careful with URLs containing //)
		if idx := strings.Index(line, " //"); idx > 0 {
			// Only remove if the // is not part of a URL (no : before it)
			before := line[:idx]
			if !strings.Contains(before, "://") || strings.LastIndex(before, "://") < idx-3 {
				line = before
			}
		}
		cleanLines = append(cleanLines, line)
	}
	s = strings.Join(cleanLines, "\n")

	// Remove trailing commas before closing brackets/braces
	s = strings.ReplaceAll(s, ",}", "}")
	s = strings.ReplaceAll(s, ",]", "]")
	s = strings.ReplaceAll(s, ",\n}", "\n}")
	s = strings.ReplaceAll(s, ",\n]", "\n]")
	s = strings.ReplaceAll(s, ", }", "}")
	s = strings.ReplaceAll(s, ", ]", "]")

	// Fix common field type errors: if a field that should be an array
	// contains a string representation of an array, fix it.
	// e.g. "hosts": "[]" -> "hosts": []
	s = fixStringArrayField(s, "subdomains")
	s = fixStringArrayField(s, "hosts")
	s = fixStringArrayField(s, "endpoints")
	s = fixStringArrayField(s, "anomalies")

	// Fix empty string map fields
	// e.g. "technologies": "" -> "technologies": {}
	s = fixStringMapField(s, "technologies")
	s = fixStringMapField(s, "services")

	return s
}

// fixStringArrayField converts a string value to an empty array for array-typed fields.
// e.g. "hosts": "some text" -> "hosts": []
func fixStringArrayField(jsonStr, fieldName string) string {
	// Pattern: "fieldName": "..." (string value instead of array)
	// We need to find this pattern and replace the string with []
	// This is a simple heuristic that works for common LLM mistakes.
	key := `"` + fieldName + `": "`
	idx := 0
	for {
		pos := strings.Index(jsonStr[idx:], key)
		if pos == -1 {
			break
		}
		absPos := idx + pos + len(key)
		// Find the closing quote of the string value
		endQuote := strings.Index(jsonStr[absPos:], `"`)
		if endQuote == -1 {
			break
		}
		absEnd := absPos + endQuote
		// Replace the string value with []
		jsonStr = jsonStr[:absPos-len(key)] + `"` + fieldName + `": []` + jsonStr[absEnd+1:]
		idx = absPos - len(key) + len(`"`+fieldName+`": []`)
	}
	return jsonStr
}

// fixStringMapField converts a string value to an empty object for map-typed fields.
func fixStringMapField(jsonStr, fieldName string) string {
	key := `"` + fieldName + `": ""`
	if idx := strings.Index(jsonStr, key); idx != -1 {
		jsonStr = jsonStr[:idx] + `"` + fieldName + `": {}` + jsonStr[idx+len(key):]
	}
	// Also handle empty string with whitespace
	key2 := `"` + fieldName + `":""`
	if idx := strings.Index(jsonStr, key2); idx != -1 {
		jsonStr = jsonStr[:idx] + `"` + fieldName + `": {}` + jsonStr[idx+len(key2):]
	}
	return jsonStr
}

// BuildSurfaceFromToolResults constructs a minimal AttackSurface from raw tool
// output when the LLM analysis fails. This ensures the pipeline can continue
// even when the LLM returns malformed JSON.
func BuildSurfaceFromToolResults(results []*tools.ToolResult, target string) *pipeline.AttackSurface {
	surface := &pipeline.AttackSurface{
		Target:       target,
		Technologies: make(map[string]string),
	}

	for _, r := range results {
		if r.Error != nil {
			continue
		}
		for _, finding := range r.ParsedFindings {
			// Extract host information
			if host, ok := finding["host"].(string); ok && host != "" {
				ip := ""
				if ipVal, ok := finding["host_ip"].(string); ok {
					ip = ipVal
				}
				if ipVal, ok := finding["ip"].(string); ok {
					ip = ipVal
				}
				port := 0
				if portVal, ok := finding["port"].(string); ok {
					fmt.Sscanf(portVal, "%d", &port)
				}
				if portVal, ok := finding["port"].(float64); ok {
					port = int(portVal)
				}

				hostRecord := pipeline.HostRecord{
					IP:         ip,
					Hostnames:  []string{host},
					OpenPorts:  []int{},
				}
				if port > 0 {
					hostRecord.OpenPorts = append(hostRecord.OpenPorts, port)
				}
				surface.Hosts = append(surface.Hosts, hostRecord)
			}

			// Extract endpoint information
			if url, ok := finding["url"].(string); ok && url != "" {
				statusCode := 0
				if sc, ok := finding["status_code"].(float64); ok {
					statusCode = int(sc)
				}
				endpoint := pipeline.EndpointRecord{
					URL:        url,
					StatusCode: statusCode,
				}
				surface.Endpoints = append(surface.Endpoints, endpoint)
			}

			// Extract technology information
			if techList, ok := finding["tech"].([]interface{}); ok {
				for _, t := range techList {
					if techStr, ok := t.(string); ok {
						surface.Technologies[techStr] = ""
					}
				}
			}
			if tech, ok := finding["tech"].(string); ok && tech != "" {
				surface.Technologies[tech] = ""
			}

			// Extract webserver / title
			if ws, ok := finding["webserver"].(string); ok && ws != "" {
				surface.Technologies["webserver"] = ws
			}
			if title, ok := finding["title"].(string); ok && title != "" {
				surface.Technologies["title"] = title
			}
		}
	}

	return surface
}

// MergeToolResults deduplicates and enriches findings from multiple tools.
func MergeToolResults(results []*tools.ToolResult) MergedData {
	merged := MergedData{
		Subdomains: make(map[string]bool),
		Hosts:      make(map[string]bool),
		Endpoints:  make(map[string]bool),
	}

	for _, r := range results {
		if r.Error != nil {
			continue
		}

		for _, finding := range r.ParsedFindings {
			if subdomain, ok := finding["subdomain"].(string); ok {
				merged.Subdomains[subdomain] = true
			}
			if host, ok := finding["host"].(string); ok {
				merged.Hosts[host] = true
			}
			if url, ok := finding["url"].(string); ok {
				merged.Endpoints[url] = true
			}
		}
	}

	return merged
}

// MergedData holds deduplicated data from multiple tool results.
type MergedData struct {
	Subdomains map[string]bool
	Hosts      map[string]bool
	Endpoints  map[string]bool
}

// UniqueSubdomains returns the deduplicated subdomain list.
func (m MergedData) UniqueSubdomains() []string {
	result := make([]string, 0, len(m.Subdomains))
	for s := range m.Subdomains {
		result = append(result, s)
	}
	return result
}

// stripCodeFence removes markdown ```json ... ``` wrappers.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)

	// Remove ```json prefix
	if strings.HasPrefix(s, "```json") {
		s = s[7:]
	} else if strings.HasPrefix(s, "```") {
		s = s[3:]
	}

	// Remove ``` suffix
	if strings.HasSuffix(s, "```") {
		s = s[:len(s)-3]
	}

	return strings.TrimSpace(s)
}
