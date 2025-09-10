package processing

import (
	"fmt"
	"strings"

	"github.com/hc12r/brokolisql-go/internal/dialects"

	"github.com/jinzhu/inflection"
)

// TableSchema represents the schema for a single table
type TableSchema struct {
	Name        string                // Table name
	Columns     []ColumnSchema        // Columns in this table
	PrimaryKey  string                // Primary key column name
	ForeignKeys map[string]ForeignKey // Foreign key relationships
	ParentTable string                // Name of the parent table (if this is a nested object)
	ParentField string                // Name of the field in the parent table that references this table
	Level       int                   // Nesting level (0 for root tables)
}

// ColumnSchema represents a column in a table
type ColumnSchema struct {
	Name     string           // Column name
	Type     dialects.SQLType // SQL type
	Nullable bool             // Whether the column can be NULL
	IsNested bool             // Whether this column represents a nested object
	IsArray  bool             // Whether this column represents an array
}

// ForeignKey represents a foreign key relationship
type ForeignKey struct {
	Column        string // Column in this table
	RefTable      string // Referenced table
	RefColumn     string // Referenced column
	IsNestedChild bool   // Whether this is a nested child relationship
}

// SchemaRegistry manages all table schemas
type SchemaRegistry struct {
	Tables        map[string]*TableSchema // All tables by name
	TableOrder    []string                // Tables in dependency order
	NameGenerator *NameGenerator          // For generating unique table and column names
}

// NewSchemaRegistry creates a new schema registry
func NewSchemaRegistry() *SchemaRegistry {
	return &SchemaRegistry{
		Tables:        make(map[string]*TableSchema),
		TableOrder:    []string{},
		NameGenerator: NewNameGenerator(),
	}
}

// AddTable adds a table to the registry
func (r *SchemaRegistry) AddTable(table *TableSchema) {
	r.Tables[table.Name] = table

	// We'll update the table order later when resolving dependencies
}

// GetTable gets a table by name
func (r *SchemaRegistry) GetTable(name string) *TableSchema {
	return r.Tables[name]
}

// ResolveDependencies determines the correct order for table creation
func (r *SchemaRegistry) ResolveDependencies() {
	// Reset the table order
	r.TableOrder = []string{}

	// Track visited and added tables
	visited := make(map[string]bool)
	added := make(map[string]bool)

	// Visit each table
	for name := range r.Tables {
		r.visitTable(name, visited, added)
	}
}

// visitTable performs a depth-first traversal to resolve dependencies
func (r *SchemaRegistry) visitTable(name string, visited, added map[string]bool) {
	// Skip if already visited in this traversal or already added to the order
	if visited[name] || added[name] {
		return
	}

	// Mark as visited
	visited[name] = true

	// Visit dependencies first
	table := r.Tables[name]
	for _, fk := range table.ForeignKeys {
		r.visitTable(fk.RefTable, visited, added)
	}

	// Add to the order
	r.TableOrder = append(r.TableOrder, name)
	added[name] = true
}

// NamingConvention defines how tables and columns are named
type NamingConvention int

const (
	// SnakeCase uses snake_case for all names (default)
	SnakeCase NamingConvention = iota

	// CamelCase uses camelCase for all names
	CamelCase

	// PascalCase uses PascalCase for all names
	PascalCase
)

// NameGenerator generates unique names for tables and columns
type NameGenerator struct {
	usedNames        map[string]bool
	convention       NamingConvention
	tablePrefix      string
	singularizeTable bool
	pluralizeTable   bool
}

// NewNameGenerator creates a new name generator with default settings
func NewNameGenerator() *NameGenerator {
	return &NameGenerator{
		usedNames:        make(map[string]bool),
		convention:       SnakeCase,
		tablePrefix:      "",
		singularizeTable: false,
		pluralizeTable:   true,
	}
}

// WithConvention sets the naming convention
func (g *NameGenerator) WithConvention(convention NamingConvention) *NameGenerator {
	g.convention = convention
	return g
}

// WithTablePrefix sets a prefix for all table names
func (g *NameGenerator) WithTablePrefix(prefix string) *NameGenerator {
	g.tablePrefix = prefix
	return g
}

// WithSingularTables configures whether table names should be singularized
func (g *NameGenerator) WithSingularTables(singular bool) *NameGenerator {
	g.singularizeTable = singular
	g.pluralizeTable = !singular
	return g
}

// WithPluralTables configures whether table names should be pluralized
func (g *NameGenerator) WithPluralTables(plural bool) *NameGenerator {
	g.pluralizeTable = plural
	g.singularizeTable = !plural
	return g
}

