package processing

import (
	"encoding/json"
	"reflect"

	"github.com/hc12r/brokolisql-go/internal/dialects"
	"github.com/hc12r/brokolisql-go/pkg/common"
)

// JSONAnalyzer analyzes JSON data and builds a schema registry
type JSONAnalyzer struct {
	registry     *SchemaRegistry
	typeInferer  *TypeInferenceEngine
	primaryKeyID int // Counter for generating primary key values
}

// NewJSONAnalyzer creates a new JSON analyzer
func NewJSONAnalyzer() *JSONAnalyzer {
	return &JSONAnalyzer{
		registry:     NewSchemaRegistry(),
		typeInferer:  NewTypeInferenceEngine(),
		primaryKeyID: 1,
	}
}

// AnalyzeJSON analyzes JSON data and builds a schema registry
func (a *JSONAnalyzer) AnalyzeJSON(data []map[string]interface{}, rootTableName string) (*SchemaRegistry, error) {
	// Save the current name generator
	oldNameGenerator := a.registry.NameGenerator

	// Reset the registry
	a.registry = NewSchemaRegistry()

	// Restore the name generator
	a.registry.NameGenerator = oldNameGenerator

	a.primaryKeyID = 1

	// Create the root table
	rootTable := &TableSchema{
		Name:        a.registry.NameGenerator.GenerateTableName(rootTableName),
		Columns:     []ColumnSchema{},
		PrimaryKey:  "id",
		ForeignKeys: make(map[string]ForeignKey),
		Level:       0,
	}

	// Add ID column to root table
	rootTable.Columns = append(rootTable.Columns, ColumnSchema{
		Name:     "id",
		Type:     dialects.SQLTypeInteger,
		Nullable: false,
		IsNested: false,
		IsArray:  false,
	})

	// Analyze the structure of the data
	a.analyzeStructure(data, rootTable)

	a.registry.AddTable(rootTable)

	a.registry.ResolveDependencies()

	return a.registry, nil
}

// analyzeStructure analyzes the structure of JSON data and updates the table schema
func (a *JSONAnalyzer) analyzeStructure(data []map[string]interface{}, table *TableSchema) {
	// Track columns we've seen
	seenColumns := make(map[string]bool)
	seenColumns["id"] = true // ID is already added

	// Analyze each object
	for _, obj := range data {
		for key, value := range obj {
			if seenColumns[key] {
				continue // Skip columns we've already processed
			}

			// Mark as seen
			seenColumns[key] = true

			// Check if this is a nested object
			if a.isNestedObject(value) {
				// Create a child table for this nested object
				a.handleNestedObject(key, value, table)
			} else if a.isArray(value) {
				// Handle array values
				a.handleArray(key, value, table)
			} else {
				// Regular column
				columnType := a.inferColumnType(value)
				table.Columns = append(table.Columns, ColumnSchema{
					Name:     key,
					Type:     columnType,
					Nullable: true, // Assume nullable by default
					IsNested: false,
					IsArray:  false,
				})
			}
		}
	}
}

