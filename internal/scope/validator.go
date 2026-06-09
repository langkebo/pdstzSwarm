package scope

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	apperrors "github.com/Armur-Ai/Pentest-Swarm-AI/internal/errors"
)

// ScopeDefinition defines what targets are allowed.
type ScopeDefinition struct {
	AllowedCIDRs   []string `json:"allowed_cidrs"   yaml:"allowed_cidrs"`
	AllowedDomains []string `json:"allowed_domains" yaml:"allowed_domains"`
	AllowedPorts   []int    `json:"allowed_ports"   yaml:"allowed_ports,omitempty"` // empty means all ports allowed
	ExcludedCIDRs  []string `json:"excluded_cidrs"  yaml:"excluded_cidrs,omitempty"`
}

// Validate checks whether a target (IP, domain, CIDR, or URL) is within scope.
// Returns nil if in scope, ErrScopeViolation if not.
func Validate(target string, scope ScopeDefinition) error {
	if len(scope.AllowedCIDRs) == 0 && len(scope.AllowedDomains) == 0 {
		return &apperrors.ScopeViolationError{
			Target: target,
			Scope:  "<empty>",
			Detail: "scope has no allowed CIDRs or domains defined",
		}
	}

	// Try parsing as CIDR first (e.g. "10.168.3.0/24")
	if _, ipNet, err := net.ParseCIDR(target); err == nil {
		return validateCIDR(ipNet, scope)
	}

	// Try parsing as URL first
	if strings.Contains(target, "://") {
		parsed, err := url.Parse(target)
		if err == nil {
			host := parsed.Hostname()
			return validateHost(host, scope)
		}
	}

	// Try as host:port
	if host, _, err := net.SplitHostPort(target); err == nil {
		return validateHost(host, scope)
	}

	// Plain IP or domain
	return validateHost(target, scope)
}

// validateCIDR checks whether a CIDR target is a subset of an allowed CIDR.
func validateCIDR(targetNet *net.IPNet, scope ScopeDefinition) error {
	for _, cidr := range scope.AllowedCIDRs {
		_, allowedNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		// If the target CIDR is a subset of (or equal to) an allowed CIDR, it's in scope.
		if allowedNet.Contains(targetNet.IP) {
			// Also verify the end of the target range is within the allowed range
			endIP := make(net.IP, len(targetNet.IP))
			for i := range targetNet.IP {
				endIP[i] = targetNet.IP[i] | ^targetNet.Mask[i]
			}
			if allowedNet.Contains(endIP) {
				return nil
			}
		}
	}

	return &apperrors.ScopeViolationError{
		Target: targetNet.String(),
		Scope:  strings.Join(scope.AllowedCIDRs, ", "),
		Detail: "CIDR is not within any allowed CIDR range",
	}
}

func validateHost(host string, scope ScopeDefinition) error {
	// Check if it's an IP
	ip := net.ParseIP(host)
	if ip != nil {
		return validateIP(ip, scope)
	}

	// It's a domain
	return validateDomain(host, scope)
}

func validateIP(ip net.IP, scope ScopeDefinition) error {
	// Check exclusions first
	for _, cidr := range scope.ExcludedCIDRs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return &apperrors.ScopeViolationError{
				Target: ip.String(),
				Scope:  cidr,
				Detail: "IP is in excluded CIDR range",
			}
		}
	}

	// Check allowed CIDRs
	for _, cidr := range scope.AllowedCIDRs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return nil // in scope
		}
	}

	// Check if IP resolves to an allowed domain — but we don't do reverse DNS
	// to avoid complexity. IP must be in an allowed CIDR.
	return &apperrors.ScopeViolationError{
		Target: ip.String(),
		Scope:  strings.Join(scope.AllowedCIDRs, ", "),
		Detail: "IP is not in any allowed CIDR range",
	}
}

func validateDomain(domain string, scope ScopeDefinition) error {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))

	for _, allowed := range scope.AllowedDomains {
		allowed = strings.ToLower(strings.TrimSuffix(allowed, "."))

		// Exact match
		if domain == allowed {
			return nil
		}

		// Wildcard: *.example.com matches sub.example.com
		if strings.HasPrefix(allowed, "*.") {
			suffix := allowed[1:] // .example.com
			if strings.HasSuffix(domain, suffix) {
				return nil
			}
		}

		// Subdomain match: if allowed is "example.com", also allow "sub.example.com"
		if strings.HasSuffix(domain, "."+allowed) {
			return nil
		}
	}

	return &apperrors.ScopeViolationError{
		Target: domain,
		Scope:  strings.Join(scope.AllowedDomains, ", "),
		Detail: "domain is not in any allowed domain scope",
	}
}

// ipAndDomainPattern matches IPs and domain-like strings in command text.
var ipAndDomainPattern = regexp.MustCompile(
	`(?:` +
		// IPv4
		`\b(?:\d{1,3}\.){3}\d{1,3}\b` +
		`|` +
		// Domain names (simplified but effective)
		`\b(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}\b` +
		`)`,
)

// ValidateCommand extracts all IPs and domains from a command string and validates
// each against the scope. Returns ErrScopeViolation if any target is out of scope.
// This is called before every command execution — no exceptions.
func ValidateCommand(cmd string, scope ScopeDefinition) error {
	matches := ipAndDomainPattern.FindAllString(cmd, -1)

	for _, match := range matches {
		// Skip common non-target strings
		if isCommonNonTarget(match) {
			continue
		}

		if err := Validate(match, scope); err != nil {
			return fmt.Errorf("command contains out-of-scope target: %w", err)
		}
	}

	return nil
}

// isCommonNonTarget filters out strings that look like domains/IPs but aren't targets.
func isCommonNonTarget(s string) bool {
	s = strings.ToLower(s)

	nonTargets := []string{
		"localhost",
		"127.0.0.1",
		"0.0.0.0",
		"github.com",
		"golang.org",
		"google.com",
		"api.github.com",
		"raw.githubusercontent.com",
		"huggingface.co",
	}

	for _, nt := range nonTargets {
		if s == nt {
			return true
		}
	}

	// Skip common file extensions that look like domains
	fileExtensions := []string{".yaml", ".yml", ".json", ".xml", ".txt", ".log", ".conf", ".cfg"}
	for _, ext := range fileExtensions {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}

	return false
}
