package processing

import (
	"reflect"
	"testing"

	"github.com/hc12r/brokolisql-go/internal/dialects"
	"github.com/hc12r/brokolisql-go/pkg/common"
)

func TestNewTypeInferenceEngine(t *testing.T) {
	engine := NewTypeInferenceEngine()

	// Check default values
	if engine.TypeThreshold != 0.8 {
		t.Errorf("NewTypeInferenceEngine() TypeThreshold = %v, want %v", engine.TypeThreshold, 0.8)
	}

	if len(engine.DateFormats) == 0 {
		t.Errorf("NewTypeInferenceEngine() DateFormats should not be empty")
	}
}

func TestTypeInferenceEngine_isInteger(t *testing.T) {
	engine := NewTypeInferenceEngine()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"Valid integer", "123", true},
		{"Negative integer", "-123", true},
		{"Zero", "0", true},
		{"Float", "123.45", false},
		{"String", "abc", false},
		{"Mixed", "123abc", false},
		{"Empty string", "", false},
		{"Whitespace", "  ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := engine.isInteger(tt.input); got != tt.want {
				t.Errorf("TypeInferenceEngine.isInteger() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTypeInferenceEngine_isFloat(t *testing.T) {
	engine := NewTypeInferenceEngine()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"Valid float", "123.45", true},
		{"Integer as float", "123", true},
		{"Negative float", "-123.45", true},
		{"Scientific notation", "1.23e+2", true},
		{"Zero", "0", true},
		{"String", "abc", false},
		{"Mixed", "123.45abc", false},
		{"Empty string", "", false},
		{"Whitespace", "  ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := engine.isFloat(tt.input); got != tt.want {
				t.Errorf("TypeInferenceEngine.isFloat() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTypeInferenceEngine_isBoolean(t *testing.T) {
	engine := NewTypeInferenceEngine()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"True", "true", true},
		{"False", "false", true},
		{"Yes", "yes", true},
		{"No", "no", true},
		{"T", "t", true},
		{"F", "f", true},
		{"Y", "y", true},
		{"N", "n", true},
		{"1", "1", true},
		{"0", "0", true},
		{"Uppercase", "TRUE", true},
		{"Mixed case", "True", true},
		{"With whitespace", "  true  ", true},
		{"String", "abc", false},
		{"Empty string", "", false},
		{"Whitespace", "  ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := engine.isBoolean(tt.input); got != tt.want {
				t.Errorf("TypeInferenceEngine.isBoolean() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTypeInferenceEngine_isDateTime(t *testing.T) {
	engine := NewTypeInferenceEngine()

	tests := []struct {
		name     string
		input    string
		wantDate bool
		wantTime bool
	}{
		{"ISO date", "2023-01-15", true, false},
		{"US date", "01/15/2023", true, false},
		{"UK date", "15/01/2023", true, false},
		{"Date with time", "2023-01-15T14:30:00Z", true, true},
		{"RFC3339", "2023-01-15T14:30:00+00:00", true, true},
		{"Invalid date", "2023-13-45", false, false},
		{"String", "abc", false, false},
		{"Empty string", "", false, false},
		{"Whitespace", "  ", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, isDate, hasTime := engine.isDateTime(tt.input)
			if isDate != tt.wantDate {
				t.Errorf("TypeInferenceEngine.isDateTime() isDate = %v, want %v", isDate, tt.wantDate)
			}
			if hasTime != tt.wantTime {
				t.Errorf("TypeInferenceEngine.isDateTime() hasTime = %v, want %v", hasTime, tt.wantTime)
			}
		})
	}
}

func TestTypeInferenceEngine_inferType(t *testing.T) {
	engine := NewTypeInferenceEngine()

	tests := []struct {
		name   string
		values []interface{}
		want   dialects.SQLType
	}{
		{
			name:   "Empty values",
			values: []interface{}{},
			want:   dialects.SQLTypeText,
		},
		{
			name:   "All integers",
			values: []interface{}{1, 2, 3, 4, 5},
			want:   dialects.SQLTypeInteger,
		},
		{
			name:   "Mostly integers",
			values: []interface{}{1, 2, 3, 4, "abc"},
			want:   dialects.SQLTypeText,
		},
		{
			name:   "Integer strings",
			values: []interface{}{"1", "2", "3", "4", "5"},
			want:   dialects.SQLTypeInteger,
		},
		{
			name:   "All floats",
			values: []interface{}{1.1, 2.2, 3.3, 4.4, 5.5},
			want:   dialects.SQLTypeFloat,
		},
		{
			name:   "Mixed integers and floats",
			values: []interface{}{1, 2, 3.3, 4.4, 5},
			want:   dialects.SQLTypeFloat,
		},
		{
			name:   "Float strings",
			values: []interface{}{"1.1", "2.2", "3.3", "4.4", "5.5"},
			want:   dialects.SQLTypeFloat,
		},
		{
			name:   "All booleans",
			values: []interface{}{true, false, true, false},
			want:   dialects.SQLTypeBoolean,
		},
		{
			name:   "Boolean strings",
			values: []interface{}{"true", "false", "yes", "no", "1", "0"},
			want:   dialects.SQLTypeBoolean,
		},
		{
			name:   "All dates",
			values: []interface{}{"2023-01-15", "2023-02-20", "2023-03-25"},
			want:   dialects.SQLTypeDate,
		},
		{
			name:   "All datetimes",
			values: []interface{}{"2023-01-15T14:30:00Z", "2023-02-20T15:45:00Z"},
			want:   dialects.SQLTypeDateTime,
		},
		{
			name:   "Mixed types",
			values: []interface{}{"abc", 123, 45.6, true, "2023-01-15"},
			want:   dialects.SQLTypeText,
		},
		{
			name:   "Mostly text",
			values: []interface{}{"abc", "def", "ghi", 123},
			want:   dialects.SQLTypeText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := engine.inferType(tt.values); got != tt.want {
				t.Errorf("TypeInferenceEngine.inferType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTypeInferenceEngine_InferColumnTypes(t *testing.T) {
	engine := NewTypeInferenceEngine()

	// Create a test dataset
	columns := []string{"id", "name", "age", "price", "is_active", "created_at", "updated_at", "notes"}
	rows := []common.DataRow{
		{
			"id":         1,
			"name":       "Product 1",
			"age":        30,
			"price":      19.99,
			"is_active":  true,
			"created_at": "2023-01-15",
			"updated_at": "2023-01-15T14:30:00Z",
			"notes":      "Some notes",
		},
		{
			"id":         2,
			"name":       "Product 2",
			"age":        25,
			"price":      29.99,
			"is_active":  false,
			"created_at": "2023-02-20",
			"updated_at": "2023-02-20T15:45:00Z",
			"notes":      "More notes",
		},
		{
			"id":         3,
			"name":       "Product 3",
			"age":        40,
			"price":      39.99,
			"is_active":  true,
			"created_at": "2023-03-25",
			"updated_at": "2023-03-25T16:00:00Z",
			"notes":      "Even more notes",
		},
	}

	// Expected column types
	want := map[string]dialects.SQLType{
		"id":         dialects.SQLTypeInteger,
		"name":       dialects.SQLTypeText,
		"age":        dialects.SQLTypeInteger,
		"price":      dialects.SQLTypeFloat,
		"is_active":  dialects.SQLTypeBoolean,
		"created_at": dialects.SQLTypeDate,
		"updated_at": dialects.SQLTypeDateTime,
		"notes":      dialects.SQLTypeText,
	}

	// Test InferColumnTypes
	got := engine.InferColumnTypes(columns, rows)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("TypeInferenceEngine.InferColumnTypes() = %v, want %v", got, want)
	}
}

func TestTypeInferenceEngine_CustomThreshold(t *testing.T) {
	// Create an engine with a custom threshold
	engine := &TypeInferenceEngine{
		DateFormats:   []string{"2006-01-02"},
		TypeThreshold: 0.6, // 60% threshold
	}

	// Test with mixed values where 60% are integers
	values := []interface{}{1, 2, 3, "abc", "def"}

	// With 60% threshold, this should be inferred as INTEGER
	if got := engine.inferType(values); got != dialects.SQLTypeInteger {
		t.Errorf("TypeInferenceEngine.inferType() with 60%% threshold = %v, want %v", got, dialects.SQLTypeInteger)
	}

	// Change threshold to 70%
	engine.TypeThreshold = 0.7

	// With 70% threshold, this should be inferred as TEXT
	if got := engine.inferType(values); got != dialects.SQLTypeText {
		t.Errorf("TypeInferenceEngine.inferType() with 70%% threshold = %v, want %v", got, dialects.SQLTypeText)
	}
}
