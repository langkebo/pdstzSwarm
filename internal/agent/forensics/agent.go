// Package forensics provides an LLM-powered digital forensics and incident
// response analysis agent for log analysis, timeline construction, and
// evidence correlation.
package forensics

import (
	"context"
	"fmt"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/skills"
)

// Agent performs digital forensics analysis on system logs, memory dumps,
// disk images, and network captures using LLM reasoning augmented with
// community forensics and incident-response skills.
type Agent struct {
	provider      llm.Provider
	skillInjector *skills.Injector
}

// New creates a forensics agent backed by the given LLM provider.
func New(provider llm.Provider) *Agent {
	return &Agent{provider: provider}
}

// WithSkillInjector enables community skill recommendations in the system
// prompt for more accurate forensic analysis.
func (a *Agent) WithSkillInjector(inj *skills.Injector) {
	a.skillInjector = inj
}

// AnalysisResult holds the structured output of a forensic analysis.
type AnalysisResult struct {
	IncidentSummary   string          `json:"incident_summary"`
	Timeline          []TimelineEvent `json:"timeline"`
	InitialAccess     AccessVector    `json:"initial_access"`
	LateralMovement   []MovementStep  `json:"lateral_movement"`
	Persistence       []PersistenceMech `json:"persistence"`
	DataExfiltration  ExfilAssessment `json:"data_exfiltration"`
	IOCs              []string        `json:"iocs"`
	AffectedSystems   []string        `json:"affected_systems"`
	ContainmentSteps  []string        `json:"containment_steps"`
	Confidence        string          `json:"confidence"`
}

// TimelineEvent represents a chronologically ordered forensic artifact.
type TimelineEvent struct {
	Timestamp      string `json:"timestamp"`
	EventType      string `json:"event_type"`
	Description    string `json:"description"`
	SourceArtifact string `json:"source_artifact"`
	Host           string `json:"host"`
	User           string `json:"user,omitempty"`
}

// AccessVector describes how the attacker initially compromised the environment.
type AccessVector struct {
	Method      string `json:"method"`
	Evidence    string `json:"evidence"`
	Confidence  string `json:"confidence"`
}

// MovementStep describes a lateral movement action.
type MovementStep struct {
	FromHost    string `json:"from_host"`
	ToHost      string `json:"to_host"`
	Method      string `json:"method"`
	Timestamp   string `json:"timestamp,omitempty"`
	Evidence    string `json:"evidence"`
}

// PersistenceMech describes an attacker persistence mechanism.
type PersistenceMech struct {
	Type        string `json:"type"`
	Location    string `json:"location"`
	Host        string `json:"host"`
	Status      string `json:"status"`
}

// ExfilAssessment describes data exfiltration findings.
type ExfilAssessment struct {
	Detected    bool     `json:"detected"`
	Method      string   `json:"method,omitempty"`
	Destinations []string `json:"destinations,omitempty"`
	DataTypes   []string `json:"data_types,omitempty"`
	Evidence    string   `json:"evidence,omitempty"`
}

// Analyze performs LLM-based forensic analysis on the provided log and
// evidence data.
func (a *Agent) Analyze(ctx context.Context, incidentID string, evidenceData string) (*AnalysisResult, error) {
	req := llm.CompletionRequest{
		SystemPrompt: a.systemPrompt(incidentID),
		Messages: []llm.Message{
			{
				Role: "user",
				Content: fmt.Sprintf(
					"Analyze the following forensic evidence for incident %q:\n\n%s\n\nProduce a structured forensic analysis in JSON format.",
					incidentID, evidenceData,
				),
			},
		},
		MaxTokens:         8192,
		Temperature:       0.1,
		CacheSystemPrompt: true,
	}

	resp, err := a.provider.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("forensic analysis failed: %w", err)
	}

	var result AnalysisResult
	if err := parseJSON(resp.Content, &result); err != nil {
		return nil, fmt.Errorf("parse analysis result: %w", err)
	}
	return &result, nil
}

// BuildTimeline extracts a chronological event timeline from raw log data.
func (a *Agent) BuildTimeline(ctx context.Context, logData string) ([]TimelineEvent, error) {
	req := llm.CompletionRequest{
		SystemPrompt: a.systemPrompt("timeline"),
		Messages: []llm.Message{
			{
				Role: "user",
				Content: fmt.Sprintf(
					"Extract and chronologically order all security-relevant events from these logs into a timeline JSON array:\n\n%s",
					logData,
				),
			},
		},
		MaxTokens:         8192,
		Temperature:       0.1,
		CacheSystemPrompt: true,
	}

	resp, err := a.provider.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("timeline construction failed: %w", err)
	}

	var events []TimelineEvent
	if err := parseJSON(resp.Content, &events); err != nil {
		return nil, fmt.Errorf("parse timeline: %w", err)
	}
	return events, nil
}