// handleNestedObject creates a child table for a nested object
func (a *JSONAnalyzer) handleNestedObject(key string, value interface{}, parentTable *TableSchema) {
	// Extract the nested object
	nestedObj, ok := value.(map[string]interface{})
	if !ok {
		// Try to unmarshal if it's a JSON string
		if strValue, isStr := value.(string); isStr {
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(strValue), &obj); err == nil {
				nestedObj = obj
			} else {
				// Not a valid nested object, treat as a regular column
				columnType := a.inferColumnType(value)
				parentTable.Columns = append(parentTable.Columns, ColumnSchema{
					Name:     key,
					Type:     columnType,
					Nullable: true,
					IsNested: false,
					IsArray:  false,
				})
				return
			}
		} else {
			// Not a valid nested object, treat as a regular column
			columnType := a.inferColumnType(value)
			parentTable.Columns = append(parentTable.Columns, ColumnSchema{
				Name:     key,
				Type:     columnType,
				Nullable: true,
				IsNested: false,
				IsArray:  false,
			})
			return
		}
	}

	// Generate a name for the child table
	childTableName := a.registry.NameGenerator.GenerateTableName(key)

	// Create the child table
	childTable := &TableSchema{
		Name:        childTableName,
		Columns:     []ColumnSchema{},
		PrimaryKey:  "id",
		ForeignKeys: make(map[string]ForeignKey),
		ParentTable: parentTable.Name,
		ParentField: key,
		Level:       parentTable.Level + 1,
	}

	// Add ID column to child table
	childTable.Columns = append(childTable.Columns, ColumnSchema{
		Name:     "id",
		Type:     dialects.SQLTypeInteger,
		Nullable: false,
		IsNested: false,
		IsArray:  false,
	})

	// Add the child table to the registry
	a.registry.AddTable(childTable)

	// Add a foreign key column to the parent table
	fkColumnName := a.registry.NameGenerator.GenerateForeignKeyColumnName(parentTable.Name, childTableName)
	parentTable.Columns = append(parentTable.Columns, ColumnSchema{
		Name:     fkColumnName,
		Type:     dialects.SQLTypeInteger,
		Nullable: true,
		IsNested: true,
		IsArray:  false,
	})

	// Add the foreign key relationship
	parentTable.ForeignKeys[fkColumnName] = ForeignKey{
		Column:        fkColumnName,
		RefTable:      childTableName,
		RefColumn:     "id",
		IsNestedChild: true,
	}

	// Analyze the nested object
	a.analyzeStructure([]map[string]interface{}{nestedObj}, childTable)
}

// handleArray handles array values
func (a *JSONAnalyzer) handleArray(key string, value interface{}, parentTable *TableSchema) {
	// Get the array
	var arr []interface{}

	// Convert to array if it's a slice
	if reflect.TypeOf(value) != nil && reflect.TypeOf(value).Kind() == reflect.Slice {
		arr = value.([]interface{})
	} else if strValue, ok := value.(string); ok {
		// Try to unmarshal if it's a JSON string
		if err := json.Unmarshal([]byte(strValue), &arr); err != nil {
			// Not a valid array, treat as a regular column
			parentTable.Columns = append(parentTable.Columns, ColumnSchema{
				Name:     key,
				Type:     dialects.SQLTypeText,
				Nullable: true,
				IsNested: false,
				IsArray:  true,
			})
			return
		}
	} else {
		// Not a valid array, treat as a regular column
		columnType := a.inferColumnType(value)
		parentTable.Columns = append(parentTable.Columns, ColumnSchema{
			Name:     key,
			Type:     columnType,
			Nullable: true,
			IsNested: false,
			IsArray:  false,
		})
		return
	}

	// If the array is empty, just store it as a JSON string
	if len(arr) == 0 {
		parentTable.Columns = append(parentTable.Columns, ColumnSchema{
			Name:     key,
			Type:     dialects.SQLTypeText,
			Nullable: true,
			IsNested: false,
			IsArray:  true,
		})
		return
	}

	// Check if the array contains objects
	containsObjects := false
	for _, item := range arr {
		if a.isNestedObject(item) {
			containsObjects = true
			break
		}
	}

	if containsObjects {
		// Create a child table for this array of objects
		a.handleArrayOfObjects(key, arr, parentTable)
	} else {
		// For arrays of primitive values, store as JSON string
		parentTable.Columns = append(parentTable.Columns, ColumnSchema{
			Name:     key,
			Type:     dialects.SQLTypeText,
			Nullable: true,
			IsNested: false,
			IsArray:  true,
		})
	}
}

