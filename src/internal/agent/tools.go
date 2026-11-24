package agent

import (
	"crevo-builder/internal/models"
)

// ToolFilterOptions controls which tools are exposed to the LLM for a given session.
type ToolFilterOptions struct {
	WorkspaceHasFiles bool
	FirebaseEnabled   bool
	StorageEnabled    bool
}

// GetToolsForSession returns a lean, session-scoped tool set that eliminates scan-trap
// and redundant tools (whose data is already inlined into the system prompt / messages)
// while providing high-throughput batch tools (readFiles, writeFiles) and multi-hunk editFile.
func GetToolsForSession(opts ToolFilterOptions) []models.ToolDefinition {
	tools := []models.ToolDefinition{
		WriteFilesTool(),
		CreateFileTool(),
		EditFileTool(),
		ReadFilesTool(),
		ReadFileTool(),
		ListFilesTool(),
		SearchFilesTool(),
		BatchEditFilesTool(),
		DeleteFileTool(),
		VerifyBuildTool(),
		WriteDesignIntelligenceTool(),
		UpdateSiteMemoryTool(),
		ListImagesTool(),
	}

	if !opts.WorkspaceHasFiles {
		tools = append(tools,
			FetchTemplatesTool(),
			GetTemplateInfoTool(),
			UseTemplateTool(),
		)
	}

	if opts.FirebaseEnabled {
		tools = append(tools, GetFirestoreCollectionsTool())
	}

	return tools
}

// GetTools returns all available tools for the AI (retained for backwards compatibility)
func GetTools() []models.ToolDefinition {
	return []models.ToolDefinition{
		CreateFileTool(),
		WriteFilesTool(),
		EditFileTool(),
		ReadFileTool(),
		ReadFilesTool(),
		ListFilesTool(),
		SearchFilesTool(),
		ListComponentsTool(),
		GetComponentUsageTool(),
		ListIconsTool(),
		GetIconUsageTool(),
		AnalyzeProjectTool(),
		VerifyBuildTool(),
		VerifyIntegrationTool(),
		FetchTemplatesTool(),
		GetTemplateInfoTool(),
		UseTemplateTool(),
		DiffPreviewTool(),
		BatchEditFilesTool(),
		CheckExistingComponentsTool(),
		GetFileInfoTool(),
		CreatePlanTool(),
		DeleteFileTool(),
		GetProjectCapabilitiesTool(),
		GetFirestoreCollectionsTool(),
		WriteDesignIntelligenceTool(),
		ReadDesignIntelligenceTool(),
		UpdateSiteMemoryTool(),
		ReadSiteMemoryTool(),
		GenerateAEOTool(),
		ListImagesTool(),
		GetImageUsageTool(),
	}
}

func WriteFilesTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "writeFiles",
		Description: `Create or overwrite multiple files in a single tool call. Parent directories are created automatically.

Best for: Initial website builds — write src/custom.css, modular components/data files, src/pages/Home.tsx, additional pages, and src/routes.tsx together in Turn 1.
Do NOT write protected system files (src/index.css, index.html, src/main.tsx, package.json, vite.config.ts, tsconfig.json, template.json).
For small surgical changes to an existing customized file, use editFile instead.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"files": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"path": map[string]interface{}{
								"type":        "string",
								"description": "The file path relative to project root (e.g., 'src/pages/Home.tsx')",
							},
							"content": map[string]interface{}{
								"type":        "string",
								"description": "The complete file content",
							},
						},
						"required": []string{"path", "content"},
					},
					"description": "List of files to create or overwrite in one call",
				},
			},
			"required": []string{"files"},
		},
	}
}

func ReadFilesTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "readFiles",
		Description: `Read up to 10 workspace files in parallel in a single tool call.

