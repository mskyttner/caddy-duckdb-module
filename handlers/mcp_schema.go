package handlers

import "encoding/json"

// schemaProp describes a single property in a JSON Schema object.
type schemaProp struct {
	name        string
	typ         string // "string" or "number"
	description string
	required    bool
	enum        []string // non-nil enables an enum constraint
}

// strProp returns a string schema property.
func strProp(name, description string, required bool) schemaProp {
	return schemaProp{name: name, typ: "string", description: description, required: required}
}

// numProp returns a number schema property.
func numProp(name, description string) schemaProp {
	return schemaProp{name: name, typ: "number", description: description}
}

// enumProp returns a string schema property restricted to a set of values.
func enumProp(name, description string, values ...string) schemaProp {
	return schemaProp{name: name, typ: "string", description: description, enum: values}
}

// buildSchema produces a JSON Schema object ({"type":"object",...}) suitable
// for use as mcp.Tool.InputSchema. The schema is required by the go-sdk and
// must have type "object".
func buildSchema(props ...schemaProp) json.RawMessage {
	type prop struct {
		Type        string   `json:"type"`
		Description string   `json:"description,omitempty"`
		Enum        []string `json:"enum,omitempty"`
	}

	properties := make(map[string]prop, len(props))
	var required []string

	for _, p := range props {
		properties[p.name] = prop{
			Type:        p.typ,
			Description: p.description,
			Enum:        p.enum,
		}
		if p.required {
			required = append(required, p.name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}

	b, _ := json.Marshal(schema)
	return b
}

// buildDynamicSchema constructs a schema for user-defined macros where
// parameter names and types are known only at runtime.
func buildDynamicSchema(params []string, sqlTypes []string) json.RawMessage {
	props := make([]schemaProp, 0, len(params))
	for i, param := range params {
		sqlType := "VARCHAR"
		if i < len(sqlTypes) {
			sqlType = sqlTypes[i]
		}
		desc := "SQL type: " + sqlType
		if isNumericSQLType(sqlType) {
			props = append(props, numProp(param, desc))
		} else {
			props = append(props, strProp(param, desc, false))
		}
	}
	return buildSchema(props...)
}