// handleArrayOfObjects creates a child table for an array of objects
func (a *JSONAnalyzer) handleArrayOfObjects(key string, arr []interface{}, parentTable *TableSchema) {
	// Generate a name for the child table
	childTableName := a.registry.NameGenerator.GenerateTableName(key)

	// Create the child table
	childTable := &TableSchema{
		Name:        childTableName,
		Columns:     []ColumnSchema{},
		PrimaryKey:  "id",
		ForeignKeys: make(map[string]ForeignKey),
		ParentTable: parentTable.Name,
		ParentField: key,
		Level:       parentTable.Level + 1,
	}

	// Add ID column to child table
	childTable.Columns = append(childTable.Columns, ColumnSchema{
		Name:     "id",
		Type:     dialects.SQLTypeInteger,
		Nullable: false,
		IsNested: false,
		IsArray:  false,
	})

	// Add parent ID column to child table (for the many-to-one relationship)
	parentIDColumn := parentTable.Name + "_id"
	childTable.Columns = append(childTable.Columns, ColumnSchema{
		Name:     parentIDColumn,
		Type:     dialects.SQLTypeInteger,
		Nullable: false,
		IsNested: false,
		IsArray:  false,
	})

	// Add the foreign key relationship
	childTable.ForeignKeys[parentIDColumn] = ForeignKey{
		Column:        parentIDColumn,
		RefTable:      parentTable.Name,
		RefColumn:     "id",
		IsNestedChild: false,
	}

	// Add the child table to the registry
	a.registry.AddTable(childTable)

	// Convert array items to maps
	var objects []map[string]interface{}
	for _, item := range arr {
		if obj, ok := item.(map[string]interface{}); ok {
			objects = append(objects, obj)
		} else if strValue, ok := item.(string); ok {
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(strValue), &obj); err == nil {
				objects = append(objects, obj)
			}
		}
	}

	// Analyze the structure of the objects
	if len(objects) > 0 {
		a.analyzeStructure(objects, childTable)
	}
}

// isNestedObject checks if a value is a nested object
func (a *JSONAnalyzer) isNestedObject(value interface{}) bool {
	// Check if it's a map
	if _, ok := value.(map[string]interface{}); ok {
		return true
	}

	// Check if it's a JSON string that contains an object
	if strValue, ok := value.(string); ok {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(strValue), &obj); err == nil {
			return true
		}
	}

	return false
}

// isArray checks if a value is an array
func (a *JSONAnalyzer) isArray(value interface{}) bool {
	// Check if it's a slice
	if reflect.TypeOf(value) != nil && reflect.TypeOf(value).Kind() == reflect.Slice {
		return true
	}

	// Check if it's a JSON string that contains an array
	if strValue, ok := value.(string); ok {
		var arr []interface{}
		if err := json.Unmarshal([]byte(strValue), &arr); err == nil {
			return true
		}
	}

	return false
}

// inferColumnType infers the SQL type for a value
func (a *JSONAnalyzer) inferColumnType(value interface{}) dialects.SQLType {
	switch v := value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return dialects.SQLTypeInteger
	case float32, float64:
		return dialects.SQLTypeFloat
	case bool:
		return dialects.SQLTypeBoolean
	case string:
		// Check if it's a date or datetime
		if _, isDate, hasTime := a.typeInferer.isDateTime(v); isDate {
			if hasTime {
				return dialects.SQLTypeDateTime
			}
			return dialects.SQLTypeDate
		}
		return dialects.SQLTypeText
	default:
		return dialects.SQLTypeText
	}
}

