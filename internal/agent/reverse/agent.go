// Package reverse provides an LLM-powered reverse engineering analysis agent
// that handles binary analysis, malware triage, unpacking, and decompilation
// reasoning.
package reverse

import (
	"context"
	"fmt"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/skills"
)

// Agent analyses binaries, firmware, and malware samples using LLM reasoning
// augmented with community reverse-engineering skills.
type Agent struct {
	provider      llm.Provider
	skillInjector *skills.Injector
}

// New creates a reverse-engineering agent backed by the given LLM provider.
func New(provider llm.Provider) *Agent {
	return &Agent{provider: provider}
}

// WithSkillInjector enables community skill recommendations in the system
// prompt for more informed binary and malware analysis.
func (a *Agent) WithSkillInjector(inj *skills.Injector) {
	a.skillInjector = inj
}

// AnalysisResult holds the structured output of a reverse-engineering analysis.
type AnalysisResult struct {
	Summary        string   `json:"summary"`
	FileType       string   `json:"file_type"`
	Architecture   string   `json:"architecture"`
	Obfuscation    []string `json:"obfuscation"`
	Capabilities   []string `json:"capabilities"`
	IOCs           []string `json:"iocs"`
	RiskLevel      string   `json:"risk_level"`
	Recommendations []string `json:"recommendations"`
}

// Analyze performs LLM-based reverse engineering analysis on the provided
// sample metadata and tool output.
func (a *Agent) Analyze(ctx context.Context, sampleName string, toolOutput string) (*AnalysisResult, error) {
	req := llm.CompletionRequest{
		SystemPrompt: a.systemPrompt(sampleName),
		Messages: []llm.Message{
			{
				Role: "user",
				Content: fmt.Sprintf(
					"Analyze the following reverse engineering tool output for sample %q:\n\n%s\n\nProduce a structured analysis in JSON format.",
					sampleName, toolOutput,
				),
			},
		},
		MaxTokens:         8192,
		Temperature:       0.1,
		CacheSystemPrompt: true,
	}

	resp, err := a.provider.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("reverse engineering analysis failed: %w", err)
	}

	var result AnalysisResult
	if err := parseJSON(resp.Content, &result); err != nil {
		return nil, fmt.Errorf("parse analysis result: %w", err)
	}
	return &result, nil
}

// systemPrompt returns the base system prompt optionally enriched with
// community reverse-engineering skill recommendations.
func (a *Agent) systemPrompt(sample string) string {
	prompt := reverseSystemPrompt
	if a.skillInjector == nil {
		return prompt
	}
	recs := a.skillInjector.ForTarget(sample)
	if len(recs) == 0 {
		return prompt
	}
	return prompt + "\n" + skills.MustInject(recs)
}

const reverseSystemPrompt = `You are an expert reverse engineer and malware analyst. Your task is to analyse output from binary analysis tools (objdump, strings, Ghidra, IDA, radare2, binwalk, etc.) and produce a structured security assessment.

## Analysis Framework

### 1. File Identification
- Determine file type (ELF, PE, Mach-O, firmware image, shellcode, script)
- Identify target architecture (x86, x86_64, ARM, ARM64, MIPS, etc.)
- Detect packing / compression (UPX, ASPack, VMProtect, custom)

### 2. Obfuscation & Anti-Analysis
- Anti-debugging: ptrace checks, PEB.BeingDebugged, NtGlobalFlag
- Anti-VM: RDTSC, CPUID hypervisor bit, VM-tagged registry keys
- Control-flow obfuscation: opaque predicates, junk code, control-flow flattening
- String encryption: XOR, RC4, AES with embedded or derived keys
- Import hiding: dynamic resolution via GetProcAddress / dlsym

### 3. Capability Assessment
- Network: socket creation, HTTP/DNS/raw sockets, C2 beacon patterns
- File system: read/write/delete, directory traversal, file enumeration
- Process: CreateToolhelp32Snapshot, CreateRemoteThread, process hollowing
- Persistence: registry Run keys, scheduled tasks, services, WMI, cron, launchd
- Credential access: LSASS dumping, /etc/shadow reads, browser password stores
- Privilege escalation: token manipulation, SeDebugPrivilege, sudo/CAP_SETUID
- Data exfiltration: HTTP POST, DNS tunneling, cloud storage API calls
- Encryption: crypto library imports, hardcoded keys, custom ciphers

### 4. Indicator Extraction (IOCs)
- Hardcoded IPs, domains, URLs
- Mutex names, pipe names, registry key paths
- File paths, dropped file names
- Cryptographic hashes (MD5, SHA1, SHA256)
- C2 beacon intervals and jitter patterns
- User-Agent strings and TLS certificate fingerprints

### 5. Risk Assessment
- Rate as: LOW, MEDIUM, HIGH, CRITICAL
- Consider: stealth, persistence, propagation, impact, data sensitivity

## Output Format
Respond ONLY with a valid JSON object:
{
  "summary": "one-paragraph executive summary",
  "file_type": "ELF|PE|Mach-O|script|...",
  "architecture": "x86_64|ARM64|...",
  "obfuscation": ["technique1", "technique2"],
  "capabilities": ["cap1", "cap2"],
  "iocs": ["indicator1", "indicator2"],
  "risk_level": "LOW|MEDIUM|HIGH|CRITICAL",
  "recommendations": ["rec1", "rec2"]
}

## Anti-patterns
- Do not hallucinate capabilities not evidenced in the tool output
- Do not guess architecture — use what the tools report
- Always classify risk based on observed behaviour, not speculation`