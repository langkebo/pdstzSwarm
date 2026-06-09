package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/skills"
	"github.com/spf13/cobra"
)

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Browse and search community security skills",
	Long: `Browse, search, and inspect AI Agent security skills curated from the
openclaw-sec-skills community index. Skills are organized into 8 security
domains and provide reusable prompt templates for security analysis.`,
}

var skillsListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List skills grouped by category",
	Example: "  pentestswarm skills list",
	RunE: func(cmd *cobra.Command, args []string) error {
		reg := skills.Global()
		cat, _ := cmd.Flags().GetString("category")
		limit, _ := cmd.Flags().GetInt("limit")

		if cat != "" {
			return listSkillsByCategory(reg, skills.ParseCategory(cat), limit)
		}
		listAllCategories(reg, limit)
		return nil
	},
}

var skillsSearchCmd = &cobra.Command{
	Use:     "search <query>",
	Short:   "Search skills by name, description, or tags",
	Args:    cobra.ExactArgs(1),
	Example: "  pentestswarm skills search sql injection",
	RunE: func(cmd *cobra.Command, args []string) error {
		reg := skills.Global()
		query := args[0]
		limit, _ := cmd.Flags().GetInt("limit")

		results := reg.Search(query)
		if len(results) == 0 {
			fmt.Printf("  %s No skills matching %q\n", colorDim("*"), query)
			return nil
		}

		max := len(results)
		if limit > 0 && limit < max {
			max = limit
		}

		fmt.Printf("  %s Found %d skill(s) for %q\n\n", colorCyan("*"), len(results), colorBold(query))
		fmt.Printf("  %-8s %-35s %s\n", colorDim("CATEGORY"), colorDim("NAME"), colorDim("SOURCE"))
		fmt.Println(colorDim("  ────────────────────────────────────────────────────────────────────────────────"))
		for i := 0; i < max; i++ {
			s := results[i]
			owner, repo := s.OwnerRepo()
			source := s.SourceLabel
			if source == "" && owner != "" {
				source = owner + "/" + repo
			}
			if len(source) > 35 {
				source = source[:32] + "..."
			}
			name := s.Name
			if len(name) > 34 {
				name = name[:31] + "..."
			}
			fmt.Printf("  %-8s %-35s %s\n", colorDim(string(s.Category)), colorBold(name), colorDim(source))
		}
		if max < len(results) {
			fmt.Printf("\n  %s ... and %d more (use --limit to adjust)\n", colorDim("*"), len(results)-max)
		}
		return nil
	},
}

var skillsInfoCmd = &cobra.Command{
	Use:     "info <name>",
	Short:   "Show detailed information about a skill",
	Args:    cobra.ExactArgs(1),
	Example: "  pentestswarm skills info sast-skills",
	RunE: func(cmd *cobra.Command, args []string) error {
		reg := skills.Global()
		s := reg.ByName(args[0])
		if s == nil {
			return fmt.Errorf("skill %q not found", args[0])
		}

		owner, repo := s.OwnerRepo()

		fmt.Printf("\n  %s %s\n", colorBold("Name:"), s.Name)
		fmt.Printf("  %s %s %s\n", colorBold("Category:"), s.Category.Emoji(), s.Category.DisplayName())
		fmt.Printf("  %s %s\n", colorBold("Description:"), s.Description)
		if s.Source != "" {
			fmt.Printf("  %s %s\n", colorBold("Source:"), colorCyan(s.Source))
		}
		if s.SourceLabel != "" {
			fmt.Printf("  %s %s\n", colorBold("Label:"), s.SourceLabel)
		}
		if owner != "" {
			fmt.Printf("  %s %s/%s\n", colorBold("Repository:"), owner, repo)
		}
		if s.SourceType != "" {
			fmt.Printf("  %s %s\n", colorBold("Type:"), s.SourceType)
		}
		tags := s.Tags()
		if len(tags) > 0 {
			fmt.Printf("  %s %s\n", colorBold("Tags:"), strings.Join(tags, ", "))
		}
		fmt.Println()
		return nil
	},
}

var skillsStatsCmd = &cobra.Command{
	Use:     "stats",
	Short:   "Show skill category statistics",
	Example: "  pentestswarm skills stats",
	RunE: func(cmd *cobra.Command, args []string) error {
		reg := skills.Global()
		stats := reg.CategoryStats()

		fmt.Printf("\n  %s %d skills across %d categories\n\n", colorBold("Total:"), reg.Len(), len(stats))
		fmt.Printf("  %-8s %6s  %s\n", colorDim("EMOJI"), colorDim("COUNT"), colorDim("CATEGORY"))
		fmt.Println(colorDim("  ──────────────────────────────"))
		for _, cat := range skills.AllCategories() {
			count := stats[cat]
			if count == 0 {
				continue
			}
			bar := strings.Repeat("█", count/5)
			if count%5 > 0 {
				bar += "▌"
			}
			fmt.Printf("   %s   %3d  %-12s %s\n", cat.Emoji(), count, cat.DisplayName(), colorCyan(bar))
		}
		fmt.Println()
		return nil
	},
}

var skillsSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Fetch latest skill index from upstream",
	Long: `Fetch the latest skill index from the openclaw-sec-skills repository
and save it as a local JSON cache. Use --output to specify a custom
cache path (default: ./data/openclaw-skills.json).

The embedded skill index shipped with the binary is not modified.
To use the updated cache at runtime, set the environment variable
PENTESTSWARM_SKILLS_CACHE to point to the cache file.`,
	Example: "  pentestswarm skills sync",
	RunE: func(cmd *cobra.Command, args []string) error {
		output, _ := cmd.Flags().GetString("output")
		if output == "" {
			output = filepath.Join("data", "openclaw-skills.json")
		}

		cfg := skills.DefaultSyncConfig()
		if cache := os.Getenv("PENTESTSWARM_SKILLS_CACHE"); cache != "" {
			cfg.CachePath = cache
		}

		fmt.Printf("  %s Fetching skill index from upstream...\n", colorCyan("*"))
		skillList, err := skills.SyncFromURL(cfg)
		if err != nil {
			return fmt.Errorf("sync failed: %w", err)
		}

		// Write to output path.
		dir := filepath.Dir(output)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
		data, err := json.MarshalIndent(skillList, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal skills: %w", err)
		}
		if err := os.WriteFile(output, data, 0644); err != nil {
			return fmt.Errorf("write cache: %w", err)
		}

		// Count by category.
		catCount := map[skills.Category]int{}
		for _, s := range skillList {
			catCount[s.Category]++
		}

		fmt.Printf("  %s Synced %d skills to %s\n\n", colorGreen("*"), len(skillList), output)
		for _, cat := range skills.AllCategories() {
			if n := catCount[cat]; n > 0 {
				fmt.Printf("    %s %s: %d\n", cat.Emoji(), cat.DisplayName(), n)
			}
		}
		fmt.Printf("\n  %s Set PENTESTSWARM_SKILLS_CACHE=%s to use this cache at runtime\n",
			colorDim("*"), output)
		return nil
	},
}

// listAllCategories prints all skills grouped by category.
func listAllCategories(reg *skills.Registry, limit int) {
	stats := reg.CategoryStats()

	fmt.Printf("\n  %s %d skills across %d categories\n\n", colorBold("Total:"), reg.Len(), len(stats))
	for _, cat := range skills.AllCategories() {
		skills := reg.ByCategory(cat)
		if len(skills) == 0 {
			continue
		}
		fmt.Printf("  %s %s (%d)\n", cat.Emoji(), colorBold(cat.DisplayName()), len(skills))
		max := len(skills)
		if limit > 0 && limit < max {
			max = limit
		}
		for i := 0; i < max; i++ {
			s := skills[i]
			desc := s.Description
			if len(desc) > 60 {
				desc = desc[:57] + "..."
			}
			fmt.Printf("    %s %s\n", colorDim("•"), colorDim(desc))
		}
		if max < len(skills) {
			fmt.Printf("    %s ... and %d more\n", colorDim("•"), len(skills)-max)
		}
		fmt.Println()
	}
}

// listSkillsByCategory prints skills for a single category.
func listSkillsByCategory(reg *skills.Registry, cat skills.Category, limit int) error {
	if cat == "" {
		return fmt.Errorf("unknown category — use one of: %s",
			strings.Join(categoryNames(), ", "))
	}
	skillsList := reg.ByCategory(cat)
	if len(skillsList) == 0 {
		fmt.Printf("  %s No skills in category %s\n", colorDim("*"), cat.DisplayName())
		return nil
	}

	fmt.Printf("\n  %s %s (%d skills)\n\n", cat.Emoji(), colorBold(cat.DisplayName()), len(skillsList))
	max := len(skillsList)
	if limit > 0 && limit < max {
		max = limit
	}
	for i := 0; i < max; i++ {
		s := skillsList[i]
		owner, repo := s.OwnerRepo()
		source := owner + "/" + repo
		if owner == "" {
			source = s.SourceLabel
		}
		fmt.Printf("  %-30s %s\n", colorBold(s.Name), colorDim(source))
	}
	return nil
}

func categoryNames() []string {
	var names []string
	for _, c := range skills.AllCategories() {
		names = append(names, string(c))
	}
	return names
}

func init() {
	skillsListCmd.Flags().StringP("category", "c", "", "filter by category")
	skillsListCmd.Flags().IntP("limit", "n", 5, "max skills per category")

	skillsSearchCmd.Flags().IntP("limit", "n", 20, "max results")

	skillsSyncCmd.Flags().StringP("output", "o", "", "cache output path (default: ./data/openclaw-skills.json)")

	skillsCmd.AddCommand(skillsListCmd)
	skillsCmd.AddCommand(skillsSearchCmd)
	skillsCmd.AddCommand(skillsInfoCmd)
	skillsCmd.AddCommand(skillsStatsCmd)
	skillsCmd.AddCommand(skillsSyncCmd)

	rootCmd.AddCommand(skillsCmd)
}