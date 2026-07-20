package fieldregistry

import (
	"fmt"
	"slices"
)

// FieldKind describes how a field value should be interpreted.
type FieldKind string

const (
	// KindColumn means the value is a CSV column header name.
	KindColumn FieldKind = "column"
	// KindEnum means the value must be one of a fixed set of strings.
	KindEnum FieldKind = "enum"
)

// FieldSpec defines one mappable field within a layout.
type FieldSpec struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Required    bool      `json:"required"`
	Kind        FieldKind `json:"kind"`
	Aliases     []string  `json:"aliases,omitempty"`     // common CSV header spellings, matched case-insensitively
	EnumValues  []string  `json:"enum_values,omitempty"` // valid values for KindEnum fields
	Description string    `json:"description,omitempty"`
}

// LayoutSpec describes one CSV amount-column layout variant.
type LayoutSpec struct {
	Key    string      `json:"key"`
	Label  string      `json:"label"`
	Fields []FieldSpec `json:"fields"`
}

// SourceSpec describes a top-level data source type and its supported layouts.
type SourceSpec struct {
	Key     string       `json:"key"`
	Label   string       `json:"label"`
	Layouts []LayoutSpec `json:"layouts"`
}

// MappingConfig is the parsed form of the mapping_config JSONB column.
// Each key in Fields is a FieldSpec.Key; the value is the CSV column name (for
// KindColumn) or the enum value (for KindEnum). An empty string means
// "not mapped" for optional fields.
type MappingConfig struct {
	Layout string            `json:"layout"`
	Fields map[string]string `json:"fields"`
}

// shared field definitions reused across layouts
var (
	fieldDate = FieldSpec{
		Key: "date", Label: "Date", Required: true, Kind: KindColumn,
		Aliases: []string{"date", "transaction date", "trans date", "trans. date",
			"posting date", "value date", "settlement date", "effective date", "activity date"},
	}
	fieldMerchant = FieldSpec{
		Key: "merchant", Label: "Merchant / Description", Required: true, Kind: KindColumn,
		Aliases: []string{"description", "merchant", "payee", "memo", "narrative",
			"details", "transaction description", "name", "remarks"},
	}
	fieldImportedID = FieldSpec{
		Key: "imported_id", Label: "Transaction ID (optional)", Required: false, Kind: KindColumn,
		Aliases: []string{"id", "transaction id", "check number", "reference",
			"ref", "reference number", "seq", "check no"},
	}
	fieldBalance = FieldSpec{
		Key: "balance", Label: "Balance (optional)", Required: false, Kind: KindColumn,
		Aliases: []string{"balance", "running balance", "available balance", "ledger balance", "closing balance"},
	}
)

// Registry is the authoritative, static field registry. Validation, form
// generation, and column-detection all derive from this definition.
var Registry = []SourceSpec{
	{
		Key:   "csv",
		Label: "CSV",
		Layouts: []LayoutSpec{
			{
				Key:   "signed",
				Label: "Signed amount",
				Fields: []FieldSpec{
					fieldDate,
					{
						Key: "amount", Label: "Amount", Required: true, Kind: KindColumn,
						Aliases: []string{"amount", "transaction amount", "amt", "sum", "value", "debit amount"},
					},
					fieldMerchant,
					{
						Key: "sign_convention", Label: "Sign convention", Required: true, Kind: KindEnum,
						EnumValues:  []string{"positive_is_credit", "positive_is_debit"},
						Description: "Whether a positive amount means money in (credit) or money out (debit)",
					},
					fieldImportedID,
					fieldBalance,
				},
			},
			{
				Key:   "indicator",
				Label: "Amount + debit/credit indicator",
				Fields: []FieldSpec{
					fieldDate,
					{
						Key: "amount", Label: "Amount", Required: true, Kind: KindColumn,
						Aliases: []string{"amount", "transaction amount", "amt", "sum", "value"},
					},
					{
						Key: "dc_indicator", Label: "Debit/credit indicator column", Required: true, Kind: KindColumn,
						Aliases: []string{"type", "transaction type", "dr/cr", "debit/credit",
							"cr/dr", "drcr", "indicator", "debit or credit"},
					},
					fieldMerchant,
					fieldImportedID,
					fieldBalance,
				},
			},
			{
				Key:   "split",
				Label: "Separate debit and credit columns",
				Fields: []FieldSpec{
					fieldDate,
					{
						Key: "debit", Label: "Debit / Withdrawal column", Required: true, Kind: KindColumn,
						Aliases: []string{"debit", "withdrawal", "withdrawals", "debit amount",
							"money out", "charge", "payments out"},
					},
					{
						Key: "credit", Label: "Credit / Deposit column", Required: true, Kind: KindColumn,
						Aliases: []string{"credit", "deposit", "deposits", "credit amount",
							"money in", "payment", "payments in"},
					},
					fieldMerchant,
					fieldImportedID,
					fieldBalance,
				},
			},
		},
	},
}

// GetLayout returns the layout spec for (sourceType, layoutKey), or nil if not found.
func GetLayout(sourceType, layoutKey string) *LayoutSpec {
	for i := range Registry {
		if Registry[i].Key == sourceType {
			for j := range Registry[i].Layouts {
				if Registry[i].Layouts[j].Key == layoutKey {
					return &Registry[i].Layouts[j]
				}
			}
		}
	}
	return nil
}

// ValidateConfig checks that cfg is valid for the given source type, returning
// a descriptive error if any required field is missing or an enum value is invalid.
func ValidateConfig(sourceType string, cfg MappingConfig) error {
	layout := GetLayout(sourceType, cfg.Layout)
	if layout == nil {
		return fmt.Errorf("unknown layout %q for source type %q", cfg.Layout, sourceType)
	}
	for _, f := range layout.Fields {
		val := cfg.Fields[f.Key]
		if f.Required && val == "" {
			return fmt.Errorf("required field %q is not mapped", f.Key)
		}
		if val != "" && f.Kind == KindEnum && !slices.Contains(f.EnumValues, val) {
			return fmt.Errorf("field %q value %q is not one of %v", f.Key, val, f.EnumValues)
		}
	}
	return nil
}

// SuggestMappings returns a field→column map built by matching CSV headers
// against each field's alias list (case-insensitive). Useful for auto-detection.
func SuggestMappings(sourceType, layoutKey string, headers []string) map[string]string {
	layout := GetLayout(sourceType, layoutKey)
	if layout == nil {
		return nil
	}
	lower := make([]string, len(headers))
	for i, h := range headers {
		lower[i] = lowerTrim(h)
	}
	result := make(map[string]string, len(layout.Fields))
	for _, f := range layout.Fields {
		if f.Kind != KindColumn {
			continue
		}
		for _, alias := range f.Aliases {
			for i, h := range lower {
				if h == alias {
					result[f.Key] = headers[i]
					goto nextField
				}
			}
		}
	nextField:
	}
	return result
}

func lowerTrim(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		if c != ' ' || (len(out) > 0 && out[len(out)-1] != ' ') {
			out = append(out, c)
		}
	}
	// trim trailing space
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return string(out)
}
