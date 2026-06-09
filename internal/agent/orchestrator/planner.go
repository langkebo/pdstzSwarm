package orchestrator

import "github.com/Armur-Ai/Pentest-Swarm-AI/internal/skills"

// Milestone is a checkpoint the orchestrator tracks progress against.
type Milestone struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Completed   bool   `json:"completed"`
}

// PlanResult bundles milestones with recommended skill categories.
type PlanResult struct {
	Milestones          []Milestone
	SkillCategories     []skills.Category // suggested categories for skill injection
	RecommendedSkills   string            // pre-rendered skill block, or empty
}

// Planner decomposes campaign objectives into milestones.
type Planner struct{
	injector *skills.Injector
}

// NewPlanner creates a new campaign planner.
func NewPlanner() *Planner {
	return &Planner{injector: skills.NewInjector(12)}
}

// NewPlannerWithInjector creates a planner backed by a custom skill injector.
func NewPlannerWithInjector(inj *skills.Injector) *Planner {
	return &Planner{injector: inj}
}

// DecomposeObjective breaks an objective into ordered milestones.
// Use DecomposeWithSkills for skill-aware planning.
func (p *Planner) DecomposeObjective(objective string) []Milestone {
	return p.DecomposeWithSkills(objective).Milestones
}

// DecomposeWithSkills returns milestones plus recommended skill
// categories and a pre-rendered skill block.
func (p *Planner) DecomposeWithSkills(objective string) PlanResult {
	result := PlanResult{
		Milestones: planMilestones(objective),
	}
	if p.injector != nil {
		recs := p.injector.ForObjective(objective)
		result.RecommendedSkills = skills.MustInject(recs)
		// Extract unique categories from recommendations.
		seen := map[skills.Category]bool{}
		for _, r := range recs {
			seen[r.Skill.Category] = true
		}
		for c := range seen {
			result.SkillCategories = append(result.SkillCategories, c)
		}
	}
	return result
}

// planMilestones is the core milestone decomposition logic.
func planMilestones(objective string) []Milestone {
	lower := toLower(objective)

	switch {
	case containsIgnoreCase(lower, "rce") || containsIgnoreCase(lower, "remote code"):
		return []Milestone{
			{Name: "recon_complete", Description: "Full attack surface discovered"},
			{Name: "rce_candidates_identified", Description: "Potential RCE findings classified"},
			{Name: "rce_exploited_or_exhausted", Description: "RCE exploitation attempted on all candidates"},
			{Name: "report_generated", Description: "Professional report generated"},
		}

	case containsIgnoreCase(lower, "bug bounty"):
		return []Milestone{
			{Name: "scope_loaded", Description: "Program scope imported from platform"},
			{Name: "recon_complete", Description: "Full attack surface discovered"},
			{Name: "findings_classified", Description: "All findings classified with CVE/CVSS"},
			{Name: "duplicates_checked", Description: "Duplicate detection completed"},
			{Name: "report_formatted", Description: "Bug bounty compliant report generated"},
		}

	case containsIgnoreCase(lower, "ctf") || containsIgnoreCase(lower, "flag"):
		return []Milestone{
			{Name: "recon_complete", Description: "Machine enumerated"},
			{Name: "initial_foothold", Description: "Initial access achieved"},
			{Name: "user_flag", Description: "User flag captured"},
			{Name: "privilege_escalation", Description: "Root/admin access achieved"},
			{Name: "root_flag", Description: "Root flag captured"},
		}

	default:
		// General "find all vulnerabilities"
		return []Milestone{
			{Name: "recon_complete", Description: "Full attack surface discovered"},
			{Name: "findings_classified", Description: "All findings classified and scored"},
			{Name: "exploitation_attempted", Description: "Top attack chains tested"},
			{Name: "report_generated", Description: "Professional report generated"},
		}
	}
}
