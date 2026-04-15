package build

import (
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
	"github.com/greatliontech/revolut-go/cmd/revogen/names"
)

// unionDeclFromSchema classifies a schema's union shape (if any)
// into the ir.Decl form. Called from declFromSchema when a schema
// has no concrete `type` but carries discriminator / oneOf / anyOf.
//
// Four outcomes:
//
//  1. discriminator.propertyName + mapping: real wire-tagged union.
//     Returns a DeclInterface with Discriminator set; each mapped
//     variant will get an UnionDispatch link attached by the
//     lower/unions pass so MarshalJSON injects the tag.
//
//  2. discriminator.mapping only (no propertyName): editorial
//     grouping that has no wire dispatch. Returns a DeclStruct
//     whose fields are merged from every mapped variant — this is
//     how Revolut actually uses the construct (flat payload,
//     distinguished by field presence).
//
//  3. oneOf / anyOf of pure $ref branches: untagged union. Returns
//     DeclInterface with Variants carrying RequiredProbe (the JSON
//     keys that uniquely identify each variant). If probes aren't
//     disjoint, fall back to the flatten form so the user gets a
//     usable concrete type instead of an unresolvable interface.
//
//  4. conditional-required anyOf (every branch only declares
//     `required:`): not a type union. Returns nil and callers
//     continue through declFromSchema's other classifiers — the
//     struct path will lift the groups via
//     `isConditionalRequiredAnyOf`.
//
// Returns nil when nothing matches; callers then fall through to
// the alias path.
func (b *Builder) unionDeclFromSchema(goName string, s *openapi3.Schema) *ir.Decl {
	// 4: leave to the struct/anyOf-validator path.
	if isConditionalRequiredAnyOf(s) {
		return nil
	}

	// 1 & 2: discriminator-based classification.
	if s.Discriminator != nil && len(s.Discriminator.Mapping) > 0 {
		return b.unionFromDiscriminator(goName, s)
	}

	// 3: untagged oneOf / anyOf with all-$ref branches.
	if len(s.OneOf) > 0 {
		if variants := b.unionFromRefBranches(s.OneOf); len(variants) > 0 {
			return b.untaggedUnion(goName, s, variants)
		}
	}
	if len(s.AnyOf) > 0 {
		if variants := b.unionFromRefBranches(s.AnyOf); len(variants) > 0 {
			return b.untaggedUnion(goName, s, variants)
		}
	}
	return nil
}

func (b *Builder) unionFromDiscriminator(goName string, s *openapi3.Schema) *ir.Decl {
	mapping := make([]mappedVariant, 0, len(s.Discriminator.Mapping))
	for _, tag := range sortedKeys(s.Discriminator.Mapping) {
		ref := s.Discriminator.Mapping[tag].Ref
		const prefix = "#/components/schemas/"
		if !strings.HasPrefix(ref, prefix) {
			continue
		}
		specName := strings.TrimPrefix(ref, prefix)
		mapping = append(mapping, mappedVariant{
			Tag:      tag,
			SpecName: specName,
			GoName:   b.resolvedName(specName),
		})
	}
	if len(mapping) == 0 {
		return nil
	}

	if s.Discriminator.PropertyName != "" {
		// Real wire-tagged union.
		variants := make([]ir.Variant, 0, len(mapping))
		for _, m := range mapping {
			variants = append(variants, ir.Variant{GoName: m.GoName, Tag: m.Tag})
		}
		return &ir.Decl{
			Name:          goName,
			Kind:          ir.DeclInterface,
			Doc:           s.Description,
			MarkerMethod:  "is" + goName,
			Variants:      variants,
			Discriminator: &ir.Discriminator{PropertyName: s.Discriminator.PropertyName},
		}
	}

	// propertyName absent: editorial grouping. Merge variants into
	// a single flat struct. Every variant's fields become optional
	// on the merged shape (since at most one set of fields applies
	// per wire payload).
	return b.flattenMappedVariants(goName, s, mapping)
}

func (b *Builder) untaggedUnion(goName string, s *openapi3.Schema, variants []ir.Variant) *ir.Decl {
	// A probe decoder only works if each variant has a required-
	// field set that uniquely identifies it. Compute probes and
	// check for disjointness.
	probes, disjoint := b.probesForVariants(variants)
	if !disjoint {
		// Flatten: merge fields from every variant into a single
		// struct with all fields optional.
		mapping := make([]mappedVariant, 0, len(variants))
		for _, v := range variants {
			mapping = append(mapping, mappedVariant{
				Tag:      v.GoName,
				SpecName: v.GoName, // resolvedName already applied
				GoName:   v.GoName,
			})
		}
		return b.flattenMappedVariants(goName, s, mapping)
	}
	for i := range variants {
		variants[i].RequiredProbe = probes[i]
	}
	return &ir.Decl{
		Name:         goName,
		Kind:         ir.DeclInterface,
		Doc:          s.Description,
		MarkerMethod: "is" + goName,
		Variants:     variants,
	}
}

