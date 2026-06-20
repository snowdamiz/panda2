package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/llm"
)

type RedactionPolicy string

const (
	RedactNone    RedactionPolicy = "none"
	RedactSecrets RedactionPolicy = "secrets"
	RedactContent RedactionPolicy = "content"
)

type AuditPolicy string

const (
	AuditNone      AuditPolicy = "none"
	AuditOnUse     AuditPolicy = "on_use"
	AuditSensitive AuditPolicy = "sensitive"
)

type Definition struct {
	Name                  string
	Description           string
	RequiredPermission    string
	InputSchema           json.RawMessage
	OutputSchema          json.RawMessage
	Timeout               time.Duration
	Redaction             RedactionPolicy
	Audit                 AuditPolicy
	IncludeInModelContext bool
}

type Registry struct {
	definitions map[string]Definition
}

var ErrUnknownTool = errors.New("unknown tool")

func NewRegistry(definitions ...Definition) (*Registry, error) {
	registry := &Registry{definitions: map[string]Definition{}}
	for _, definition := range definitions {
		if err := registry.Register(definition); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func NewDefaultRegistry() (*Registry, error) {
	return NewRegistry(DefaultDefinitions()...)
}

func (r *Registry) Register(definition Definition) error {
	if definition.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if definition.RequiredPermission == "" {
		return fmt.Errorf("tool %s requires a permission", definition.Name)
	}
	if len(definition.InputSchema) == 0 || !json.Valid(definition.InputSchema) {
		return fmt.Errorf("tool %s input schema must be valid JSON", definition.Name)
	}
	if len(definition.OutputSchema) == 0 || !json.Valid(definition.OutputSchema) {
		return fmt.Errorf("tool %s output schema must be valid JSON", definition.Name)
	}
	if definition.Timeout <= 0 {
		return fmt.Errorf("tool %s requires a positive timeout", definition.Name)
	}
	if _, exists := r.definitions[definition.Name]; exists {
		return fmt.Errorf("tool %s already registered", definition.Name)
	}
	r.definitions[definition.Name] = definition
	return nil
}

func (r *Registry) Get(name string) (Definition, bool) {
	definition, ok := r.definitions[name]
	return definition, ok
}

func (r *Registry) MustGet(name string) (Definition, error) {
	definition, ok := r.Get(name)
	if !ok {
		return Definition{}, ErrUnknownTool
	}
	return definition, nil
}

func (r *Registry) Definitions() []Definition {
	definitions := make([]Definition, 0, len(r.definitions))
	for _, definition := range r.definitions {
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool {
		return definitions[i].Name < definitions[j].Name
	})
	return definitions
}

func (r *Registry) OpenRouterTools(permissions map[string]struct{}) []llm.Tool {
	var result []llm.Tool
	for _, definition := range r.Definitions() {
		if _, ok := permissions[definition.RequiredPermission]; !ok {
			continue
		}
		result = append(result, llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.InputSchema,
			},
		})
	}
	return result
}

func DefaultDefinitions() []Definition {
	return []Definition{
		{
			Name:                  "fetch_recent_messages",
			Description:           "Fetch recent Discord channel or thread messages within configured privacy limits.",
			RequiredPermission:    admin.PermissionAssistantUse,
			InputSchema:           objectSchema("channel_id", "limit"),
			OutputSchema:          objectSchema("messages"),
			Timeout:               3 * time.Second,
			Redaction:             RedactContent,
			Audit:                 AuditSensitive,
			IncludeInModelContext: true,
		},
		{
			Name:                  "fetch_message",
			Description:           "Fetch one referenced Discord message for context or citation.",
			RequiredPermission:    admin.PermissionAssistantUse,
			InputSchema:           objectSchema("channel_id", "message_id"),
			OutputSchema:          objectSchema("message"),
			Timeout:               2 * time.Second,
			Redaction:             RedactContent,
			Audit:                 AuditSensitive,
			IncludeInModelContext: true,
		},
		{
			Name:                  "search_knowledge",
			Description:           "Search admin-managed guild knowledge.",
			RequiredPermission:    admin.PermissionAssistantMemoryRead,
			InputSchema:           objectSchema("query", "limit"),
			OutputSchema:          objectSchema("results"),
			Timeout:               2 * time.Second,
			Redaction:             RedactSecrets,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
		},
		{
			Name:                  "summarize_text_file",
			Description:           "Summarize extracted text from a safe uploaded file.",
			RequiredPermission:    admin.PermissionAssistantAttachments,
			InputSchema:           objectSchema("attachment_id", "detail"),
			OutputSchema:          objectSchema("summary"),
			Timeout:               10 * time.Second,
			Redaction:             RedactContent,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
		},
		{
			Name:                  "draft_moderator_note",
			Description:           "Create a non-destructive draft moderator note from provided context.",
			RequiredPermission:    "moderation.use",
			InputSchema:           objectSchema("context", "tone"),
			OutputSchema:          objectSchema("draft"),
			Timeout:               5 * time.Second,
			Redaction:             RedactSecrets,
			Audit:                 AuditOnUse,
			IncludeInModelContext: false,
		},
		{
			Name:                  "read_config",
			Description:           "Read bot configuration visible to the current user.",
			RequiredPermission:    admin.PermissionAdminConfigRead,
			InputSchema:           objectSchema("guild_id"),
			OutputSchema:          objectSchema("config"),
			Timeout:               time.Second,
			Redaction:             RedactSecrets,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
		},
		{
			Name:                  "generate_workflow_json",
			Description:           "Generate structured JSON for command workflows without taking action.",
			RequiredPermission:    admin.PermissionAssistantUse,
			InputSchema:           objectSchema("workflow", "inputs"),
			OutputSchema:          objectSchema("json"),
			Timeout:               2 * time.Second,
			Redaction:             RedactSecrets,
			Audit:                 AuditNone,
			IncludeInModelContext: true,
		},
	}
}

func objectSchema(required ...string) json.RawMessage {
	properties := map[string]any{}
	for _, name := range required {
		properties[name] = map[string]string{"type": "string"}
	}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             required,
	}
	data, _ := json.Marshal(schema)
	return data
}
