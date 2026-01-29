# Issue: Bundler Breaks External Schemas with Self-References

## Overview
After upgrading from libopenapi v0.25.0 to v0.33.0, bundling fails for OpenAPI specs that contain external schemas with self-references. The bundler inlines the schema body but leaves internal `$ref` pointers unchanged, creating dangling references.

## The Problem

### Error Message
```
cannot compose bundle document: invalid model
component `#/components/schemas/someRecursiveSchema` does not exist in the specification
```

This error occurs in the **composition step** after inline bundling succeeds.

### What is a Self-Referencing Schema?
A self-referencing (recursive) schema is one that refers to itself, commonly used for tree structures:

```yaml
components:
  schemas:
    Tree:
      type: object
      properties:
        name:
          type: string
        children:
          type: array
          items:
            $ref: '#/components/schemas/Tree'  # Self-reference
```

### Reproduction Case

**main.yaml:**
```yaml
openapi: 3.1.0
info:
  title: Test API
  version: 1.0.0
paths:
  /tree:
    get:
      responses:
        '200':
          description: Success
          content:
            application/json:
              schema:
                $ref: './external.yaml#/components/schemas/Tree'
```

**external.yaml:**
```yaml
openapi: 3.1.0
components:
  schemas:
    Tree:
      type: object
      properties:
        name:
          type: string
        children:
          type: array
          items:
            $ref: '#/components/schemas/Tree'
```

**Code:**
```go
config := &bundler.BundleInlineConfig{
    ResolveDiscriminatorExternalRefs: true,
}
inlinedDoc, err := bundler.BundleBytesWithConfig(mainYamlBytes, config)
// ✓ This succeeds

composedDoc, err := bundler.BundleBytesComposed(inlinedDoc)
// ✗ This fails with: component `#/components/schemas/Tree` does not exist
```

## Root Cause Analysis

### What Changed in v0.33?

Between v0.25 and v0.33, libopenapi introduced significant bundling changes:

1. **New discriminator handling**: `ResolveDiscriminatorExternalRefs` flag copies external schemas referenced by discriminators to root components
2. **Bundling mode tracking**: Better control over when internal refs are preserved
3. **Collision avoidance**: External schemas get renamed with suffixes when copied
4. **Context-based ref preservation**: Discriminator oneOf/anyOf refs are marked to prevent incorrect inlining

### The Bug Mechanism

When bundling with `ResolveDiscriminatorExternalRefs: true`:

1. External file contains a recursive schema with a local self-reference (`#/components/schemas/Tree`)
2. Schema is NOT discriminator-mapped, so discriminator handling ignores it
3. Inline bundling copies the schema body directly into the response location
4. **The self-reference inside the copied schema still points to the original name**
5. Composition validates all refs → finds dangling `#/components/schemas/Tree` → FAILS

**Actual output in bundled file:**
```yaml
paths:
  /tree:
    get:
      responses:
        '200':
          content:
            application/json:
              schema:
                type: object
                properties:
                  name:
                    type: string
                  children:
                    type: array
                    items:
                      $ref: '#/components/schemas/Tree'  # ← Dangling!
components:
  schemas: {}  # Tree is NOT here
```

### Why This Worked in v0.25

v0.25:
- Used `model.Render()` with manual node.Content copying
- Had different (less aggressive) external ref inlining behavior
- Didn't have the discriminator copying mechanism

v0.33:
- Uses `model.RenderInline()` with recursive schema traversal
- More aggressive about inlining non-discriminator external refs
- Has special handling for discriminator refs but not recursive refs

### Why Discriminator Schemas Work

The `resolveDiscriminatorExternalRefs()` function specifically:
1. Detects schemas referenced by discriminators in oneOf/anyOf
2. Copies them to root components
3. Marks their refs as "preserved" to prevent inlining
4. Updates discriminator mappings to point to new locations

**Recursive schemas need similar treatment.**

## Comparison: What Works vs What Breaks

### ✓ Works: Local Recursive Schema
```yaml
openapi: 3.1.0
components:
  schemas:
    Tree:
      type: object
      properties:
        name:
          type: string
        children:
          type: array
          items:
            $ref: '#/components/schemas/Tree'
paths:
  /tree:
    get:
      responses:
        '200':
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Tree'
```
Self-reference stays valid because schema is already in components.

### ✓ Works: External Discriminator Schema
```yaml
# main.yaml
schema:
  oneOf:
    - $ref: './external.yaml#/components/schemas/Dog'
  discriminator:
    propertyName: type
```
`ResolveDiscriminatorExternalRefs` copies Dog to components.

