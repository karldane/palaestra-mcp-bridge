package newrelic

import (
	"fmt"
	"regexp"
	"strings"
)

// parseLogQuery converts log search query syntax to valid NRQL WHERE clause syntax
// Supports:
// - field:value pairs (e.g., level:ERROR, service:my-app)
// - Quoted values (e.g., message:"error message")
// - Boolean operators: AND, OR, NOT
// - Simple text search (searches message field)
func parseLogQuery(query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", nil
	}

	// Check if it's a simple text search (no field:value pairs)
	if !strings.Contains(query, ":") && !strings.Contains(query, "AND") &&
		!strings.Contains(query, "OR") && !strings.Contains(query, "NOT") {
		return fmt.Sprintf("message LIKE '%%%s%%'", escapeString(query)), nil
	}

	// Parse the query
	result, err := parseExpression(query)
	if err != nil {
		return "", fmt.Errorf("failed to parse query: %w", err)
	}

	return result, nil
}

// parseExpression parses the query expression handling AND, OR, NOT operators
func parseExpression(query string) (string, error) {
	query = strings.TrimSpace(query)

	// Handle NOT operator
	if strings.HasPrefix(query, "NOT ") {
		inner, err := parseExpression(query[4:])
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("NOT (%s)", inner), nil
	}

	// Split by AND first (lower precedence than OR)
	andParts := splitByOperator(query, "AND")
	if len(andParts) > 1 {
		var parts []string
		for _, part := range andParts {
			parsed, err := parseExpression(part)
			if err != nil {
				return "", err
			}
			parts = append(parts, parsed)
		}
		return fmt.Sprintf("(%s)", strings.Join(parts, " AND ")), nil
	}

	// Split by OR
	orParts := splitByOperator(query, "OR")
	if len(orParts) > 1 {
		var parts []string
		for _, part := range orParts {
			parsed, err := parseExpression(part)
			if err != nil {
				return "", err
			}
			parts = append(parts, parsed)
		}
		return fmt.Sprintf("(%s)", strings.Join(parts, " OR ")), nil
	}

	// Single term - parse field:value or plain text
	return parseTerm(query)
}

// splitByOperator splits query by operator, respecting parentheses and quotes
func splitByOperator(query, operator string) []string {
	var parts []string
	var current strings.Builder
	inQuotes := false
	inParens := 0

	// Normalize operator spacing
	operatorPattern := regexp.MustCompile(`\s+` + operator + `\s+`)
	query = operatorPattern.ReplaceAllString(query, " "+operator+" ")

	tokens := strings.Fields(query)

	for i, token := range tokens {
		// Check for quotes
		if strings.Contains(token, `"`) {
			// Count quotes in this token
			quoteCount := strings.Count(token, `"`)
			if quoteCount%2 == 1 {
				inQuotes = !inQuotes
			}
		}

		// Track parentheses
		if !inQuotes {
			inParens += strings.Count(token, "(") - strings.Count(token, ")")
		}

		// Check if this is the operator
		if !inQuotes && inParens == 0 && strings.ToUpper(token) == operator {
			if current.Len() > 0 {
				parts = append(parts, strings.TrimSpace(current.String()))
				current.Reset()
			}
		} else {
			if current.Len() > 0 {
				current.WriteString(" ")
			}
			current.WriteString(token)
		}

		// If there's more content and we hit the operator at end of quoted string
		_ = i // suppress unused warning
	}

	if current.Len() > 0 {
		parts = append(parts, strings.TrimSpace(current.String()))
	}

	return parts
}

// parseTerm parses a single term (field:value or plain text)
func parseTerm(term string) (string, error) {
	term = strings.TrimSpace(term)

	// Remove outer parentheses if present
	if strings.HasPrefix(term, "(") && strings.HasSuffix(term, ")") {
		term = term[1 : len(term)-1]
		return parseExpression(term)
	}

	// Check for field:value pattern
	colonIdx := strings.Index(term, ":")
	if colonIdx > 0 {
		field := strings.TrimSpace(term[:colonIdx])
		value := strings.TrimSpace(term[colonIdx+1:])

		// Remove quotes if present
		if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			value = value[1 : len(value)-1]
		}

		return fmt.Sprintf("%s = '%s'", field, escapeString(value)), nil
	}

	// Plain text - search message field
	return fmt.Sprintf("message LIKE '%%%s%%'", escapeString(term)), nil
}

// escapeString escapes special characters in a string for NRQL
func escapeString(s string) string {
	// Escape single quotes by doubling them (NRQL standard)
	s = strings.ReplaceAll(s, "'", "''")

	// Escape backslashes
	s = strings.ReplaceAll(s, "\\", "\\\\")

	return s
}
