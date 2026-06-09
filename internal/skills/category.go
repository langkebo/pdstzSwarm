// Package skills provides a curated registry of AI Agent security skills
// sourced from the openclaw-sec-skills community project.
//
// It defines the Category taxonomy and provides lookup helpers for mapping
// skill categories to the Pentest-Swarm-AI agent ecosystem.
package skills

import "strings"

// Category represents a security domain classification for skills.
type Category string

const (
	CategoryCodeAudit         Category = "code_audit"
	CategoryPentest           Category = "pentest"
	CategoryReverseEngineering Category = "reverse_engineering"
	CategoryCTF               Category = "ctf"
	CategoryThreatModeling    Category = "threat_modeling"
	CategoryMobileSecurity    Category = "mobile_security"
	CategoryIncidentResponse  Category = "incident_response"
	CategorySecurityTools     Category = "security_tools"
)

// AllCategories returns every registered category in display order.
func AllCategories() []Category {
	return []Category{
		CategoryCodeAudit,
		CategoryPentest,
		CategoryReverseEngineering,
		CategoryCTF,
		CategoryThreatModeling,
		CategoryMobileSecurity,
		CategoryIncidentResponse,
		CategorySecurityTools,
	}
}

// DisplayName returns the human-readable Chinese label for c.
func (c Category) DisplayName() string {
	switch c {
	case CategoryCodeAudit:
		return "代码审计"
	case CategoryPentest:
		return "渗透测试"
	case CategoryReverseEngineering:
		return "逆向工程"
	case CategoryCTF:
		return "CTF竞赛"
	case CategoryThreatModeling:
		return "威胁建模"
	case CategoryMobileSecurity:
		return "移动安全"
	case CategoryIncidentResponse:
		return "应急响应"
	case CategorySecurityTools:
		return "安全工具"
	default:
		return string(c)
	}
}

// Emoji returns a decorative emoji for c.
func (c Category) Emoji() string {
	switch c {
	case CategoryCodeAudit:
		return "🔒"
	case CategoryPentest:
		return "⚔️"
	case CategoryReverseEngineering:
		return "🔍"
	case CategoryCTF:
		return "🏆"
	case CategoryThreatModeling:
		return "🎯"
	case CategoryMobileSecurity:
		return "📱"
	case CategoryIncidentResponse:
		return "🚨"
	case CategorySecurityTools:
		return "🛡️"
	default:
		return "📦"
	}
}

// ParseCategory converts a string to a Category. It handles both the
// internal identifier and the Chinese display name. Returns the
// zero value if the input does not match any known category.
func ParseCategory(s string) Category {
	s = strings.TrimSpace(s)
	switch s {
	case "code_audit", "代码审计":
		return CategoryCodeAudit
	case "pentest", "渗透测试":
		return CategoryPentest
	case "reverse_engineering", "逆向工程":
		return CategoryReverseEngineering
	case "ctf", "CTF竞赛":
		return CategoryCTF
	case "threat_modeling", "威胁建模":
		return CategoryThreatModeling
	case "mobile_security", "移动安全":
		return CategoryMobileSecurity
	case "incident_response", "应急响应":
		return CategoryIncidentResponse
	case "security_tools", "安全工具":
		return CategorySecurityTools
	default:
		return ""
	}
}