// probesForVariants returns each variant's unique required-field
// set — the JSON names that appear in this variant's `required:`
// but not in any other variant's. If any variant has no unique
// required field, the function reports disjoint=false and callers
// must fall back to flattening.
func (b *Builder) probesForVariants(variants []ir.Variant) ([][]string, bool) {
	perVariant := make([][]string, len(variants))
	reqs := make([]map[string]bool, len(variants))
	for i, v := range variants {
		decl, ok := b.declByName[v.GoName]
		if !ok || decl.Kind != ir.DeclStruct {
			return nil, false
		}
		set := map[string]bool{}
		for _, f := range decl.Fields {
			if f.Required {
				set[f.JSONName] = true
			}
		}
		reqs[i] = set
	}
	for i, set := range reqs {
		var unique []string
		for name := range set {
			uniqueToI := true
			for j, other := range reqs {
				if j == i {
					continue
				}
				if other[name] {
					uniqueToI = false
					break
				}
			}
			if uniqueToI {
				unique = append(unique, name)
			}
		}
		if len(unique) == 0 {
			return nil, false
		}
		sort.Strings(unique)
		perVariant[i] = unique
	}
	return perVariant, true
}

// mappedVariant carries the three names a variant needs: the
// discriminator tag (wire value), the spec schema name (for looking
// up the source schema), and the resolved Go name.
type mappedVariant struct {
	Tag      string
	SpecName string
	GoName   string
}

// flattenMappedVariants produces a merged DeclStruct whose fields
// are the union of every mapped variant's fields, all marked
// optional. If two variants declare the same JSON field with
// differing types, the FIRST variant's type wins; a TODO comment
// surfaces the conflict in godoc so the discrepancy is visible.
// Ordering matches the mapping slice.
func (b *Builder) flattenMappedVariants(goName string, s *openapi3.Schema, mapping []mappedVariant) *ir.Decl {
	// Build a migration-aware doc block: call out the variants the
	// spec groups under this schema so users coming from prior
	// SDK versions know where their per-variant types went.
	doc := s.Description
	if doc != "" {
		doc += "\n\n"
	}
	doc += "The spec groups these shapes under a documentation-only discriminator "
	doc += "(no wire-level propertyName); fill in the fields that apply to your "
	doc += "scenario. Groupings:"
	for _, m := range mapping {
		doc += "\n  - " + m.Tag + ": " + m.SpecName
	}
	decl := &ir.Decl{
		Name: goName,
		Kind: ir.DeclStruct,
		Doc:  doc,
	}
	seen := map[string]bool{}
	for _, m := range mapping {
		ref := b.doc.Components.Schemas[m.SpecName]
		if ref == nil || ref.Value == nil {
			continue
		}
		v := ref.Value
		if len(v.AllOf) > 0 {
			if merged := mergeAllOf(v); merged != nil {
				v = merged
			}
		}
		for _, propName := range sortedKeys(v.Properties) {
			if seen[propName] {
				continue
			}
			seen[propName] = true
			prop := v.Properties[propName]
			if prop == nil || prop.Value == nil {
				continue
			}
			fieldType := b.resolveType(prop, Context{Parent: goName, Field: propName})
			if fieldType == nil {
				continue
			}
			if fieldType.GoExpr() == "time.Time" {
				fieldType = ir.Pointer(fieldType)
			}
			decl.Fields = append(decl.Fields, &ir.Field{
				JSONName:   propName,
				GoName:     names.FieldName(propName),
				Type:       fieldType,
				Required:   false, // every field becomes optional on the merged shape
				ReadOnly:   prop.Value.ReadOnly,
				WriteOnly:  prop.Value.WriteOnly,
				Deprecated: deprecationMessage(prop.Value),
				Doc:        firstLine(prop.Value.Description),
				DefaultDoc: defaultDoc(prop.Value),
			})
		}
	}
	return decl
}

// unionFromRefBranches turns a oneOf/anyOf of $ref branches into
// ordered Variants. Returns nil when any branch is inline (no
// stable name available) or when the slice is empty.
func (b *Builder) unionFromRefBranches(branches openapi3.SchemaRefs) []ir.Variant {
	if len(branches) == 0 {
		return nil
	}
	out := make([]ir.Variant, 0, len(branches))
	for _, br := range branches {
		if br == nil {
			return nil
		}
		specName := schemaRefName(br)
		if specName == "" {
			return nil
		}
		goName := b.resolvedName(specName)
		out = append(out, ir.Variant{GoName: goName, Tag: specName})
	}
	return out
}