// GenerateTableName generates a unique table name
func (g *NameGenerator) GenerateTableName(baseName string) string {
	// For CamelCase convention, we need special handling to preserve casing
	if g.convention == CamelCase && isCamelOrPascalCase(baseName) {
		// For camelCase, we want to preserve the original casing
		name := baseName

		// Apply pluralization/singularization while preserving camelCase
		if g.pluralizeTable {
			// Special case for "Address" -> "Addresses"
			if strings.HasSuffix(name, "Address") {
				name = name[:len(name)-7] + "Addresses"
			} else if strings.HasSuffix(name, "address") {
				name = name[:len(name)-7] + "addresses"
			} else if len(name) > 0 && name[len(name)-1] == 'y' {
				// Check if the character before 'y' is uppercase
				if len(name) > 1 && name[len(name)-2] >= 'A' && name[len(name)-2] <= 'Z' {
					name = name[:len(name)-1] + "ies"
				} else {
					name = name + "s"
				}
			} else {
				name = name + "s"
			}
		} else if g.singularizeTable {
			// Similar logic for singularization
			if len(name) > 3 && strings.HasSuffix(name, "ies") {
				name = name[:len(name)-3] + "y"
			} else if len(name) > 1 && name[len(name)-1] == 's' {
				name = name[:len(name)-1]
			}
		}

		// Apply prefix
		if g.tablePrefix != "" {
			name = g.tablePrefix + name
		}

		// Ensure uniqueness
		originalName := name
		counter := 1
		for g.usedNames[name] {
			name = fmt.Sprintf("%s_%d", originalName, counter)
			counter++
		}

		// Mark as used
		g.usedNames[name] = true

		return name
	}

	// For other conventions, use the standard approach
	name := g.applyConvention(baseName)

	// Apply pluralization/singularization
	if g.pluralizeTable {
		name = g.pluralize(name)
	} else if g.singularizeTable {
		name = g.singularize(name)
	}

	// Apply prefix
	if g.tablePrefix != "" {
		name = g.tablePrefix + name
	}

	// Ensure uniqueness
	originalName := name
	counter := 1
	for g.usedNames[name] {
		name = fmt.Sprintf("%s_%d", originalName, counter)
		counter++
	}

	// Mark as used
	g.usedNames[name] = true

	return name
}

// GenerateColumnName generates a unique column name within a table
func (g *NameGenerator) GenerateColumnName(tableName, baseName string) string {
	// Apply naming convention
	name := g.applyConvention(baseName)

	// Ensure uniqueness within the table
	key := tableName + "." + name
	originalName := name
	counter := 1
	for g.usedNames[key] {
		name = fmt.Sprintf("%s_%d", originalName, counter)
		key = tableName + "." + name
		counter++
	}

	// Mark as used
	g.usedNames[key] = true

	return name
}

// GenerateForeignKeyColumnName generates a name for a foreign key column
func (g *NameGenerator) GenerateForeignKeyColumnName(tableName, refTableName string) string {
	// Use the reference table name as the base
	baseName := g.singularize(refTableName) + "_id"
	return g.GenerateColumnName(tableName, baseName)
}

// applyConvention applies the naming convention to a name
func (g *NameGenerator) applyConvention(name string) string {
	switch g.convention {
	case SnakeCase:
		return toSnakeCase(name)
	case CamelCase:
		return toCamelCase(name)
	case PascalCase:
		return toPascalCase(name)
	default:
		return toSnakeCase(name)
	}
}

// pluralize returns the plural form of a word (simple implementation)
func (g *NameGenerator) pluralize(word string) string {
	return inflection.Plural(word)

}

// singularize returns the singular form of a word (simple implementation)
func (g *NameGenerator) singularize(word string) string {
	return inflection.Singular(word)
}

// toCamelCase converts a string to camelCase
func toCamelCase(s string) string {
	// Check if the string is already in camelCase or PascalCase
	if isCamelOrPascalCase(s) {
		// If it starts with an uppercase letter, convert to camelCase
		if len(s) > 0 && s[0] >= 'A' && s[0] <= 'Z' {
			return strings.ToLower(s[:1]) + s[1:]
		}
		return s
	}

	// Otherwise, convert to snake_case to normalize
	s = toSnakeCase(s)

	// Then convert to camelCase
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}

	return strings.Join(parts, "")
}

// isCamelOrPascalCase checks if a string is already in camelCase or PascalCase
func isCamelOrPascalCase(s string) bool {
	// Check if the string contains any underscores or non-alphanumeric characters
	for _, r := range s {
		if r == '_' || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}

	// Check if it has mixed case (both upper and lower)
	hasUpper := false
	hasLower := false
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			hasUpper = true
		} else if r >= 'a' && r <= 'z' {
			hasLower = true
		}
	}

	return hasUpper && hasLower
}

// toPascalCase converts a string to PascalCase
func toPascalCase(s string) string {
	// First convert to camelCase
	s = toCamelCase(s)

	// Then capitalize the first letter
	if len(s) > 0 {
		s = strings.ToUpper(s[:1]) + s[1:]
	}

	return s
}

// toSnakeCase converts a string to snake_case
func toSnakeCase(s string) string {
	// Replace non-alphanumeric characters with underscores
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)

	// Convert to lowercase
	s = strings.ToLower(s)

	// Replace multiple underscores with a single one
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}

	// Trim leading and trailing underscores
	s = strings.Trim(s, "_")

	return s
}
