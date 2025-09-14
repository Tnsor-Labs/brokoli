# Nested JSON Support in Streaming Mode

## Overview

This document describes how BrokoliSQL-Go handles nested JSON objects in streaming mode. The streaming implementation has been enhanced to properly detect and process nested objects, creating normalized relational tables with foreign key relationships.

## Problem

The original streaming implementation had a limitation: it didn't properly handle nested JSON objects. When processing JSON files with nested objects (like objects or arrays), it would simply convert them to JSON strings rather than creating proper relational tables with foreign key relationships.

For example, given this JSON:

```json
[
  {
    "id": 1,
    "name": "John Doe",
    "address": {
      "street": "123 Main St",
      "city": "Anytown"
    }
  }
]
```

The original streaming implementation would create a single table with the address as a JSON string, while the default (non-streaming) implementation would create two tables (users and addresses) with a foreign key relationship.

## Solution

The solution integrates the existing nested JSON processing capabilities with the streaming implementation:

1. **Early Detection**: The streaming implementation now samples the first 100 rows to detect if the data contains nested objects.

2. **Conditional Processing**: If nested objects are detected, the implementation switches to using the `NestedJSONProcessor` for the entire file.

3. **Code Reuse**: The solution reuses the existing `NestedJSONProcessor` code, avoiding code duplication.

4. **Graceful Fallback**: For files without nested objects, the streaming implementation continues to use the memory-efficient streaming approach.

## Implementation Details

### Detection of Nested Objects

The streaming implementation uses the `hasNestedObjects` method to check if the data contains nested objects:

```go
func (g *StreamingSQLGenerator) hasNestedObjects(rows []common.DataRow) bool {
    // Check each row for nested objects
    for _, row := range rows {
        for _, value := range row {
            // Check if it's a map
            if _, ok := value.(map[string]interface{}); ok {
                return true
            }

            // Check if it's a JSON string that contains an object
            if strValue, ok := value.(string); ok {
                // If it starts with { and ends with }, it might be a JSON object
                if len(strValue) > 1 && strValue[0] == '{' && strValue[len(strValue)-1] == '}' {
                    return true
                }
            }
        }
    }

    return false
}
```

### Processing Flow

The processing flow in `StreamingSQLGenerator.ProcessStream` has been updated:

1. Sample the first 100 rows
2. Check if the sample contains nested objects
3. If nested objects are detected:
   - Close the streaming channels
   - Load the entire file using the regular JSON loader
   - Process the data using the `NestedJSONProcessor`
   - Write the SQL to the output file
4. If no nested objects are detected:
   - Continue with the regular streaming implementation

## Usage

No changes are required to use this feature. The streaming implementation automatically detects and handles nested objects:

```bash
brokolisql --input data.json --output output.sql --table users --streaming --create-table
```

## Examples

### Nested JSON Example

For a JSON file with nested objects like:

```json
[
  {
    "id": 1,
    "name": "John Doe",
    "address": {
      "street": "123 Main St",
      "city": "Anytown"
    }
  }
]
```

The output SQL will contain multiple tables with foreign key relationships:

```sql
CREATE TABLE "addresses" (
  "id" INTEGER PRIMARY KEY,
  "street" TEXT,
  "city" TEXT
);

CREATE TABLE "users" (
  "id" INTEGER PRIMARY KEY,
  "address_id" INTEGER,
  "name" TEXT,
  FOREIGN KEY ("address_id") REFERENCES "addresses" ("id") ON DELETE CASCADE
);

INSERT INTO "addresses" ("id", "street", "city") VALUES
(11, '123 Main St', 'Anytown');

INSERT INTO "users" ("id", "address_id", "name") VALUES
(1, 11, 'John Doe');
```

## Testing

The implementation includes comprehensive tests:

1. **Unit Tests**: `TestStreamingSQLGenerator_NestedObjects` verifies that the streaming implementation correctly detects and processes nested objects.

2. **Integration Tests**: `TestStreamingModeIntegration_NestedJSON` provides end-to-end testing of the nested JSON processing in streaming mode.

## Conclusion

With this enhancement, the streaming implementation now properly handles nested JSON objects, creating normalized relational tables with foreign key relationships. This ensures that the streaming mode produces the same high-quality output as the default implementation, while still maintaining the memory efficiency benefits for flat data.