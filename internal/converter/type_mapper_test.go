package converter

import "testing"

func TestMapType_VarcharPreservesLength(t *testing.T) {
	mapper := NewTypeMapper()

	tests := []struct {
		input    string
		expected string
	}{
		{"varchar(100)", "varchar(100)"},
		{"varchar(256)", "varchar(256)"},
		{"varchar(300)", "varchar(300)"},
		{"varchar(max)", "longtext"},
		{"nvarchar(100)", "varchar(100)"},
		{"nvarchar(192)", "varchar(192)"},
		{"nvarchar(200)", "varchar(200)"},
		{"nvarchar(500)", "varchar(500)"},
		{"nvarchar(max)", "longtext"},
		{"varbinary(max)", "longblob"},
		// small ones also preserved
		{"varchar(50)", "varchar(50)"},
		{"nvarchar(50)", "varchar(50)"},
	}

	for _, tt := range tests {
		got, err := mapper.MapType(tt.input)
		if err != nil {
			t.Fatalf("MapType(%q) returned error: %v", tt.input, err)
		}
		if got != tt.expected {
			t.Errorf("MapType(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}