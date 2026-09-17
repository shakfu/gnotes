package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// tool is one entry in the tools/list response.
type tool struct {
	Name        string       `json:"name"`
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description"`
	InputSchema object       `json:"inputSchema"`
	Annotations *annotations `json:"annotations,omitempty"`
}

// annotations are hints about a tool's effects. Clients use them to decide what
// to confirm with the user, so they describe consequences honestly: nothing
// here is marked read-only unless it truly writes nothing.
type annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  bool   `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// object is a JSON Schema fragment.
type object map[string]any

// props builds an object schema. Naming the required fields separately keeps
// each property's description next to the property it describes.
func props(required []string, properties object) object {
	schema := object{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	// The tools here take a fixed set of arguments; refusing unknown ones turns
	// a model's typo into a clear error rather than a silently ignored field.
	schema["additionalProperties"] = false
	return schema
}

// str builds a string property.
func str(description string) object {
	return object{"type": "string", "description": description}
}

// enum builds a string property constrained to a fixed set.
func enum(description string, values ...string) object {
	return object{"type": "string", "description": description, "enum": values}
}

func boolean(description string) object {
	return object{"type": "boolean", "description": description}
}

func strList(description string) object {
	return object{"type": "array", "items": object{"type": "string"}, "description": description}
}

func ptr[T any](v T) *T { return &v }

// handler runs a tool and returns the text the model reads.
//
// An error becomes a tool execution error (isError on a successful result),
// not a protocol error: the model can read it, understand what went wrong, and
// try something else. Protocol errors are reserved for a call that should never
// have been made at all, such as an unknown tool name.
type handler func(s *Server, args json.RawMessage) (string, error)

// registered is a tool and its handler.
type registered struct {
	tool
	run handler
}

// tools returns the tool definitions for tools/list.
func (s *Server) tools() []tool {
	out := make([]tool, len(wikiRegistry))
	for i, entry := range wikiRegistry {
		out[i] = entry.tool
	}
	return out
}

// callParams is the tools/call payload.
type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// callTool dispatches one tool invocation.
func (s *Server) callTool(raw json.RawMessage) (any, error) {
	var p callParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "malformed tools/call params: " + err.Error()}
	}

	for _, entry := range wikiRegistry {
		if entry.Name != p.Name {
			continue
		}

		// Another front end may have written since the last call.
		if _, err := s.wiki.Refresh(); err != nil {
			return textResult("could not read the project: "+err.Error(), true), nil
		}

		text, err := entry.run(s, p.Arguments)
		if err != nil {
			// A failed operation is a result the model can read and react to,
			// not a protocol fault.
			return textResult(err.Error(), true), nil
		}
		return textResult(text, false), nil
	}

	// An unknown tool is a client mistake rather than a failed operation.
	return nil, &rpcError{Code: codeInvalidParams, Message: "no tool named " + p.Name}
}

// textResult builds a tools/call result carrying one block of text.
func textResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

// decodeArgs unmarshals a tool's arguments. Absent arguments are an empty
// object, so a tool whose fields are all optional can be called with none.
func decodeArgs(raw json.RawMessage, into any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()

	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}