Use this on follow-up edits to read ONLY the specific pages or custom components you need to modify.
NEVER read boilerplate files already provided in the Workspace Reference (src/components/ui/*, src/index.css, src/main.tsx, src/App.tsx, src/components/ThemeToggle.tsx, package.json, vite.config.ts, tsconfig.json, template.json).`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"paths": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "string",
					},
					"description": "Relative file paths to read (max 10, e.g., ['src/pages/Home.tsx', 'src/custom.css'])",
				},
			},
			"required": []string{"paths"},
		},
	}
}

func CreateFileTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "createFile",
		Description: `Create or OVERWRITE a single file with specified content. WARNING: This replaces existing content entirely.

Key behaviors:
- Parent directories are created automatically.
- Prefer writeFiles when creating 2+ files in the same turn.
- Best for: NEW files, fresh starter Home.tsx replacement, or complete rewrites when editFile would require too many changes (>20 lines).
- WARNING: When the user asks for a small change (button fix, color change, text update) on an already-customized file, use editFile instead so existing design is preserved.

Returns: success confirmation with byte count and any import/content warnings.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "The file path relative to project root (e.g., 'src/components/Navbar.tsx')",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "The complete file content",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

func EditFileTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "editFile",
		Description: `Edit an existing file using EXACT search/replace. Supports either a single search/replace pair OR multiple sequential edits via the 'edits' array in one call.

Safety checks:
1. Search string must match EXACTLY including whitespace and newlines
2. If multiple matches are found, add more context OR set replaceAll=true
3. Large deletions (>50% of file) are blocked — use createFile for full rewrites

Best for: Targeted edits, className updates, adding/removing sections or imports, or applying multiple hunks to one file in a single call.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "The file path relative to project root",
				},
				"search": map[string]interface{}{
					"type":        "string",
					"description": "The EXACT text to find (use for single edit; omit when using 'edits')",
				},
				"replace": map[string]interface{}{
					"type":        "string",
					"description": "The text to replace the search text with (use for single edit; omit when using 'edits')",
				},
				"replaceAll": map[string]interface{}{
					"type":        "boolean",
					"description": "If true, replace all occurrences. Required if multiple matches exist.",
				},
				"edits": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"search": map[string]interface{}{
								"type":        "string",
								"description": "The EXACT text to find",
							},
							"replace": map[string]interface{}{
								"type":        "string",
								"description": "The replacement text",
							},
							"replaceAll": map[string]interface{}{
								"type":        "boolean",
								"description": "If true, replace all occurrences of this hunk",
							},
						},
						"required": []string{"search", "replace"},
					},
					"description": "Optional array of multiple search/replace hunks to apply sequentially to this file in one call",
				},
			},
			"required": []string{"path"},
		},
	}
}

func ReadFileTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "readFile",
		Description: "Read a single existing file before making targeted edits (use readFiles when reading 2+ files). NEVER read boilerplate files already provided in the Workspace Reference (src/components/ui/*, src/index.css, src/main.tsx, src/App.tsx, src/components/ThemeToggle.tsx, package.json, vite.config.ts, tsconfig.json, template.json).",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "The file path relative to project root",
				},
			},
			"required": []string{"path"},
		},
	}
}

func ListFilesTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "listFiles",
		Description: `List files and directories at a given path. Use this to explore project structure and verify files exist before reading them.

Note: node_modules/, dist/, and hidden files (.*) are automatically excluded from results.
Returns newline-separated list of paths. Directories end with /. Use '.' for root directory.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "The directory path relative to project root. Use '.' for root directory.",
				},
				"recursive": map[string]interface{}{
					"type":        "boolean",
					"description": "If true, list files recursively. Default is false.",
				},
			},
			"required": []string{"path"},
		},
	}
}

func SearchFilesTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "searchFiles",
		Description: `Search file contents using regex patterns. Searches these file types: .ts, .tsx, .js, .jsx, .json, .css, .scss, .html, .md, .txt, .yaml, .yml

Automatically excluded: node_modules/, dist/, hidden files (.*)
Returns matching lines with file paths and line numbers. Much faster than reading files one by one.

Examples:
- Find imports: "import.*Button"
- Find function definitions: "function\\s+handleSubmit"
- Find component usage: "<Header"
- Find TODO comments: "TODO|FIXME"`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{
					"type":        "string",
					"description": "Regex pattern to search for in file contents",
				},
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Subdirectory to search within (default: entire project). Use to narrow scope.",
				},
				"maxResults": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum matches to return (1-50, default: 20). Use lower values for broad patterns.",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func ListComponentsTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "listComponents",
		Description: `List all available shadcn/ui components from the component registry (30+ components).

Use this to discover polished, pre-built UI components before building custom ones. The registry includes buttons, cards, dialogs, forms, navigation, data display, and more.

Returns component names with brief descriptions. Use getComponentUsage to get import statements and code examples for any component.`,
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}
}

func GetComponentUsageTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "getComponentUsage",
		Description: `Get detailed usage info for a shadcn/ui component including the exact import statement, available variants, and a code example.

Use this before creating files that need polished UI elements like buttons, cards, dialogs, forms, etc.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"component": map[string]interface{}{
					"type":        "string",
					"description": "Component name (e.g., 'Button', 'Card', 'Dialog', 'Tabs')",
				},
			},
			"required": []string{"component"},
		},
	}
}

func ListIconsTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "listIcons",
		Description: `List available Lucide React icons by category or search. Use to find the right icon for navigation, actions, status indicators, etc.

Categories: Navigation, Actions, Status, Media, UI, Communication, Search, User, Commerce, Social

Returns icon names with brief descriptions. Use getIconUsage for import statements and examples.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"category": map[string]interface{}{
					"type":        "string",
					"description": "Category to list (e.g., 'Navigation', 'Actions'). Omit to see all categories.",
				},
				"search": map[string]interface{}{
					"type":        "string",
					"description": "Search term to find icons by name or tag (e.g., 'arrow', 'close')",
				},
			},
		},
	}
}

func GetIconUsageTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "getIconUsage",
		Description: `Get import statement and usage example for a Lucide React icon. Icons are imported from 'lucide-react'.

Sizing: Use className for size (e.g., className="h-5 w-5" or className="size-6")
Stroke: Use strokeWidth prop to adjust line thickness (default: 2)
Color: Use Tailwind text color classes (e.g., className="h-5 w-5 text-blue-500")

ACCESSIBILITY:
- For meaningful icons, add aria-label: <Check className="h-5 w-5" aria-label="Success" />
- For decorative icons, add aria-hidden="true"`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"icon": map[string]interface{}{
					"type":        "string",
					"description": "Icon name (e.g., 'ArrowLeftIcon', 'CheckIcon')",
				},
			},
			"required": []string{"icon"},
		},
	}
}

func AnalyzeProjectTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "analyzeProject",
		Description: `Analyze the existing project structure to understand what already exists before making changes.

IMPORTANT: For template-based projects, ALSO read template.json at the project root first. It contains structured metadata:
- available_pages: Pages included in the template
- custom_components: Layout, Navigation, etc.
- shadcn_components: Available UI components
- styling: Primary color, framework preferences

Returns:
- Existing pages and their layout settings (from routes.tsx)
- Components used in each page
- Current styling approach and color schemes
- Custom components in src/components/
- Architecture patterns in use

Use this BEFORE modifying files on an existing project. Combined with template.json, this gives you full context.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"focus": map[string]interface{}{
					"type":        "string",
					"description": "Optional focus area: 'pages', 'components', 'styling', 'all'. Default is 'all'.",
				},
			},
		},
	}
}

func VerifyBuildTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "verifyBuild",
		Description: `Run build verification and Vite bundling to check for compilation errors and generate the live preview bundle.

CRITICAL TIMING RULES:
- Call this tool ONLY ONCE at the very end of your task, after ALL page files, components, and routes have been created or modified.
- NEVER call verifyBuild in the middle of creating pages or after every single file change. Intermediate builds waste time and fail because files reference each other before all are written.
- Standard flow: Write/edit all website files (via writeFiles/createFile/editFile) -> Update routes if needed -> Run verifyBuild ONCE at the end.

Key behaviors:
- Compiles the project and verifies all imports and JSX syntax
- Generates the production dist/ bundle for the live preview
- Returns 'Build successful' or error messages in format 'file:line:column: error message'

IMPORTANT: If build fails:
1. READ the error message carefully (note file, line, column)
2. Use readFile to see the problematic code in context
3. FIX the specific issue (don't guess - understand the error)
4. Run verifyBuild again to verify the fix
5. Do NOT just retry without fixing - each retry wastes resources`,
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}
}

func VerifyIntegrationTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "verifyIntegration",
		Description: `Verify that created pages are properly imported and rendered. MUST use after creating pages in src/pages/.

Checks:
- Is the page imported in routes.tsx (or App.tsx)?
- Is the page added to the routes array or rendered in JSX?

Returns:
- "All pages are properly integrated" if everything is wired up
- List of issues (unimported, unused) with fix instructions

CRITICAL: Do NOT tell the user they can "browse to" or "navigate to" pages unless this tool confirms they are integrated.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"files": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "string",
					},
					"description": "List of page file paths to verify (e.g., ['src/pages/Home.tsx', 'src/pages/About.tsx'])",
				},
			},
			"required": []string{"files"},
		},
	}
}

func FetchTemplatesTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "fetchTemplates",
		Description: `Fetch available website templates from the template registry. Use this when the user's goal suggests a specific type of website (e.g., "travel site", "e-commerce store", "portfolio").

Returns a list of available templates with IDs, names, descriptions, and categories. Use getTemplateInfo to get detailed metadata about a specific template before using it.`,
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}
}

func GetTemplateInfoTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "getTemplateInfo",
		Description: `Get detailed metadata about a specific template including file structure, available pages, custom components, styling, and usage examples.

Use this after fetchTemplates to understand what a template contains before deciding to use it.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"template": map[string]interface{}{
					"type":        "string",
					"description": "Template ID (e.g., 'default', 'travel', 'ecommerce')",
				},
			},
			"required": []string{"template"},
		},
	}
}

func UseTemplateTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "useTemplate",
		Description: `Download and apply a specific template to the workspace. This replaces the current workspace files with the template's files.

Use this at the start of a new project to switch from the default template to a more specialized one (e.g., portfolio, e-commerce, landing page).

WARNING: This overwrites workspace files. Only use this BEFORE you have created or edited any files. Never use it after you have started building.

Returns confirmation when the template is downloaded and ready.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"template": map[string]interface{}{
					"type":        "string",
					"description": "Template ID to use (e.g., 'default', 'travel', 'ecommerce')",
				},
			},
			"required": []string{"template"},
		},
	}
}

func DiffPreviewTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "diffPreview",
		Description: `Preview file changes before applying them. Shows line-by-line diff with additions, deletions, and change percentage.

Use this BEFORE using createFile when you're unsure about the impact of your changes.

Returns:
- Line-by-line diff (added lines prefixed with '+', removed lines prefixed with '-')
- Total change percentage
- Warning if change exceeds 50% of file

This helps you catch accidental rewrites before they happen.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "File path relative to project root",
				},
				"oldContent": map[string]interface{}{
					"type":        "string",
					"description": "Current file content (use readFile first)",
				},
				"newContent": map[string]interface{}{
					"type":        "string",
					"description": "Proposed new file content",
				},
			},
			"required": []string{"path", "oldContent", "newContent"},
		},
	}
}

func BatchEditFilesTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "batchEditFiles",
		Description: `Apply the same search/replace change to multiple files at once.

Valid file types: .tsx, .ts, .jsx, .js, .css, .scss, .html
Protected files (automatically skipped): vite.config.ts, tsconfig.json, package.json, index.html, src/main.tsx, src/index.css

Efficient for:
- "Make all pages have vibrant backgrounds"
- "Update button color across all files"
- "Replace old class name with new one everywhere"

Returns list of modified files with replacement counts.

IMPORTANT: Performs actual file modifications across matched files. Run verifyBuild once after completing all edits.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{
					"type":        "string",
					"description": "Glob pattern for files to modify (e.g., 'src/pages/*.tsx', 'src/components/*.tsx'). Must be relative to project root.",
				},
				"search": map[string]interface{}{
					"type":        "string",
					"description": "Search string to find (can be plain text or regex). Must match exactly including whitespace.",
				},
				"replace": map[string]interface{}{
					"type":        "string",
					"description": "Replacement text.",
				},
				"replaceAll": map[string]interface{}{
					"type":        "boolean",
					"description": "If true, replace all occurrences in each file. If false (default), only first match per file.",
				},
				"maxFiles": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of files to modify (default: 10). Safety limit to prevent accidental massive changes.",
				},
			},
			"required": []string{"pattern", "search", "replace"},
		},
	}
}

func CheckExistingComponentsTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "checkExistingComponents",
		Description: `Check if component files already exist before creating them. Use this BEFORE creating a new component file.

Returns:
- List of existing custom components in src/components/
- Whether specific component names already exist
- Full paths for existing components

This prevents accidentally recreating components that already exist.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"componentNames": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "string",
					},
					"description": "Optional list of component names to check (e.g., ['PricingCard', 'Header', 'Footer']). If omitted, returns all existing components.",
				},
			},
		},
	}
}

func GetFileInfoTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "getFileInfo",
		Description: `Get metadata and structure information about a file before editing.

Returns:
- File size and line count
- Last modification time
- File type (page, component, style, config)
- Import statements
- Components used (for .tsx files)
- Layout setting (if in routes.tsx)

Use this to understand a file before making changes to it.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "File path relative to project root",
				},
			},
			"required": []string{"path"},
		},
	}
}

func CreatePlanTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "createPlan",
		Description: `Create a structured plan that shows the user what you're about to build BEFORE you start.

**WHY USE THIS**: Users see the plan immediately and can adjust requirements early - this prevents wasted work and builds trust.

**MANDATORY - Use createPlan when:**
- Creating 2 or more pages
- Modifying 3 or more files
- Building a full website from scratch
- User request is vague (e.g., "build me a portfolio", "create a landing page")
- Adding multiple sections at once
- Any task where implementation approach isn't obvious

**Skip planning only for:**
- Single file edits with clear instructions
- Adding ONE section to existing page
- Quick fixes with obvious implementation

After creating the plan, proceed to execute it step-by-step. The user will see what you're building.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"summary": map[string]interface{}{
					"type":        "string",
					"description": "Brief summary of what the plan accomplishes (1-2 sentences)",
				},
				"steps": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"file": map[string]interface{}{
								"type":        "string",
								"description": "File path relative to project root",
							},
							"action": map[string]interface{}{
								"type":        "string",
								"enum":        []string{"create", "modify", "delete"},
								"description": "Action to perform on the file",
							},
							"description": map[string]interface{}{
								"type":        "string",
								"description": "What changes will be made to this file",
							},
						},
						"required": []string{"file", "action", "description"},
					},
				},
				"dependencies": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Files that must be created/modified before others",
				},
				"risks": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Potential risks or things to watch out for",
				},
			},
			"required": []string{"summary", "steps"},
		},
	}
}

func DeleteFileTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "deleteFile",
		Description: `Delete a file from the project. Use this to clean up unwanted, duplicate, or obsolete files.

**PROTECTED FILES - Cannot be deleted:**
- src/routes.tsx, src/main.tsx, src/index.tsx (routing/entry points)
- package.json, tsconfig.json, index.html (project config)
- vite.config.ts/js, tailwind.config.ts/js, postcss.config.js (build config)
- template.json (template metadata)
- Any file ending in .config.js, .config.ts, .config.json

**When to use:**
- Removing duplicate or obsolete components
- Cleaning up failed/partial file creations
- Removing files that shouldn't exist
- Consolidating files after refactoring

Returns success confirmation or error if file is protected/doesn't exist.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "File path relative to project root (e.g., 'src/pages/OldPage.tsx')",
				},
				"reason": map[string]interface{}{
					"type":        "string",
					"description": "Brief reason for deletion (helps with audit trail)",
				},
			},
			"required": []string{"path"},
		},
	}
}

func GetProjectCapabilitiesTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "getProjectCapabilities",
		Description: `Check what dynamic features are available for this project.

**IMPORTANT: Call this FIRST when user requests features that need:**
- Data persistence (tasks, notes, user data, saving anything)
- User authentication (login, signup, user accounts)
- File uploads (images, documents, media)
- Real-time updates or live data sync

**Returns:**
- firebase.enabled: Whether Firebase/Firestore is available for data storage
- firebase.collection_prefix: The prefix for Firestore collections (use this for data isolation)
- storage.enabled: Whether file uploads are available
- storage.max_file_size_mb: Maximum upload size in MB

**What to do with the results:**
- If a capability is ENABLED: Use the pre-built hooks from the template (useFirestore, useSystemStorage)
- If a capability is DISABLED: Tell the user their plan doesn't include this feature and suggest they upgrade or check project settings

**DO NOT generate Firebase or storage code if the capability is disabled - it won't work.**`,
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}
}

// GetFirestoreCollectionsTool returns the tool definition for listing Firestore collections
func GetFirestoreCollectionsTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "getFirestoreCollections",
		Description: `List existing Firestore collections for this project to discover existing data structures.

**IMPORTANT: Call this tool EARLY in your session when Firebase is enabled.**

**WHEN TO USE:**
- At the START of any session where firebase.enabled = true (from getProjectCapabilities)
- Before creating or modifying any code that uses Firestore
- When user mentions existing data ("show my tasks", "display saved items")

**RETURNS:**
- success: Whether the query succeeded
- collections: Array of existing collections, each with:
  - name: Collection name (e.g., "transactions", "tasks")
  - document_count: Number of documents (or "100+" for large collections)
  - sample_fields: Field names from a sample document
- collection_prefix: The project's namespace (e.g., "projects/abc-123")
- error/message: Error info if failed

**CRITICAL:**
- If collections exist, REUSE their names - do NOT create new collection names
- Match your TypeScript interfaces to the sample_fields
- If collections are empty, you can create new names

**DO NOT call if getProjectCapabilities shows firebase.enabled = false**`,
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}
}

// WriteDesignIntelligenceTool defines the tool for persisting design decisions
func WriteDesignIntelligenceTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "writeDesignIntelligence",
		Description: `Persist design decisions to maintain visual consistency across sessions.

Call this AFTER making significant design choices (initial site generation, redesign, theme changes).
Data is deep-merged — existing keys are preserved unless explicitly overwritten.

**Categories to document (be SPECIFIC and OPINIONATED, not vague):**
- visual_personality: Emotional feel of the design (e.g., "Warm, artisanal, inviting with editorial confidence")
- colors: Palette strategy with actual values (e.g., "Earth tones with terracotta accent #E07C5C, tinted backgrounds bg-orange-50")
- typography: Font choices and why (e.g., "Serif headings for editorial feel, sans-serif body, dramatic clamp() scale")
- spacing: Spacing philosophy (e.g., "Generous py-24 section padding, asymmetric hero py-32/py-16, gap-8 grids")
- layout: Patterns used (e.g., "Full-width hero, alternating light/dark sections, 3-column feature grids")
- component_vocabulary: Component styling decisions (e.g., "Rounded-xl cards with shadow-lg, pill badges, ghost buttons with border")
- animations: Motion approach (e.g., "Subtle hover lifts, staggered fade-in on scroll, 300ms transitions")
- image_direction: Image selection rationale (e.g., "Warm-tone gallery images, dark-text backgrounds for hero contrast")
- anti_patterns: What this design deliberately avoids (e.g., "No pure black, no symmetric padding, no default hamburger menu")

**BAD example (too vague, useless for consistency):**
  { "colors": "blue theme", "spacing": "normal" }

**GOOD example (specific, maintains consistency):**
  { "visual_personality": "Clean, minimal SaaS with confident whitespace",
    "colors": { "strategy": "Indigo-600 primary, slate-100 backgrounds, emerald-500 success accents" },
    "component_vocabulary": "Rounded-lg buttons with shadow-sm, bordered cards, pill-shaped badges" }`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"data": map[string]interface{}{
					"type":        "object",
					"description": "Design decisions to persist. Keys are categories (colors, typography, spacing, animations, layout, anti_patterns). Values are objects with specific decisions.",
				},
			},
			"required": []string{"data"},
		},
	}
}

// ReadDesignIntelligenceTool defines the tool for reading persisted design decisions
func ReadDesignIntelligenceTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "readDesignIntelligence",
		Description: `Read persisted design decisions from previous sessions.

Call this at the START of every continuation session, BEFORE making any design changes.
Returns the design intelligence JSON with all recorded decisions.
If empty, this is a new project with no recorded design decisions.

Use the returned data to maintain visual consistency:
- Follow the recorded color palette
- Use the same typography choices
- Maintain spacing patterns
- Avoid listed anti-patterns`,
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}
}

// UpdateSiteMemoryTool defines the tool for persisting business facts
func UpdateSiteMemoryTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "updateSiteMemory",
		Description: `Persist business facts learned from the user to maintain context across sessions.

Call this IMMEDIATELY when the user shares business information — BEFORE generating code.
Data is deep-merged — existing facts are preserved unless explicitly overwritten.

**Categories to capture:**
- business: name, industry, description, tagline, founded year
- products: services, specialties, price range, bestsellers
- locations: addresses, hours, phone numbers
- audience: target demographics, needs, preferences
- brand: tone of voice, values, differentiators
- contact: email, phone, social media handles

**Confidence levels — CRITICAL:**
Mark each fact with how you learned it:
- "confidence": "stated" — user explicitly said this (e.g., "Our email is hello@bloom.com")
- "confidence": "inferred" — you deduced this from context (e.g., user mentioned "our cafe" → likely food/beverage industry)

**Anti-fabrication rule:**
NEVER make up contact information (phone, email, address, social media).
If the user hasn't provided contact details, do NOT store placeholder values.
Only store facts the user explicitly shared or that you can reasonably infer.

**Example:**
updateSiteMemory({ data: {
  "business": { "name": "Bloom & Brew", "industry": "Coffee shop", "confidence": "stated" },
  "audience": { "target": "Young professionals", "confidence": "inferred" },
  "contact": { "email": "hello@bloomandbrew.com", "confidence": "stated" }
}})`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"data": map[string]interface{}{
					"type":        "object",
					"description": "Business facts to persist. Keys are categories (business, products, locations, audience, brand, contact). Values are objects with specific facts.",
				},
			},
			"required": []string{"data"},
		},
	}
}

// ReadSiteMemoryTool defines the tool for reading persisted business facts
func ReadSiteMemoryTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "readSiteMemory",
		Description: `Read persisted business facts from previous sessions.

Call this at the START of every continuation session to load all known business context.
Returns the site memory JSON with all recorded business facts.
If empty, no business facts have been recorded yet.

Use the returned data to:
- Reference correct business name, products, locations
- Write accurate copy that matches the brand tone
- Include real contact information instead of placeholders`,
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}
}

// GenerateAEOTool defines the tool for generating Answer Engine Optimization assets
func GenerateAEOTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "generateAEO",
		Description: `Generate Answer Engine Optimization (AEO) assets to make the site discoverable by AI search engines.

Creates three assets:
1. **public/llms.txt** — Plain text site summary for LLMs (like robots.txt but for AI models)
2. **public/robots.txt** — Standard crawler directives with explicit AI bot allow rules
3. **JSON-LD in index.html** — Schema.org structured data for Organization/WebSite

Reads from memory.json (if available) for business context, and scans src/pages/ for page list.

**When to call:**
- After a successful verifyBuild on a project that has memory.json with business facts
- Do this automatically after completing a site generation — no need to ask the user
- Skip if memory.json is empty (no business context to generate from)

The generated files make the site discoverable by Perplexity, ChatGPT, Claude, and other AI search tools.`,
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}
}

// ListImagesTool defines the tool for searching stock images
func ListImagesTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "listImages",
		Description: `Search the stock image library for professional photos to use in the website.

Returns matching images with URLs, categories, tone, and mood metadata.
Use these instead of placeholder images or external hotlinks.

**Selection rules:**
- Match image tone to the site color scheme (warm site → warm images)
- Use dark-text contrast images for light backgrounds, light-text for dark backgrounds
- Select 5-8 DISTINCT images across hero, service/product cards, gallery, and testimonials — NEVER reuse the same URL twice
- When using /api/images/stock?q=...&type=gallery&index=N, pass a unique descriptive q and increment index=0,1,2,3... per card
- Always add descriptive alt text derived from the image subject
- User-uploaded images always take priority over library images

**Available categories:**
- Backgrounds: texture, gradient, abstract, atmosphere
- Gallery: food-bakery, food-coffee, architecture, craft-artisan, wellness, interior, hospitality, wine, creative-art, professional, beauty-retail, salon, industrial, nature, abstract`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"type": map[string]interface{}{
					"type":        "string",
					"description": "Filter by image type: 'background' or 'gallery'. Omit for both.",
					"enum":        []string{"background", "gallery"},
				},
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Free-form search keywords or topic (e.g., 'dentist', 'gym fitness', 'lawyer', 'auto repair', 'pet clinic', 'modern tech saas')",
				},
				"category": map[string]interface{}{
					"type":        "string",
					"description": "Filter by category or industry (e.g., 'dentist', 'fitness', 'law', 'automotive', 'pets', 'food', 'texture')",
				},
				"mood": map[string]interface{}{
					"type":        "string",
					"description": "Filter by mood (backgrounds only): warm, cool, earthy, moody, soft, bold, playful, ethereal, neutral",
				},
				"tone": map[string]interface{}{
					"type":        "string",
					"description": "Filter by tone: light, dark, warm, cool",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Max results to return (default 10, max 30)",
				},
			},
		},
	}
}

// GetImageUsageTool defines the tool for getting usage info for a specific image
func GetImageUsageTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "getImageUsage",
		Description: `Get detailed usage information for a specific stock image.

Returns the image URL, recommended JSX code for usage, alt text suggestion, and metadata.
Provides ready-to-use code snippets for both background and gallery images.`,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"filename": map[string]interface{}{
					"type":        "string",
					"description": "The exact filename from listImages results (e.g., 'bg_sand-dunes_texture_earthy_warm_dark-text.png')",
				},
			},
			"required": []string{"filename"},
		},
	}
}

