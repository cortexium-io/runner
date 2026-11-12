package guidesmedia

// CodePathAllowed is a closed media-only source surface, not a directory-wide
// frontend allowance. Independent source review still owns semantic approval.
func CodePathAllowed(name string) bool {
	switch name {
	case "frontend/src/components/guides/LearningWalkthrough.vue",
		"frontend/src/components/guides/LearningWalkthrough.test.ts",
		"frontend/src/components/guides/GuidePage.vue",
		"frontend/src/components/guides/guides.css",
		"frontend/src/components/guides/walkthrough-media.json",
		"frontend/src/components/guides/GuideMediaControls.vue",
		"frontend/src/components/guides/GuideMediaControls.test.ts",
		"frontend/src/components/guides/GuideMediaTranscript.vue",
		"frontend/src/components/guides/GuideMediaTranscript.test.ts",
		"frontend/src/components/guides/guide-media.test.ts",
		"frontend/scripts/guide-media.test.ts",
		"docs/printed-learning.md":
		return true
	}
	for game, component := range map[string]string{
		"sudoku": "Sudoku", "binary": "Binary", "suguru": "Suguru",
		"nonogram": "Nonogram", "futoshiki": "Futoshiki", "shikaku": "Shikaku",
		"hashi": "Hashi", "calcudoku": "Calcudoku", "kakuro": "Kakuro",
	} {
		if name == "frontend/src/pages/how-to-play/"+game+"/HowToPlay"+component+"Page.vue" {
			return true
		}
	}
	return false
}
