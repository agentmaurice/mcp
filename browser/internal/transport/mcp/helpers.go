package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// getStringArg extracts a string argument from the request
func getStringArg(args map[string]interface{}, key string, required bool) (string, error) {
	val, ok := args[key]
	if !ok {
		if required {
			return "", fmt.Errorf("missing required argument: %s", key)
		}
		return "", nil
	}

	str, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("argument %s must be a string", key)
	}

	if required && str == "" {
		return "", fmt.Errorf("argument %s cannot be empty", key)
	}

	return str, nil
}

// getIntArg extracts an integer argument from the request
func getIntArg(args map[string]interface{}, key string, defaultVal int) (int, error) {
	val, ok := args[key]
	if !ok {
		return defaultVal, nil
	}

	switch v := val.(type) {
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case int64:
		return int(v), nil
	default:
		return defaultVal, fmt.Errorf("argument %s must be a number", key)
	}
}

// getBoolArg extracts a boolean argument from the request
func getBoolArg(args map[string]interface{}, key string, defaultVal bool) (bool, error) {
	val, ok := args[key]
	if !ok {
		return defaultVal, nil
	}

	b, ok := val.(bool)
	if !ok {
		return defaultVal, fmt.Errorf("argument %s must be a boolean", key)
	}

	return b, nil
}

// getFloatArg extracts a float argument from the request
func getFloatArg(args map[string]interface{}, key string, defaultVal float64) (float64, error) {
	val, ok := args[key]
	if !ok {
		return defaultVal, nil
	}

	switch v := val.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	default:
		return defaultVal, fmt.Errorf("argument %s must be a number", key)
	}
}

// getStringSliceArg extracts a string slice argument from the request
func getStringSliceArg(args map[string]interface{}, key string, required bool) ([]string, error) {
	val, ok := args[key]
	if !ok {
		if required {
			return nil, fmt.Errorf("missing required argument: %s", key)
		}
		return nil, nil
	}

	switch v := val.(type) {
	case []string:
		return v, nil
	case []interface{}:
		result := make([]string, len(v))
		for i, item := range v {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("argument %s must be an array of strings", key)
			}
			result[i] = str
		}
		return result, nil
	default:
		return nil, fmt.Errorf("argument %s must be an array of strings", key)
	}
}

// getObjectArg extracts an object argument from the request.
func getObjectArg(args map[string]interface{}, key string, required bool) (map[string]interface{}, error) {
	val, ok := args[key]
	if !ok {
		if required {
			return nil, fmt.Errorf("missing required argument: %s", key)
		}
		return nil, nil
	}

	obj, ok := val.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("argument %s must be an object", key)
	}

	return obj, nil
}

// createTextResult creates a text result for MCP
func createTextResult(data interface{}) (*mcp.CallToolResult, error) {
	var text string
	switch v := data.(type) {
	case string:
		text = v
	default:
		jsonData, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal result: %w", err)
		}
		text = string(jsonData)
	}

	result := mcp.NewToolResultText(text)
	if _, isString := data.(string); !isString {
		result.StructuredContent = data
	}
	return result, nil
}

// createErrorResult creates an error result for MCP
func createErrorResult(err error) *mcp.CallToolResult {
	return mcp.NewToolResultError(err.Error())
}

// createImageResult creates an image result for MCP
func createImageResult(data string, mimeType string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewImageContent(data, mimeType),
		},
	}
}

// getArgsMap converts the arguments to a map[string]interface{}
func getArgsMap(args any) (map[string]interface{}, error) {
	if args == nil {
		return make(map[string]interface{}), nil
	}
	if m, ok := args.(map[string]interface{}); ok {
		return m, nil
	}
	return nil, fmt.Errorf("arguments must be a map")
}
