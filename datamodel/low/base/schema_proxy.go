// Copyright 2022 Princess B33f Heavy Industries / Dave Shanley
// SPDX-License-Identifier: MIT

package base

import (
	"github.com/pb33f/libopenapi/datamodel/low"
	"github.com/pb33f/libopenapi/index"
	"gopkg.in/yaml.v3"
)

// SchemaProxy exists as a stub that will create a Schema once (and only once) the Schema() method is called.
//
// Why use a Proxy design?
//
// There are three reasons.
//
// 1. Circular References and Endless Loops.
//
// JSON Schema allows for references to be used. This means references can loop around and create infinite recursive
// structures, These 'Circular references' technically mean a schema can NEVER be resolved, not without breaking the
// loop somewhere along the chain.
//
// Polymorphism in the form of 'oneOf' and 'anyOf' in version 3+ only exacerbates the problem.
//
// These circular traps can be discovered using the resolver, however it's still not enough to stop endless loops and
// endless goroutine spawning. A proxy design means that resolving occurs on demand and runs down a single level only.
// preventing any run-away loops.
//
// 2. Performance
//
// Even without circular references, Polymorphism creates large additional resolving chains that take a long time
// and slow things down when building. By preventing recursion through every polymorphic item, building models is kept
// fast and snappy, which is desired for realtime processing of specs.
//
//  - Q: Yeah, but, why not just use state to avoiding re-visiting seen polymorphic nodes?
//  - A: It's slow, takes up memory and still has runaway potential in very, very long chains.
//
// 3. Short Circuit Errors.
//
// Schemas are where things can get messy, mainly because the Schema standard changes between versions, and
// it's not actually JSONSchema until 3.1, so lots of times a bad schema will break parsing. Errors are only found
// when a schema is needed, so the rest of the document is parsed and ready to use.
type SchemaProxy struct {
	kn         *yaml.Node
	vn         *yaml.Node
	idx        *index.SpecIndex
	rendered   *Schema
	buildError error
}

// Build will prepare the SchemaProxy for rendering, it does not build the Schema, only sets up internal state.
func (sp *SchemaProxy) Build(root *yaml.Node, idx *index.SpecIndex) error {
	sp.vn = root
	sp.idx = idx
	return nil
}

// Schema will first check if this SchemaProxy has already rendered the schema, and return the pre-rendered version
// first.
//
// If this is the first run of Schema(), then the SchemaProxy will create a new Schema from the underlying
// yaml.Node. Once built out, the SchemaProxy will record that Schema as rendered and store it for later use,
// (this is what is we mean when we say 'pre-rendered').
//
// Schema() then returns the newly created Schema.
//
// If anything goes wrong during the build, then nothing is returned and the error that occurred can
// be retrieved by using GetBuildError()
func (sp *SchemaProxy) Schema() *Schema {
	if sp.rendered != nil {
		return sp.rendered
	}
	schema := new(Schema)
	_ = low.BuildModel(sp.vn, schema)
	err := schema.Build(sp.vn, sp.idx)
	if err != nil {
		sp.buildError = err
		return nil
	}
	sp.rendered = schema
	return schema
}

// GetBuildError returns the build error that was set when Schema() was called. If Schema() has not been run, or
// there were no errors during build, then nil will be returned.
func (sp *SchemaProxy) GetBuildError() error {
	return sp.buildError
}