### ✗ Breaks: External Recursive Schema
```yaml
# main.yaml
schema:
  $ref: './external.yaml#/components/schemas/Tree'

# external.yaml
components:
  schemas:
    Tree:
      properties:
        children:
          items:
            $ref: '#/components/schemas/Tree'
```
Schema gets inlined, self-ref becomes dangling.

## Proposed Solution

Add a new config flag `ResolveRecursiveExternalSchemas` with logic similar to `resolveDiscriminatorExternalRefs()`:

### Algorithm
1. **Detection Phase**: Before calling `RenderInline()`, scan all external indexes for schemas containing self-references
2. **Copy Phase**: Copy those schemas to root components using `copySchemaToComponents()`
3. **Preservation Phase**: Mark them to NOT be inlined during rendering
4. **Validation**: Self-references remain valid because schema is in components

### Implementation Sketch

```go
// In BundleInlineConfig
type BundleInlineConfig struct {
    ResolveDiscriminatorExternalRefs bool
    ResolveRecursiveExternalSchemas  bool  // New flag
    // ...
}

// New function (similar to resolveDiscriminatorExternalRefs)
func resolveRecursiveExternalSchemas(
    rootIndex *index.SpecIndex,
    config *BundleInlineConfig,
) error {
    // Iterate through external indexes
    for _, extIndex := range rootIndex.GetExternalIndexes() {
        // Find schemas with self-references
        for schemaName, schemaRef := range extIndex.GetAllSchemas() {
            if containsSelfReference(schemaRef, schemaName) {
                // Copy to root components
                copySchemaToComponents(rootIndex, extIndex, schemaName)
                // Mark refs as preserved
                markRefsAsPreserved(schemaRef)
            }
        }
    }
    return nil
}

// Helper to detect self-references
func containsSelfReference(schemaRef *index.Reference, schemaName string) bool {
    // Walk the schema tree looking for $ref pointing to own name
    // e.g., $ref: '#/components/schemas/Tree' where schemaName == 'Tree'
}
```

### Files to Modify
- `/bundler/bundler.go`: Add flag and `resolveRecursiveExternalSchemas()` function
- `/bundler/bundler.go`: Call new function in `bundleWithConfig()` when flag is true

## Testing Strategy

### Unit Test (bundler_test.go)
```go
func TestBundleBytesWithConfig_ExternalRecursiveSchema(t *testing.T) {
    // Create temp directory
    // Write main.yaml with response referencing external Tree schema
    // Write external.yaml with self-referencing Tree schema

    config := &BundleInlineConfig{
        ResolveRecursiveExternalSchemas: true,
    }

    // Bundle and compose
    inlined, err := BundleBytesWithConfig(mainBytes, config)
    assert.NoError(t, err)

    composed, err := BundleBytesComposed(inlined)
    assert.NoError(t, err)

    // Verify Tree exists in components
    doc, _ := libopenapi.NewDocument(composed)
    v3Model, _ := doc.BuildV3Model()
    assert.NotNil(t, v3Model.Model.Components.Schemas.GetOrZero("Tree"))

    // Verify self-reference is valid
    tree := v3Model.Model.Components.Schemas.GetOrZero("Tree")
    childrenRef := tree.Schema().Properties.GetOrZero("children").Items.A.SchemaReference
    assert.Equal(t, "#/components/schemas/Tree", childrenRef)
}
```

### Integration Test
Run the rest-proxy transform tests that currently fail:
```bash
cd rest-proxy-preprocessor
RESET_EXPECTED_TEST_FILES=true go test ./lib/transform/...
```

## Alternative Solutions Considered

### Alt 1: Skip Composition for Tests
**Rejected**: This is a workaround that doesn't fix the real issue. Production specs might still break.

### Alt 2: Move Recursive Schemas to Main Files
**Rejected**: Only fixes test files, not the underlying issue. Real specs may have external recursive schemas.

### Alt 3: Wait for Upstream Fix
**Rejected**: No immediate solution, blocks the v0.33 upgrade which is needed for discriminator fixes.

### Alt 4: Patch Locally (CHOSEN)
**Accepted**: You're already maintaining forks of walky and yaml-jsonpath. Adding libopenapi patches is acceptable and can be contributed upstream later.

## Impact

### Who This Affects
Any OpenAPI spec that:
- References external files
- External files contain self-referencing schemas
- Uses `BundleBytesComposed()` for validation

### Workaround (Until Fixed)
1. Move recursive schemas to main file
2. Or use v0.25.0
3. Or skip composition step (loses validation)

## References
- libopenapi bundler: `/bundler/bundler.go`
- Discriminator handling: `resolveDiscriminatorExternalRefs()` function
- Related test: `bundler_test.go:878-929` (discriminator external refs)
- No existing test for external recursive schemas