// systemPrompt returns the base system prompt optionally enriched with
// community forensics and incident-response skill recommendations.
func (a *Agent) systemPrompt(context string) string {
	prompt := forensicsSystemPrompt
	if a.skillInjector == nil {
		return prompt
	}
	recs := a.skillInjector.ForObjective("forensics " + context)
	if len(recs) == 0 {
		return prompt
	}
	return prompt + "\n" + skills.MustInject(recs)
}

const forensicsSystemPrompt = `You are an expert digital forensics analyst and incident responder. Analyse system logs, memory dumps, disk images, network captures, and cloud audit trails to reconstruct attacker activity and produce actionable findings.

## Analysis Framework

### 1. Timeline Reconstruction
- Parse timestamps from: syslog, auth.log, Windows Event Log, auditd, CloudTrail
- Normalise time zones — flag any time-skew anomalies (anti-forensic timestomp)
- Order events chronologically and group into attack phases
- Flag gaps in logging that may indicate log tampering or cleaning

### 2. Initial Access Vector
- Web: webshell upload via vulnerable plugin, SQLi to file write, SSRF to metadata
- Credentials: brute-force SSH/RDP, credential stuffing, password spraying
- Phishing: malicious attachment execution, credential harvesting page
- Supply chain: compromised dependency, CI/CD pipeline injection
- Cloud: exposed access keys, misconfigured instance metadata, over-privileged IAM roles

### 3. Execution & Persistence
- Linux: cron, systemd timers, .bashrc/.profile, SSH authorized_keys, LD_PRELOAD
- Windows: Run/RunOnce registry, Scheduled Tasks, WMI event subscriptions, services
- Cloud: Lambda triggers, CloudWatch Events, IAM role assumption chains
- Flag: encoded PowerShell (-enc, -e), base64 bash payloads, living-off-the-land binaries

### 4. Lateral Movement
- SSH: connections from unusual hosts, port forwarding (-L/-R), SSH agent forwarding
- Windows: PSExec, WMI (wmic process call create), WinRM (Enter-PSSession)
- Cloud: cross-account AssumeRole, shared VPC peering abuse
- RDP: connections from public IPs, RDP session hijacking

### 5. Data Collection & Exfiltration
- Archive creation: tar, zip, rar of sensitive directories
- Data staging: unusual directories (/tmp/.hidden, C:\Windows\Temp\staging)
- Exfil channels: HTTP POST large volumes, DNS tunneling (TXT queries > 150 bytes), cloud storage API calls
- Volume analysis: sudden spike in outbound traffic, off-hours data transfer

### 6. Indicator Extraction (IOCs)
- IP addresses, domain names, URLs
- File hashes (MD5, SHA1, SHA256)
- Mutex names, pipe names, registry key paths
- User agents, TLS JA3/JA4 fingerprints
- Cryptocurrency wallet addresses (ransom notes)
- Email addresses (phishing senders, exfil destinations)

### 7. Containment Recommendations
- Immediate: isolate affected hosts (network ACL / security group deny)
- Short-term: rotate compromised credentials, revoke cloud access keys
- Long-term: patch exploited vulnerabilities, enable MFA, segment networks

## Output Format
Respond ONLY with a valid JSON object:
{
  "incident_summary": "one-paragraph executive summary",
  "timeline": [{"timestamp": "ISO8601", "event_type": "string", "description": "string", "source_artifact": "string", "host": "string", "user": "string"}],
  "initial_access": {"method": "string", "evidence": "string", "confidence": "HIGH|MEDIUM|LOW"},
  "lateral_movement": [{"from_host": "string", "to_host": "string", "method": "string", "timestamp": "string", "evidence": "string"}],
  "persistence": [{"type": "string", "location": "string", "host": "string", "status": "active|cleaned|unknown"}],
  "data_exfiltration": {"detected": true|false, "method": "string", "destinations": ["string"], "data_types": ["string"], "evidence": "string"},
  "iocs": ["indicator1", "indicator2"],
  "affected_systems": ["host1", "host2"],
  "containment_steps": ["step1", "step2"],
  "confidence": "HIGH|MEDIUM|LOW"
}

## Anti-patterns
- Do not invent timestamps — use only those present in the evidence
- Do not assume lateral movement without evidence of cross-host activity
- Always indicate confidence level per finding`