// ExtractNestedData extracts data for all tables from the original JSON data
func (a *JSONAnalyzer) ExtractNestedData(data []map[string]interface{}) map[string][]map[string]interface{} {
	result := make(map[string][]map[string]interface{})

	// Find the root table (the one with no parent)
	var rootTableName string
	for _, tableName := range a.registry.TableOrder {
		table := a.registry.GetTable(tableName)
		if table.ParentTable == "" {
			rootTableName = tableName
			break
		}
	}

	if rootTableName == "" {
		common.DefaultLogger.Debug("No root table found!")
		return result
	}

	rootTable := a.registry.GetTable(rootTableName)
	common.DefaultLogger.Debug("Extracting data for root table: %s", rootTableName)
	common.DefaultLogger.Debug("Table order: %v", a.registry.TableOrder)

	// Extract data for the root table
	rootData := a.extractTableData(data, rootTable, nil)
	result[rootTableName] = rootData
	common.DefaultLogger.Debug("Extracted %d rows for root table %s", len(rootData), rootTableName)

	// Process tables in dependency order
	// First, build a map of child tables by parent
	childTables := make(map[string][]string)
	for _, tableName := range a.registry.TableOrder {
		table := a.registry.GetTable(tableName)
		if table.ParentTable != "" {
			childTables[table.ParentTable] = append(childTables[table.ParentTable], tableName)
		}
	}

	// Process tables in a breadth-first manner starting from the root
	processedTables := map[string]bool{rootTableName: true}
	queue := []string{rootTableName}

	for len(queue) > 0 {
		// Get the next parent table to process
		parentName := queue[0]
		queue = queue[1:]

		// Process all children of this parent
		for _, childName := range childTables[parentName] {
			if processedTables[childName] {
				continue
			}

			childTable := a.registry.GetTable(childName)
			parentTable := a.registry.GetTable(parentName)

			// Find the parent data
			parentData, ok := result[parentName]
			if !ok {
				// fmt.Printf("Parent data not found for %s (parent: %s)\n", childName, parentName)
				continue
			}

			// Extract data for this table
			var tableData []map[string]interface{}

			// Check if this is an array table (has a parent_id column)
			isArrayTable := false
			for _, col := range childTable.Columns {
				if col.Name == parentTable.Name+"_id" {
					isArrayTable = true
					break
				}
			}

			if isArrayTable {
				common.DefaultLogger.Debug("%s is an array table", childName)
				tableData = a.extractArrayTableData(parentData, parentTable, childTable)
			} else {
				common.DefaultLogger.Debug("%s is a nested object table", childName)
				tableData = a.extractChildTableData(parentData, parentTable, childTable)
			}

			result[childName] = tableData
			common.DefaultLogger.Debug("Added %d rows for %s to result", len(tableData), childName)

			// Mark as processed and add to queue
			processedTables[childName] = true
			queue = append(queue, childName)
		}
	}

	// Debug output of the final result
	common.DefaultLogger.Debug("Final result contains data for %d tables:", len(result))
	for tableName, tableData := range result {
		common.DefaultLogger.Debug("  - %s: %d rows", tableName, len(tableData))
	}

	return result
}

// extractArrayTableData extracts data for a table that represents an array of objects
func (a *JSONAnalyzer) extractArrayTableData(parentData []map[string]interface{}, parentTable, childTable *TableSchema) []map[string]interface{} {
	var result []map[string]interface{}

	// Find the array field in the parent table
	arrayField := childTable.ParentField

	// Process each parent row
	for _, parentRow := range parentData {
		// Get the array from the parent
		arrayValue, ok := parentRow[arrayField]
		if !ok {
			continue
		}

		// Convert to array
		var arr []interface{}
		if reflect.TypeOf(arrayValue) != nil && reflect.TypeOf(arrayValue).Kind() == reflect.Slice {
			arr = arrayValue.([]interface{})
		} else if strValue, ok := arrayValue.(string); ok {
			if err := json.Unmarshal([]byte(strValue), &arr); err != nil {
				continue
			}
		} else {
			continue
		}

		// Process each array item
		for _, item := range arr {
			// Convert to map
			var objMap map[string]interface{}
			if obj, ok := item.(map[string]interface{}); ok {
				objMap = obj
			} else if strValue, ok := item.(string); ok {
				if err := json.Unmarshal([]byte(strValue), &objMap); err != nil {
					continue
				}
			} else {
				continue
			}

			// Create a row for the child table
			row := make(map[string]interface{})

			// Add ID
			row["id"] = a.primaryKeyID
			a.primaryKeyID++

			// Add parent ID
			row[parentTable.Name+"_id"] = parentRow["id"]

			// Add regular columns
			for _, col := range childTable.Columns {
				if col.Name == "id" || col.Name == parentTable.Name+"_id" {
					continue // Already added
				}

				if col.IsNested {
					// This is a foreign key to a nested object
					// We'll set this later when processing the child table
					continue
				}

				// Get the value from the object
				if val, ok := objMap[col.Name]; ok {
					row[col.Name] = val
				}
			}

			result = append(result, row)
		}
	}

	return result
}

