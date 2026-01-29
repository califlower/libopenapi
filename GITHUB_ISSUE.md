# Bundler breaks external schemas with self-references

## Problem
When bundling OpenAPI specs, external schemas that contain self-references get inlined, but their internal `$ref` pointers remain unchanged, creating dangling references that cause composition to fail.

## Example

**main.yaml:**
```yaml
openapi: 3.1.0
paths:
  /tree:
    get:
      responses:
        '200':
          content:
            application/json:
              schema:
                $ref: './external.yaml#/components/schemas/Tree'
```

**external.yaml:**
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

## Expected Behavior
The `Tree` schema should be copied to the root document's components with the self-reference remaining valid:
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
            $ref: '#/components/schemas/Tree'  # Still valid
```

## Actual Behavior
The `Tree` schema body is inlined directly into the response, but the self-reference still points to `#/components/schemas/Tree`, which doesn't exist in the root document:
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
                      $ref: '#/components/schemas/Tree'  # Dangling reference!
```

Calling `BundleBytesComposed()` fails with:
```
component `#/components/schemas/Tree` does not exist in the specification
```

## Why This Happens
- In v0.33, non-discriminator external refs are aggressively inlined
- `ResolveDiscriminatorExternalRefs` flag only handles discriminator-mapped schemas
- When a schema is inlined, its body is copied but internal refs aren't updated
- Recursive schemas need special handling to be copied to components instead of inlined

## Suggested Fix
Add similar logic to `resolveDiscriminatorExternalRefs()` that detects external schemas with self-references and copies them to components instead of inlining them.

## Version
libopenapi v0.33.0 (issue not present in v0.25.0)