// extractTableData extracts data for a table from the original JSON data
func (a *JSONAnalyzer) extractTableData(data []map[string]interface{}, table *TableSchema, parentRow map[string]interface{}) []map[string]interface{} {
	var result []map[string]interface{}

	// For the root table, we need to create rows with IDs
	for _, obj := range data {
		row := make(map[string]interface{})

		// Add ID
		row["id"] = a.primaryKeyID
		a.primaryKeyID++

		// Add regular columns
		for _, col := range table.Columns {
			if col.Name == "id" {
				continue // Already added
			}

			if col.IsNested {
				continue
			}

			// Get the value from the original object
			if val, ok := obj[col.Name]; ok {
				row[col.Name] = val
			}
		}

		// For the root table, we need to preserve the nested objects
		// so they can be processed later
		if table.ParentTable == "" {
			// Copy all fields from the original object, including nested objects
			for key, val := range obj {
				// Skip fields that are already in the row
				if _, exists := row[key]; !exists {
					row[key] = val
				}
			}
		}

		result = append(result, row)
	}

	return result
}

// extractChildTableData extracts data for a child table
func (a *JSONAnalyzer) extractChildTableData(parentData []map[string]interface{}, parentTable, childTable *TableSchema) []map[string]interface{} {
	var result []map[string]interface{}

	// Find the foreign key column in the parent table
	var fkColumn string
	for col, fk := range parentTable.ForeignKeys {
		if fk.RefTable == childTable.Name {
			fkColumn = col
			break
		}
	}

	if fkColumn == "" {
		common.DefaultLogger.Debug("No foreign key found for child table %s in parent table %s", childTable.Name, parentTable.Name)
		return result // No foreign key found
	}

	common.DefaultLogger.Debug("Extracting data for child table %s from parent table %s using field %s",
		childTable.Name, parentTable.Name, childTable.ParentField)

	// Process each parent row
	for i, parentRow := range parentData {
		// Get the nested object from the parent
		nestedObj, ok := parentRow[childTable.ParentField]
		if !ok {
			common.DefaultLogger.Debug("Parent field %s not found in parent row %d", childTable.ParentField, i)
			continue
		}

		// Convert to map if it's a string
		var objMap map[string]interface{}
		if strObj, isStr := nestedObj.(string); isStr {
			if err := json.Unmarshal([]byte(strObj), &objMap); err != nil {
				common.DefaultLogger.Debug("Failed to unmarshal string to object: %v", err)
				continue
			}
		} else if objMap, ok = nestedObj.(map[string]interface{}); !ok {
			common.DefaultLogger.Debug("Nested object is not a map: %T", nestedObj)
			continue
		}

		// Create a row for the child table
		row := make(map[string]interface{})

		// Add ID
		row["id"] = a.primaryKeyID
		a.primaryKeyID++

		parentRow[childTable.ParentField+"_id"] = row["id"]

		// Add regular columns
		for _, col := range childTable.Columns {
			if col.Name == "id" {
				continue // Already added
			}

			if col.IsNested {
				// This is a foreign key to a nested object
				continue
			}

			// Get the value from the nested object
			if val, ok := objMap[col.Name]; ok {
				row[col.Name] = val
				common.DefaultLogger.Debug("Added column %s with value %v to %s", col.Name, val, childTable.Name)
			} else {
				common.DefaultLogger.Debug("Column %s not found in nested object for %s", col.Name, childTable.Name)
			}
		}

		// Preserve nested objects for further processing
		for key, val := range objMap {
			// Skip fields that are already in the row
			if _, exists := row[key]; !exists {
				row[key] = val
			}
		}

		result = append(result, row)
	}

	common.DefaultLogger.Debug("Extracted %d rows for %s", len(result), childTable.Name)
	return result
}
